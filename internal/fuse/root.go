package fuse

import (
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/s3gateway/internal/cache"
	"github.com/s3gateway/internal/s3client"
)

type RootNode struct {
	DirNode
}

func NewRoot(s3c *s3client.Client, cm *cache.Manager, bucket string, ttl time.Duration, soleWriter bool) *RootNode {
	root := &RootNode{}
	root.bctx = &bucketCtx{
		s3:         s3c,
		cache:      cm,
		bucket:     bucket,
		ttl:        int64(ttl),
		soleWriter: soleWriter,
	}
	root.prefix = ""
	return root
}

var _ = (fs.InodeEmbedder)((*RootNode)(nil))
