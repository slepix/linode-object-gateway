package fuse

import (
	"time"

	"github.com/s3gateway/internal/cache"
	"github.com/s3gateway/internal/catalog"
	"github.com/s3gateway/internal/s3client"
)

type bucketCtx struct {
	s3         *s3client.Client
	cache      *cache.Manager
	catalog    *catalog.Catalog
	writeBack  *catalog.WriteBackQueue
	bucket     string
	ttl        time.Duration
	soleWriter bool
}
