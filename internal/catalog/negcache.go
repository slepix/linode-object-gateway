package catalog

import (
	"strings"
	"sync"
	"time"
)

type negEntry struct {
	expires time.Time
}

type NegCache struct {
	mu      sync.RWMutex
	buckets map[string]map[string]negEntry
	ttl     time.Duration
	stopCh  chan struct{}
}

func NewNegCache(ttl time.Duration) *NegCache {
	nc := &NegCache{
		buckets: make(map[string]map[string]negEntry),
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}
	go nc.reaper()
	return nc
}

func (nc *NegCache) Contains(bucket, key string) bool {
	nc.mu.RLock()
	defer nc.mu.RUnlock()
	b, ok := nc.buckets[bucket]
	if !ok {
		return false
	}
	e, ok := b[key]
	if !ok {
		return false
	}
	return time.Now().Before(e.expires)
}

func (nc *NegCache) Add(bucket, key string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	b, ok := nc.buckets[bucket]
	if !ok {
		b = make(map[string]negEntry)
		nc.buckets[bucket] = b
	}
	b[key] = negEntry{expires: time.Now().Add(nc.ttl)}
}

func (nc *NegCache) Remove(bucket, key string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if b, ok := nc.buckets[bucket]; ok {
		delete(b, key)
	}
}

func (nc *NegCache) RemovePrefix(bucket, prefix string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	b, ok := nc.buckets[bucket]
	if !ok {
		return
	}
	for k := range b {
		if strings.HasPrefix(k, prefix) {
			delete(b, k)
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
			for bucket, b := range nc.buckets {
				for k, e := range b {
					if now.After(e.expires) {
						delete(b, k)
					}
				}
				if len(b) == 0 {
					delete(nc.buckets, bucket)
				}
			}
			nc.mu.Unlock()
		}
	}
}
