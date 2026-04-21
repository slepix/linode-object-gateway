package fuse

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/s3gateway/internal/cache"
	"github.com/s3gateway/internal/catalog"
	"github.com/s3gateway/internal/s3client"
)

type RootNode struct {
	DirNode
}

func NewRoot(s3c *s3client.Client, cm *cache.Manager, cat *catalog.Catalog, wb *catalog.WriteBackQueue, bucket string, ttl time.Duration, soleWriter bool) *RootNode {
	root := &RootNode{}
	root.bctx = &bucketCtx{
		s3:         s3c,
		cache:      cm,
		catalog:    cat,
		writeBack:  wb,
		bucket:     bucket,
		ttl:        ttl,
		soleWriter: soleWriter,
	}
	root.prefix = ""
	return root
}

var _ = (fs.InodeEmbedder)((*RootNode)(nil))
var _ = (fs.NodeStatfser)((*RootNode)(nil))

func (r *RootNode) Statfs(ctx context.Context, out *gofuse.StatfsOut) syscall.Errno {
	out.Blocks = 1 << 30
	out.Bfree = 1 << 30
	out.Bavail = 1 << 30
	out.Bsize = 4096
	out.NameLen = 255
	out.Frsize = 4096
	out.Files = 1 << 20
	out.Ffree = 1 << 20
	return 0
}
