package catalog

import (
	"sync"
	"time"
)

type negEntry struct {
	expires time.Time
}

type NegCache struct {
	mu      sync.RWMutex
	entries map[string]negEntry
	ttl     time.Duration
	stopCh  chan struct{}
}

func NewNegCache(ttl time.Duration) *NegCache {
	nc := &NegCache{
		entries: make(map[string]negEntry),
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}
	go nc.reaper()
	return nc
}

func (nc *NegCache) Contains(bucket, key string) bool {
	nc.mu.RLock()
	defer nc.mu.RUnlock()
	e, ok := nc.entries[negKey(bucket, key)]
	if !ok {
		return false
	}
	return time.Now().Before(e.expires)
}

func (nc *NegCache) Add(bucket, key string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	nc.entries[negKey(bucket, key)] = negEntry{expires: time.Now().Add(nc.ttl)}
}

func (nc *NegCache) Remove(bucket, key string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	delete(nc.entries, negKey(bucket, key))
}

func (nc *NegCache) RemovePrefix(bucket, prefix string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	pfx := bucket + "/" + prefix
	for k := range nc.entries {
		if len(k) >= len(pfx) && k[:len(pfx)] == pfx {
			delete(nc.entries, k)
		}
	}
}

func (nc *NegCache) Stop() {
	close(nc.stopCh)
}

func (nc *NegCache) reaper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-nc.stopCh:
			return
		case <-ticker.C:
			nc.mu.Lock()
			now := time.Now()
			for k, e := range nc.entries {
				if now.After(e.expires) {
					delete(nc.entries, k)
				}
			}
			nc.mu.Unlock()
		}
	}
}

func negKey(bucket, key string) string {
	return bucket + "/" + key
}
