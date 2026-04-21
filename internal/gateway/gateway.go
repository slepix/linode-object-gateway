package gateway

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/s3gateway/internal/cache"
	"github.com/s3gateway/internal/catalog"
	"github.com/s3gateway/internal/config"
	"github.com/s3gateway/internal/s3client"
)

type Gateway struct {
	cfg       *config.Config
	cache     *cache.Manager
	catalog   *catalog.Catalog
	writeBack *catalog.WriteBackQueue
	mounts    []*BucketMount
	wg        sync.WaitGroup
}

func New(cfg *config.Config) *Gateway {
	return &Gateway{cfg: cfg}
}

func (g *Gateway) Start() error {
	cm, err := cache.NewManager(g.cfg.CacheDir, g.cfg.MaxCacheSize)
	if err != nil {
		return fmt.Errorf("init cache: %w", err)
	}
	g.cache = cm

	catCfg := catalog.Config{
		DBPath:          catalog.DefaultDBPath(g.cfg.CacheDir),
		SyncInterval:    g.cfg.Catalog.SyncInterval.Duration,
		NegCacheTTL:     g.cfg.Catalog.NegCacheTTL.Duration,
		DirCacheTTL:     g.cfg.Catalog.DirCacheTTL.Duration,
		SyncConcurrency: g.cfg.Catalog.SyncConcurrency,
	}
	cat, err := catalog.New(catCfg)
	if err != nil {
		g.cache.Close()
		return fmt.Errorf("init catalog: %w", err)
	}
	g.catalog = cat

	// Create all mounts first so we can build the S3 client lookup map
	s3ClientsByBucket := make(map[string]*s3client.Client)
	for _, bcfg := range g.cfg.Buckets {
		bm := NewBucketMount(bcfg, g.cache, g.catalog, nil, g.cfg.DefaultTTL.Duration)
		s3ClientsByBucket[bcfg.Name] = bm.s3
		g.catalog.RegisterBucket(bcfg.Name, bm.s3)
		g.mounts = append(g.mounts, bm)
	}

	// Create and start write-back queue if enabled
	if g.cfg.WriteBack.Enabled {
		g.writeBack = catalog.NewWriteBackQueue(
			g.cfg.WriteBack.Workers,
			g.cfg.WriteBack.QueueSize,
			g.cfg.WriteBack.MaxRetries,
			g.cfg.WriteBack.RetryDelay.Duration,
			func(bucket string) *s3client.Client {
				return s3ClientsByBucket[bucket]
			},
		)
		g.writeBack.Start()

		for _, bm := range g.mounts {
			bm.writeBack = g.writeBack
		}
	}

	// Now start (mount) each bucket
	for i, bm := range g.mounts {
		if err := bm.Start(); err != nil {
			for j := i - 1; j >= 0; j-- {
				g.mounts[j].Stop()
			}
			// Wait for previously-started wait goroutines to exit
			// before returning, so we don't leak them.
			g.wg.Wait()
			if g.writeBack != nil {
				g.writeBack.Stop()
			}
			g.catalog.Stop()
			g.cache.Close()
			return fmt.Errorf("start bucket %s: %w", g.cfg.Buckets[i].Name, err)
		}

		g.wg.Add(1)
		go func(m *BucketMount) {
			defer g.wg.Done()
			m.Wait()
		}(bm)
	}

	g.catalog.Start()

	slog.Info("gateway started",
		"buckets", len(g.mounts),
		"write_back", g.cfg.WriteBack.Enabled,
		"sync_interval", g.cfg.Catalog.SyncInterval.Duration,
	)
	return nil
}

func (g *Gateway) Stop() {
	slog.Info("gateway shutting down")

	g.catalog.Stop()

	if g.writeBack != nil {
		g.writeBack.Stop()
	}

	g.cache.StopEvictor()

	for i := len(g.mounts) - 1; i >= 0; i-- {
		g.mounts[i].Stop()
	}

	g.cache.Close()
	slog.Info("gateway stopped")
}

func (g *Gateway) Wait() {
	g.wg.Wait()
}
