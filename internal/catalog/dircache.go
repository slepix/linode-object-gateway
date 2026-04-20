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
	items   map[string]dirCacheEntry
	ttl     time.Duration
	stopCh  chan struct{}
}

func NewDirCache(ttl time.Duration) *DirCache {
	dc := &DirCache{
		items:  make(map[string]dirCacheEntry),
		ttl:    ttl,
		stopCh: make(chan struct{}),
	}
	go dc.reaper()
	return dc
}

func (dc *DirCache) Get(bucket, parent string) ([]Entry, bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	e, ok := dc.items[dirKey(bucket, parent)]
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
	cp := make([]Entry, len(entries))
	copy(cp, entries)
	dc.items[dirKey(bucket, parent)] = dirCacheEntry{
		entries: cp,
		expires: time.Now().Add(dc.ttl),
	}
}

func (dc *DirCache) Invalidate(bucket, parent string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	delete(dc.items, dirKey(bucket, parent))
}

func (dc *DirCache) InvalidateAll(bucket string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	pfx := bucket + "/"
	for k := range dc.items {
		if len(k) >= len(pfx) && k[:len(pfx)] == pfx {
			delete(dc.items, k)
		}
	}
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
			for k, e := range dc.items {
				if now.After(e.expires) {
					delete(dc.items, k)
				}
			}
			dc.mu.Unlock()
		}
	}
}

func dirKey(bucket, parent string) string {
	return bucket + "/" + parent
}
