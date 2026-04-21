package fuse

import (
	"context"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
)

type FileNode struct {
	fs.Inode
	bctx *bucketCtx
	key  string

	mu    sync.RWMutex
	size  int64
	etag  string
	mtime time.Time
}

var _ = (fs.NodeGetattrer)((*FileNode)(nil))
var _ = (fs.NodeSetattrer)((*FileNode)(nil))
var _ = (fs.NodeOpener)((*FileNode)(nil))
var _ = (fs.NodeAccesser)((*FileNode)(nil))

func (f *FileNode) snapshot() (int64, string, time.Time) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.size, f.etag, f.mtime
}

func (f *FileNode) update(size int64, etag string, mtime time.Time) {
	f.mu.Lock()
	f.size = size
	f.etag = etag
	f.mtime = mtime
	f.mu.Unlock()
}

func (f *FileNode) setSize(size int64) {
	f.mu.Lock()
	f.size = size
	f.mu.Unlock()
}

func (f *FileNode) etagSnapshot() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.etag
}

func (f *FileNode) Access(ctx context.Context, mask uint32) syscall.Errno {
	return 0
}

func (f *FileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	size, _, mtime := f.snapshot()
	out.Mode = 0666
	out.Nlink = 1
	out.Size = uint64(size)
	out.SetTimes(&mtime, &mtime, &mtime)
	out.AttrValid = 1
	return 0
}

func (f *FileNode) Setattr(ctx context.Context, fh fs.FileHandle, in *gofuse.SetAttrIn, out *gofuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		f.setSize(int64(sz))
	}
	size, _, mtime := f.snapshot()
	out.Mode = 0666
	out.Nlink = 1
	out.Size = uint64(size)
	out.SetTimes(&mtime, &mtime, &mtime)
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
