package fuse

import (
	"context"
	"log/slog"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/s3gateway/internal/s3client"
)

type DirNode struct {
	fs.Inode
	bctx   *bucketCtx
	prefix string
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

func (d *DirNode) ttlDuration() time.Duration {
	return time.Duration(d.bctx.ttl)
}

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

	dirPrefix := childKey + "/"
	listResult, err := d.bctx.s3.ListObjects(ctx, dirPrefix, "/", nil)
	if err == nil && (len(listResult.Objects) > 0 || len(listResult.CommonPrefixes) > 0) {
		child := &DirNode{
			bctx:   d.bctx,
			prefix: dirPrefix,
		}
		out.Mode = 0777 | syscall.S_IFDIR
		out.Nlink = 2
		now := time.Now()
		out.SetTimes(&now, &now, &now)
		out.EntryValid = 1
		stable := fs.StableAttr{Mode: syscall.S_IFDIR}
		return d.NewInode(ctx, child, stable), 0
	}

	meta, err := d.bctx.s3.HeadObject(ctx, childKey)
	if err != nil {
		if d.bctx.s3.IsNotFound(err) {
			return nil, syscall.ENOENT
		}
		slog.Error("lookup head failed", "key", childKey, "error", err)
		return nil, s3client.TranslateError(err)
	}

	child := &FileNode{
		bctx: d.bctx,
		key:  childKey,
		size: meta.Size,
		etag: meta.ETag,
		mtime: meta.LastModified,
	}
	out.Mode = 0666
	out.Size = uint64(meta.Size)
	out.SetTimes(&meta.LastModified, &meta.LastModified, &meta.LastModified)
	out.EntryValid = 1
	stable := fs.StableAttr{Mode: syscall.S_IFREG}
	return d.NewInode(ctx, child, stable), 0
}

func (d *DirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	stream := &S3DirStream{
		s3:     d.bctx.s3,
		bucket: d.bctx.bucket,
		prefix: d.prefix,
		ctx:    ctx,
	}
	return stream, 0
}

func (d *DirNode) Mkdir(ctx context.Context, name string, mode uint32, out *gofuse.EntryOut) (*fs.Inode, syscall.Errno) {
	markerKey := d.prefix + name + "/"

	emptyReader := strings.NewReader("")
	_, err := d.bctx.s3.PutObject(ctx, markerKey, emptyReader, 0)
	if err != nil {
		slog.Error("mkdir failed", "key", markerKey, "error", err)
		return nil, s3client.TranslateError(err)
	}

	child := &DirNode{
		bctx:   d.bctx,
		prefix: markerKey,
	}
	out.Mode = 0777 | syscall.S_IFDIR
	out.Nlink = 2
	now := time.Now()
	out.SetTimes(&now, &now, &now)
	out.EntryValid = 1
	stable := fs.StableAttr{Mode: syscall.S_IFDIR}
	return d.NewInode(ctx, child, stable), 0
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

	out.Mode = 0666
	out.Size = 0
	now := time.Now()
	out.SetTimes(&now, &now, &now)
	out.EntryValid = 1
	stable := fs.StableAttr{Mode: syscall.S_IFREG}
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

	return 0
}
