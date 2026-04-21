package fuse

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/s3gateway/internal/catalog"
	"github.com/s3gateway/internal/s3client"
)

type Handle struct {
	file       *FileNode
	s3         *s3client.Client
	bucket     string
	key        string
	ttl        time.Duration
	soleWriter bool

	tmpFile *os.File
	dirty   bool
	mu      sync.Mutex
}

var _ = (fs.FileReader)((*Handle)(nil))
var _ = (fs.FileWriter)((*Handle)(nil))
var _ = (fs.FileFlusher)((*Handle)(nil))
var _ = (fs.FileReleaser)((*Handle)(nil))
var _ = (fs.FileFsyncer)((*Handle)(nil))

func newHandle(f *FileNode) (*Handle, error) {
	tmpFile, err := os.CreateTemp("", "s3gw-upload-*")
	if err != nil {
		return nil, err
	}

	return &Handle{
		file:       f,
		s3:         f.bctx.s3,
		bucket:     f.bctx.bucket,
		key:        f.key,
		ttl:        f.bctx.ttl,
		soleWriter: f.bctx.soleWriter,
		tmpFile:    tmpFile,
	}, nil
}

func (h *Handle) Read(ctx context.Context, dest []byte, off int64) (gofuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cm := h.file.bctx.cache

	if h.dirty {
		n, err := h.tmpFile.ReadAt(dest, off)
		if err != nil && err != io.EOF {
			return nil, syscall.EIO
		}
		return gofuse.ReadResultData(dest[:n]), 0
	}

	_, entry, valid, err := cm.Get(h.bucket, h.key, h.ttl, h.soleWriter)
	if err != nil {
		slog.Error("cache get error", "key", h.key, "error", err)
	}

	if valid {
		n, readErr := cm.Store().Read(h.bucket, h.key, off, dest)
		if readErr != nil && readErr != io.EOF {
			slog.Warn("cache read error, falling through to S3", "key", h.key, "error", readErr)
		} else {
			return gofuse.ReadResultData(dest[:n]), 0
		}
	}

	if !h.soleWriter && entry != nil {
		s3Meta, headErr := h.s3.HeadObject(ctx, h.key)
		if headErr == nil && s3Meta.ETag == entry.ETag {
			cm.Touch(h.bucket, h.key)
			n, readErr := cm.Store().Read(h.bucket, h.key, off, dest)
			if readErr == nil || readErr == io.EOF {
				return gofuse.ReadResultData(dest[:n]), 0
			}
		}
	}

	return h.fetchFromS3AndCache(ctx, dest, off)
}

func (h *Handle) fetchFromS3AndCache(ctx context.Context, dest []byte, off int64) (gofuse.ReadResult, syscall.Errno) {
	cm := h.file.bctx.cache

	body, meta, err := h.s3.GetObject(ctx, h.key)
	if err != nil {
		slog.Error("s3 get failed", "key", h.key, "error", err)
		return nil, s3client.TranslateError(err)
	}
	defer body.Close()

	cacheTmp, err := os.CreateTemp("", "s3gw-cache-*")
	if err != nil {
		slog.Error("create cache temp failed", "error", err)
		return nil, syscall.EIO
	}
	cacheTmpName := cacheTmp.Name()
	defer os.Remove(cacheTmpName)

	if _, err := io.Copy(cacheTmp, body); err != nil {
		cacheTmp.Close()
		slog.Error("download to cache failed", "key", h.key, "error", err)
		return nil, syscall.EIO
	}

	if putErr := cm.Put(h.bucket, h.key, cacheTmp, *meta); putErr != nil {
		slog.Warn("cache put after download failed", "key", h.key, "error", putErr)
	}
	cacheTmp.Close()

	h.file.update(meta.Size, meta.ETag, meta.LastModified)

	n, err := cm.Store().Read(h.bucket, h.key, off, dest)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}

	return gofuse.ReadResultData(dest[:n]), 0
}

func (h *Handle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	n, err := h.tmpFile.WriteAt(data, off)
	if err != nil {
		slog.Error("tmp write failed", "key", h.key, "error", err)
		return 0, syscall.EIO
	}
	h.dirty = true
	return uint32(n), 0
}

func (h *Handle) Flush(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.dirty {
		return 0
	}

	if _, err := h.tmpFile.Seek(0, io.SeekStart); err != nil {
		slog.Error("seek failed on flush", "key", h.key, "error", err)
		return syscall.EIO
	}

	stat, err := h.tmpFile.Stat()
	if err != nil {
		slog.Error("stat failed on flush", "key", h.key, "error", err)
		return syscall.EIO
	}
	size := stat.Size()

	wb := h.file.bctx.writeBack
	if wb != nil {
		// Write-back mode: copy tmpFile to a staging file and submit async
		stagingFile, err := os.CreateTemp("", "s3gw-wb-*")
		if err != nil {
			slog.Error("create staging file failed", "key", h.key, "error", err)
			return syscall.EIO
		}

		if _, err := io.Copy(stagingFile, h.tmpFile); err != nil {
			stagingFile.Close()
			os.Remove(stagingFile.Name())
			slog.Error("copy to staging failed", "key", h.key, "error", err)
			return syscall.EIO
		}
		stagingFile.Close()

		// Update catalog immediately to reflect the new file
		parent, baseName := catalog.SplitKeyExported(h.key)
		h.file.bctx.catalog.Put(h.bucket, &catalog.Entry{
			Key:      h.key,
			Parent:   parent,
			Name:     baseName,
			Size:     size,
			Modified: time.Now(),
		})

		// Cache the file locally
		cacheFile, openErr := os.Open(stagingFile.Name())
		if openErr == nil {
			dummyMeta := s3client.ObjectMeta{Size: size, LastModified: time.Now()}
			if putErr := h.file.bctx.cache.Put(h.bucket, h.key, cacheFile, dummyMeta); putErr != nil {
				slog.Warn("cache put on write-back failed", "key", h.key, "error", putErr)
			}
			cacheFile.Close()
		}

		h.file.update(size, h.file.etagSnapshot(), time.Now())
		h.dirty = false

		job := &catalog.UploadJob{
			Bucket:   h.bucket,
			Key:      h.key,
			FilePath: stagingFile.Name(),
			Size:     size,
		}

		if err := wb.Submit(job); err != nil {
			slog.Warn("write-back queue full, uploading synchronously", "key", h.key)
			return h.syncUpload(ctx, size)
		}

		return 0
	}

	return h.syncUpload(ctx, size)
}

func (h *Handle) syncUpload(ctx context.Context, size int64) syscall.Errno {
	if _, err := h.tmpFile.Seek(0, io.SeekStart); err != nil {
		slog.Error("seek failed on sync upload", "key", h.key, "error", err)
		return syscall.EIO
	}

	objMeta, err := h.s3.PutObject(ctx, h.key, h.tmpFile, size)
	if err != nil {
		slog.Error("S3 PUT FAILED - write rejected", "key", h.key, "error", err)
		return syscall.EIO
	}

	cacheFile, openErr := os.Open(h.tmpFile.Name())
	if openErr == nil {
		if putErr := h.file.bctx.cache.Put(h.bucket, h.key, cacheFile, *objMeta); putErr != nil {
			slog.Warn("cache update after write failed", "key", h.key, "error", putErr)
		}
		cacheFile.Close()
	}

	parent, baseName := catalog.SplitKeyExported(h.key)
	h.file.bctx.catalog.Put(h.bucket, &catalog.Entry{
		Key:      h.key,
		Parent:   parent,
		Name:     baseName,
		Size:     size,
		ETag:     objMeta.ETag,
		Modified: time.Now(),
	})

	h.file.update(size, objMeta.ETag, time.Now())
	h.dirty = false

	return 0
}

func (h *Handle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return h.Flush(ctx)
}

func (h *Handle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.tmpFile != nil {
		name := h.tmpFile.Name()
		h.tmpFile.Close()
		os.Remove(name)
		h.tmpFile = nil
	}

	return 0
}
