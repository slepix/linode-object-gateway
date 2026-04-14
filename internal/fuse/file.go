package fuse

import (
	"context"
	"log/slog"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
)

type FileNode struct {
	fs.Inode
	bctx  *bucketCtx
	key   string
	size  int64
	etag  string
	mtime time.Time
}

var _ = (fs.NodeGetattrer)((*FileNode)(nil))
var _ = (fs.NodeSetattrer)((*FileNode)(nil))
var _ = (fs.NodeOpener)((*FileNode)(nil))

func (f *FileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	out.Mode = 0644
	out.Size = uint64(f.size)
	out.SetTimes(&f.mtime, &f.mtime, &f.mtime)
	out.AttrValid = 1
	return 0
}

func (f *FileNode) Setattr(ctx context.Context, fh fs.FileHandle, in *gofuse.SetAttrIn, out *gofuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		f.size = int64(sz)
	}
	out.Mode = 0644
	out.Size = uint64(f.size)
	out.SetTimes(&f.mtime, &f.mtime, &f.mtime)
	return 0
}

func (f *FileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	handle, err := newHandle(f)
	if err != nil {
		slog.Error("open handle failed", "key", f.key, "error", err)
		return nil, 0, syscall.EIO
	}
	return handle, gofuse.FOPEN_DIRECT_IO, 0
}
