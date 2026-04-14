package gateway

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/s3gateway/internal/cache"
	"github.com/s3gateway/internal/config"
)

type Gateway struct {
	cfg    *config.Config
	cache  *cache.Manager
	mounts []*BucketMount
	wg     sync.WaitGroup
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

	for i, bcfg := range g.cfg.Buckets {
		bm := NewBucketMount(bcfg, g.cache, g.cfg.DefaultTTL.Duration)
		if err := bm.Start(); err != nil {
			for j := i - 1; j >= 0; j-- {
				g.mounts[j].Stop()
			}
			g.cache.Close()
			return fmt.Errorf("start bucket %s: %w", bcfg.Name, err)
		}
		g.mounts = append(g.mounts, bm)

		g.wg.Add(1)
		go func(m *BucketMount) {
			defer g.wg.Done()
			m.Wait()
		}(bm)
	}

	slog.Info("gateway started", "buckets", len(g.mounts))
	return nil
}

func (g *Gateway) Stop() {
	slog.Info("gateway shutting down")

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
