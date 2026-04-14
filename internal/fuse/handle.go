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
		ttl:        time.Duration(f.bctx.ttl),
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

	h.file.size = meta.Size
	h.file.etag = meta.ETag
	h.file.mtime = meta.LastModified

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

	h.file.size = size
	h.file.etag = objMeta.ETag
	h.file.mtime = time.Now()
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
