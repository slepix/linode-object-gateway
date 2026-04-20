package catalog

import (
	"context"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/s3gateway/internal/s3client"
)

type SyncManager struct {
	store       *Store
	dirCache    *DirCache
	negCache    *NegCache
	interval    time.Duration
	concurrency int
	stopCh      chan struct{}
	wg          sync.WaitGroup
	buckets     map[string]*s3client.Client
	mu          sync.RWMutex
}

func NewSyncManager(store *Store, dirCache *DirCache, negCache *NegCache, interval time.Duration, concurrency int) *SyncManager {
	return &SyncManager{
		store:       store,
		dirCache:    dirCache,
		negCache:    negCache,
		interval:    interval,
		concurrency: concurrency,
		stopCh:      make(chan struct{}),
		buckets:     make(map[string]*s3client.Client),
	}
}

func (sm *SyncManager) RegisterBucket(name string, client *s3client.Client) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.buckets[name] = client
}

func (sm *SyncManager) Start() {
	sm.wg.Add(1)
	go sm.loop()
	slog.Info("sync manager started", "interval", sm.interval, "concurrency", sm.concurrency)
}

func (sm *SyncManager) Stop() {
	close(sm.stopCh)
	sm.wg.Wait()
	slog.Info("sync manager stopped")
}

func (sm *SyncManager) loop() {
	defer sm.wg.Done()

	sm.syncAll()

	ticker := time.NewTicker(sm.interval)
	defer ticker.Stop()

	for {
		select {
		case <-sm.stopCh:
			return
		case <-ticker.C:
			sm.syncAll()
		}
	}
}

func (sm *SyncManager) syncAll() {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sem := make(chan struct{}, sm.concurrency)

	var wg sync.WaitGroup
	for name, client := range sm.buckets {
		wg.Add(1)
		sem <- struct{}{}
		go func(bucket string, s3c *s3client.Client) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := sm.syncBucket(bucket, s3c); err != nil {
				slog.Error("sync bucket failed", "bucket", bucket, "error", err)
			}
		}(name, client)
	}
	wg.Wait()
}

func (sm *SyncManager) syncBucket(bucket string, s3c *s3client.Client) error {
	start := time.Now()

	var token *string
	var totalSynced int

	for {
		select {
		case <-sm.stopCh:
			return nil
		default:
		}

		result, err := s3c.ListObjects(context.Background(), "", "", token)
		if err != nil {
			return err
		}

		entries := make([]Entry, 0, len(result.Objects)+len(result.CommonPrefixes))

		for _, cp := range result.CommonPrefixes {
			parent, name := splitKey(cp)
			entries = append(entries, Entry{
				Key:    cp,
				Parent: parent,
				Name:   name,
				IsDir:  true,
			})
		}

		for _, obj := range result.Objects {
			if strings.HasSuffix(obj.Key, "/") {
				parent, name := splitKey(obj.Key)
				entries = append(entries, Entry{
					Key:      obj.Key,
					Parent:   parent,
					Name:     name,
					IsDir:    true,
					Modified: obj.LastModified,
				})
				continue
			}
			parent, name := splitKey(obj.Key)
			entries = append(entries, Entry{
				Key:      obj.Key,
				Parent:   parent,
				Name:     name,
				Size:     obj.Size,
				ETag:     obj.ETag,
				Modified: obj.LastModified,
			})
		}

		if len(entries) > 0 {
			if err := sm.store.PutBatch(bucket, entries); err != nil {
				return err
			}
			totalSynced += len(entries)
		}

		if result.ContinuationToken == nil {
			break
		}
		token = result.ContinuationToken
	}

	if err := sm.store.SetSyncCursor(bucket, "", start); err != nil {
		slog.Warn("set sync cursor failed", "bucket", bucket, "error", err)
	}

	sm.dirCache.InvalidateAll(bucket)

	slog.Info("sync complete", "bucket", bucket, "objects", totalSynced, "duration", time.Since(start))
	return nil
}

func (sm *SyncManager) SyncPrefix(bucket string, prefix string) error {
	sm.mu.RLock()
	s3c, ok := sm.buckets[bucket]
	sm.mu.RUnlock()
	if !ok {
		return nil
	}

	var token *string
	for {
		result, err := s3c.ListObjects(context.Background(), prefix, "/", token)
		if err != nil {
			return err
		}

		entries := make([]Entry, 0, len(result.Objects)+len(result.CommonPrefixes))

		for _, cp := range result.CommonPrefixes {
			parent, name := splitKey(cp)
			entries = append(entries, Entry{
				Key:    cp,
				Parent: parent,
				Name:   name,
				IsDir:  true,
			})
		}

		for _, obj := range result.Objects {
			if strings.HasSuffix(obj.Key, "/") {
				continue
			}
			parent, name := splitKey(obj.Key)
			entries = append(entries, Entry{
				Key:      obj.Key,
				Parent:   parent,
				Name:     name,
				Size:     obj.Size,
				ETag:     obj.ETag,
				Modified: obj.LastModified,
			})
		}

		if len(entries) > 0 {
			if err := sm.store.PutBatch(bucket, entries); err != nil {
				return err
			}
		}

		if result.ContinuationToken == nil {
			break
		}
		token = result.ContinuationToken
	}

	sm.dirCache.Invalidate(bucket, prefix)
	sm.negCache.RemovePrefix(bucket, prefix)

	return nil
}

func splitKey(key string) (parent, name string) {
	return SplitKeyExported(key)
}

func SplitKeyExported(key string) (parent, name string) {
	clean := strings.TrimSuffix(key, "/")
	parent = path.Dir(clean)
	if parent == "." {
		parent = ""
	} else {
		parent += "/"
	}
	name = path.Base(clean)
	return
}
