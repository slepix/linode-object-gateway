package fuse

import (
	"context"
	"log/slog"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/s3gateway/internal/catalog"
	"github.com/s3gateway/internal/s3client"
)

type DirNode struct {
	fs.Inode
	bctx   *bucketCtx
	prefix string
}

type lookupResult struct {
	isDir bool
	meta  *s3client.ObjectMeta
}

var _ = (fs.NodeLookuper)((*DirNode)(nil))
var _ = (fs.NodeReaddirer)((*DirNode)(nil))
var _ = (fs.NodeMkdirer)((*DirNode)(nil))
var _ = (fs.NodeCreater)((*DirNode)(nil))
var _ = (fs.NodeUnlinker)((*DirNode)(nil))
var _ = (fs.NodeRmdirer)((*DirNode)(nil))
var _ = (fs.NodeRenamer)((*DirNode)(nil))
var _ = (fs.NodeGetattrer)((*DirNode)(nil))
var _ = (fs.NodeAccesser)((*DirNode)(nil))

func (d *DirNode) Access(ctx context.Context, mask uint32) syscall.Errno {
	return 0
}

func (d *DirNode) Getattr(ctx context.Context, fh fs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	out.Mode = 0777 | syscall.S_IFDIR
	out.Nlink = 2
	now := time.Now()
	out.SetTimes(&now, &now, &now)
	out.AttrValid = 1
	return 0
}

func (d *DirNode) Lookup(ctx context.Context, name string, out *gofuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childKey := d.prefix + name
	cat := d.bctx.catalog

	// Try catalog for directory
	dirKey := childKey + "/"
	entry, err := cat.Lookup(d.bctx.bucket, dirKey)
	if err != nil {
		slog.Error("catalog lookup failed", "key", dirKey, "error", err)
		return nil, syscall.EIO
	}
	if entry != nil && entry.IsDir {
		return d.makeDirInode(ctx, dirKey, out), 0
	}

	// Try catalog for directory by checking children
	has, err := cat.HasDir(d.bctx.bucket, dirKey)
	if err != nil {
		slog.Error("catalog hasdir failed", "key", dirKey, "error", err)
		return nil, syscall.EIO
	}
	if has {
		return d.makeDirInode(ctx, dirKey, out), 0
	}

	// Try catalog for file
	entry, err = cat.Lookup(d.bctx.bucket, childKey)
	if err != nil {
		slog.Error("catalog lookup failed", "key", childKey, "error", err)
		return nil, syscall.EIO
	}
	if entry != nil && !entry.IsDir {
		return d.makeFileInode(ctx, childKey, entry.Size, entry.ETag, entry.Modified, out), 0
	}

	// Catalog miss -- fall back to S3. Dedupe concurrent fallbacks for the
	// same child so a thundering herd collapses into a single round-trip.
	dirPrefix := childKey + "/"
	sfKey := "lookup:" + d.bctx.bucket + "/" + childKey
	resI, sfErr, _ := d.bctx.s3SF.Do(sfKey, func() (interface{}, error) {
		listResult, listErr := d.bctx.s3.ListObjects(ctx, dirPrefix, "/", nil)
		if listErr != nil {
			return nil, listErr
		}
		if len(listResult.Objects) > 0 || len(listResult.CommonPrefixes) > 0 {
			d.ingestListResult(dirPrefix, listResult)
			return lookupResult{isDir: true}, nil
		}
		meta, headErr := d.bctx.s3.HeadObject(ctx, childKey)
		if headErr != nil {
			return nil, headErr
		}
		return lookupResult{meta: meta}, nil
	})
	if sfErr != nil {
		if d.bctx.s3.IsNotFound(sfErr) {
			return nil, syscall.ENOENT
		}
		slog.Error("lookup fallback failed", "key", childKey, "error", sfErr)
		return nil, s3client.TranslateError(sfErr)
	}
	res := resI.(lookupResult)
	if res.isDir {
		return d.makeDirInode(ctx, dirPrefix, out), 0
	}
	meta := res.meta

	// Populate catalog with the discovered file
	parent, baseName := catalog.SplitKeyExported(childKey)
	cat.Put(d.bctx.bucket, &catalog.Entry{
		Key:      childKey,
		Parent:   parent,
		Name:     baseName,
		Size:     meta.Size,
		ETag:     meta.ETag,
		Modified: meta.LastModified,
	})

	return d.makeFileInode(ctx, childKey, meta.Size, meta.ETag, meta.LastModified, out), 0
}

func (d *DirNode) makeDirInode(ctx context.Context, prefix string, out *gofuse.EntryOut) *fs.Inode {
	child := &DirNode{bctx: d.bctx, prefix: prefix}
	out.Mode = 0777 | syscall.S_IFDIR
	out.Nlink = 2
	now := time.Now()
	out.SetTimes(&now, &now, &now)
	out.EntryValid = 1
	stable := fs.StableAttr{Mode: syscall.S_IFDIR, Ino: stableIno(prefix)}
	return d.NewInode(ctx, child, stable)
}

func (d *DirNode) makeFileInode(ctx context.Context, key string, size int64, etag string, mtime time.Time, out *gofuse.EntryOut) *fs.Inode {
	child := &FileNode{
		bctx:  d.bctx,
		key:   key,
		size:  size,
		etag:  etag,
		mtime: mtime,
	}
	out.Mode = 0666
	out.Nlink = 1
	out.Size = uint64(size)
	out.SetTimes(&mtime, &mtime, &mtime)
	out.EntryValid = 1
	stable := fs.StableAttr{Mode: syscall.S_IFREG, Ino: stableIno(key)}
	return d.NewInode(ctx, child, stable)
}

func (d *DirNode) ingestListResult(prefix string, result *s3client.ListResult) {
	cat := d.bctx.catalog
	bucket := d.bctx.bucket

	entries := make([]catalog.Entry, 0, len(result.CommonPrefixes)+len(result.Objects))

	for _, cp := range result.CommonPrefixes {
		parent, name := catalog.SplitKeyExported(cp)
		entries = append(entries, catalog.Entry{
			Key:    cp,
			Parent: parent,
			Name:   name,
			IsDir:  true,
		})
	}

	for _, obj := range result.Objects {
		if obj.Key == prefix || strings.HasSuffix(obj.Key, "/") {
			continue
		}
		parent, name := catalog.SplitKeyExported(obj.Key)
		entries = append(entries, catalog.Entry{
			Key:      obj.Key,
			Parent:   parent,
			Name:     name,
			Size:     obj.Size,
			ETag:     obj.ETag,
			Modified: obj.LastModified,
		})
	}

	if len(entries) == 0 {
		return
	}
	if err := cat.PutBatch(bucket, entries); err != nil {
		slog.Warn("ingest batch failed", "prefix", prefix, "error", err)
	}
}

func (d *DirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	cat := d.bctx.catalog

	// Try catalog first
	entries, err := cat.ListDir(d.bctx.bucket, d.prefix)
	if err == nil && len(entries) > 0 {
		return newCatalogDirStream(entries), 0
	}

	// Catalog empty for this prefix -- sync from S3 then retry
	if syncErr := cat.SyncPrefix(d.bctx.bucket, d.prefix); syncErr != nil {
		slog.Warn("readdir sync prefix failed, falling back to S3 stream",
			"prefix", d.prefix, "error", syncErr)
		stream := &S3DirStream{
			s3:     d.bctx.s3,
			bucket: d.bctx.bucket,
			prefix: d.prefix,
			ctx:    ctx,
		}
		return stream, 0
	}

	entries, err = cat.ListDir(d.bctx.bucket, d.prefix)
	if err != nil {
		slog.Error("readdir catalog list failed", "prefix", d.prefix, "error", err)
		stream := &S3DirStream{
			s3:     d.bctx.s3,
			bucket: d.bctx.bucket,
			prefix: d.prefix,
			ctx:    ctx,
		}
		return stream, 0
	}

	return newCatalogDirStream(entries), 0
}

func (d *DirNode) Mkdir(ctx context.Context, name string, mode uint32, out *gofuse.EntryOut) (*fs.Inode, syscall.Errno) {
	markerKey := d.prefix + name + "/"

	emptyReader := strings.NewReader("")
	_, err := d.bctx.s3.PutObject(ctx, markerKey, emptyReader, 0)
	if err != nil {
		slog.Error("mkdir failed", "key", markerKey, "error", err)
		return nil, s3client.TranslateError(err)
	}

	parent, baseName := catalog.SplitKeyExported(markerKey)
	d.bctx.catalog.Put(d.bctx.bucket, &catalog.Entry{
		Key:    markerKey,
		Parent: parent,
		Name:   baseName,
		IsDir:  true,
	})

	return d.makeDirInode(ctx, markerKey, out), 0
}

func (d *DirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *gofuse.EntryOut) (inode *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	key := d.prefix + name

	child := &FileNode{
		bctx:  d.bctx,
		key:   key,
		size:  0,
		mtime: time.Now(),
	}

	handle, err := newHandle(child)
	if err != nil {
		slog.Error("create handle failed", "key", key, "error", err)
		return nil, nil, 0, syscall.EIO
	}
	handle.dirty = true

	parent, baseName := catalog.SplitKeyExported(key)
	d.bctx.catalog.Put(d.bctx.bucket, &catalog.Entry{
		Key:      key,
		Parent:   parent,
		Name:     baseName,
		Size:     0,
		Modified: child.mtime,
	})

	out.Mode = 0666
	out.Nlink = 1
	out.Size = 0
	now := time.Now()
	out.SetTimes(&now, &now, &now)
	out.EntryValid = 1
	stable := fs.StableAttr{Mode: syscall.S_IFREG, Ino: stableIno(key)}
	inode = d.NewInode(ctx, child, stable)
	return inode, handle, gofuse.FOPEN_DIRECT_IO, 0
}

func (d *DirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	key := d.prefix + name

	if err := d.bctx.s3.DeleteObject(ctx, key); err != nil {
		slog.Error("unlink failed", "key", key, "error", err)
		return s3client.TranslateError(err)
	}

	d.bctx.cache.Invalidate(d.bctx.bucket, key)
	d.bctx.catalog.Delete(d.bctx.bucket, key)
	return 0
}

func (d *DirNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	dirPrefix := d.prefix + name + "/"

	listResult, err := d.bctx.s3.ListObjects(ctx, dirPrefix, "/", nil)
	if err != nil {
		return s3client.TranslateError(err)
	}

	hasContents := false
	for _, obj := range listResult.Objects {
		if obj.Key != dirPrefix {
			hasContents = true
			break
		}
	}
	if len(listResult.CommonPrefixes) > 0 {
		hasContents = true
	}

	if hasContents {
		return syscall.ENOTEMPTY
	}

	if err := d.bctx.s3.DeleteObject(ctx, dirPrefix); err != nil {
		slog.Error("rmdir failed", "key", dirPrefix, "error", err)
		return s3client.TranslateError(err)
	}

	d.bctx.catalog.DeletePrefix(d.bctx.bucket, dirPrefix)
	return 0
}

func (d *DirNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	srcKey := d.prefix + name

	var dstPrefix string
	switch p := newParent.(type) {
	case *DirNode:
		dstPrefix = p.prefix
	case *RootNode:
		dstPrefix = p.prefix
	default:
		return syscall.EINVAL
	}
	dstKey := dstPrefix + newName

	if err := d.bctx.s3.CopyObject(ctx, srcKey, dstKey); err != nil {
		slog.Error("rename copy failed", "src", srcKey, "dst", dstKey, "error", err)
		return s3client.TranslateError(err)
	}

	if err := d.bctx.s3.DeleteObject(ctx, srcKey); err != nil {
		slog.Warn("rename delete-source failed", "src", srcKey, "error", err)
	}

	d.bctx.cache.Invalidate(d.bctx.bucket, srcKey)
	d.bctx.cache.Invalidate(d.bctx.bucket, dstKey)
	d.bctx.catalog.Delete(d.bctx.bucket, srcKey)
	d.bctx.catalog.Delete(d.bctx.bucket, dstKey)

	return 0
}
