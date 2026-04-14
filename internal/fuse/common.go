package fuse

import (
	"github.com/s3gateway/internal/cache"
	"github.com/s3gateway/internal/s3client"
)

type bucketCtx struct {
	s3         *s3client.Client
	cache      *cache.Manager
	bucket     string
	ttl        int64
	soleWriter bool
}
