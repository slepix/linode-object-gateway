package catalog

import (
	"sync"
	"time"
)

type dirCacheEntry struct {
	entries []Entry
	expires time.Time
}

type DirCache struct {
	mu      sync.RWMutex
	buckets map[string]map[string]dirCacheEntry
	ttl     time.Duration
	stopCh  chan struct{}
}

func NewDirCache(ttl time.Duration) *DirCache {
	dc := &DirCache{
		buckets: make(map[string]map[string]dirCacheEntry),
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}
	go dc.reaper()
	return dc
}

func (dc *DirCache) Get(bucket, parent string) ([]Entry, bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	b, ok := dc.buckets[bucket]
	if !ok {
		return nil, false
	}
	e, ok := b[parent]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	cp := make([]Entry, len(e.entries))
	copy(cp, e.entries)
	return cp, true
}

func (dc *DirCache) Set(bucket, parent string, entries []Entry) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	b, ok := dc.buckets[bucket]
	if !ok {
		b = make(map[string]dirCacheEntry)
		dc.buckets[bucket] = b
	}
	cp := make([]Entry, len(entries))
	copy(cp, entries)
	b[parent] = dirCacheEntry{
		entries: cp,
		expires: time.Now().Add(dc.ttl),
	}
}

func (dc *DirCache) Invalidate(bucket, parent string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if b, ok := dc.buckets[bucket]; ok {
		delete(b, parent)
	}
}

func (dc *DirCache) InvalidateAll(bucket string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	delete(dc.buckets, bucket)
}

func (dc *DirCache) Stop() {
	close(dc.stopCh)
}

func (dc *DirCache) reaper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-dc.stopCh:
			return
		case <-ticker.C:
			dc.mu.Lock()
			now := time.Now()
			for bucket, b := range dc.buckets {
				for k, e := range b {
					if now.After(e.expires) {
						delete(b, k)
					}
				}
				if len(b) == 0 {
					delete(dc.buckets, bucket)
				}
			}
			dc.mu.Unlock()
		}
	}
}
