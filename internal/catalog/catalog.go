package catalog

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/s3gateway/internal/s3client"
	"golang.org/x/sync/singleflight"
)

type Config struct {
	DBPath          string
	SyncInterval    time.Duration
	NegCacheTTL     time.Duration
	DirCacheTTL     time.Duration
	SyncConcurrency int
}

type Catalog struct {
	store    *Store
	negCache *NegCache
	dirCache *DirCache
	sync     *SyncManager
	sfGroup  singleflight.Group
}

func New(cfg Config) (*Catalog, error) {
	store, err := NewStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open catalog store: %w", err)
	}

	negCache := NewNegCache(cfg.NegCacheTTL)
	dirCache := NewDirCache(cfg.DirCacheTTL)
	syncMgr := NewSyncManager(store, dirCache, negCache, cfg.SyncInterval, cfg.SyncConcurrency)

	return &Catalog{
		store:    store,
		negCache: negCache,
		dirCache: dirCache,
		sync:     syncMgr,
	}, nil
}

func DefaultDBPath(cacheDir string) string {
	return filepath.Join(cacheDir, "catalog.db")
}

func (c *Catalog) RegisterBucket(name string, client *s3client.Client) {
	c.sync.RegisterBucket(name, client)
}

func (c *Catalog) Start() {
	c.sync.Start()
}

func (c *Catalog) Stop() {
	c.sync.Stop()
	c.negCache.Stop()
	c.dirCache.Stop()
	c.store.Close()
	slog.Info("catalog closed")
}

func (c *Catalog) ListDir(bucket, parent string) ([]Entry, error) {
	if cached, ok := c.dirCache.Get(bucket, parent); ok {
		return cached, nil
	}

	key := fmt.Sprintf("list:%s/%s", bucket, parent)
	val, err, _ := c.sfGroup.Do(key, func() (interface{}, error) {
		entries, err := c.store.ListDir(bucket, parent)
		if err != nil {
			return nil, err
		}
		c.dirCache.Set(bucket, parent, entries)
		return entries, nil
	})
	if err != nil {
		return nil, err
	}
	return val.([]Entry), nil
}

func (c *Catalog) Lookup(bucket, key string) (*Entry, error) {
	if c.negCache.Contains(bucket, key) {
		return nil, nil
	}

	sfKey := fmt.Sprintf("lookup:%s/%s", bucket, key)
	val, err, _ := c.sfGroup.Do(sfKey, func() (interface{}, error) {
		return c.store.Lookup(bucket, key)
	})
	if err != nil {
		return nil, err
	}
	entry, _ := val.(*Entry)
	if entry == nil {
		c.negCache.Add(bucket, key)
	}
	return entry, nil
}

func (c *Catalog) HasDir(bucket, parent string) (bool, error) {
	sfKey := fmt.Sprintf("hasdir:%s/%s", bucket, parent)
	val, err, _ := c.sfGroup.Do(sfKey, func() (interface{}, error) {
		return c.store.HasDir(bucket, parent)
	})
	if err != nil {
		return false, err
	}
	return val.(bool), nil
}

func (c *Catalog) Put(bucket string, e *Entry) error {
	if err := c.store.Put(bucket, e); err != nil {
		return err
	}
	c.negCache.Remove(bucket, e.Key)
	c.dirCache.Invalidate(bucket, e.Parent)
	return nil
}

func (c *Catalog) Delete(bucket, key string) error {
	entry, _ := c.store.Lookup(bucket, key)
	if err := c.store.Delete(bucket, key); err != nil {
		return err
	}
	c.negCache.Remove(bucket, key)
	if entry != nil {
		c.dirCache.Invalidate(bucket, entry.Parent)
	}
	return nil
}

func (c *Catalog) DeletePrefix(bucket, prefix string) error {
	if err := c.store.DeletePrefix(bucket, prefix); err != nil {
		return err
	}
	c.negCache.RemovePrefix(bucket, prefix)
	c.dirCache.InvalidateAll(bucket)
	return nil
}

func (c *Catalog) SyncPrefix(bucket, prefix string) error {
	return c.sync.SyncPrefix(bucket, prefix)
}

func (c *Catalog) Count(bucket string) (int64, error) {
	return c.store.Count(bucket)
}

func (c *Catalog) Store() *Store {
	return c.store
}
