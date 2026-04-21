package fuse

import (
	"time"

	"golang.org/x/sync/singleflight"

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

	// s3SF deduplicates concurrent S3 fallback calls within the gateway so a
	// burst of readers/lookupers collapses into a single origin round-trip.
	s3SF singleflight.Group
}
