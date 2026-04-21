package catalog

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

type negEntry struct {
	bucket  string
	key     string
	expires time.Time
	elem    *list.Element
}

type NegCache struct {
	mu      sync.Mutex
	buckets map[string]map[string]*negEntry
	lru     *list.List // front = oldest, back = newest
	ttl     time.Duration
	maxSize int
	stopCh  chan struct{}
}

const defaultNegCacheMaxSize = 10000

func NewNegCache(ttl time.Duration) *NegCache {
	nc := &NegCache{
		buckets: make(map[string]map[string]*negEntry),
		lru:     list.New(),
		ttl:     ttl,
		maxSize: defaultNegCacheMaxSize,
		stopCh:  make(chan struct{}),
	}
	go nc.reaper()
	return nc
}

func (nc *NegCache) Contains(bucket, key string) bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	b, ok := nc.buckets[bucket]
	if !ok {
		return false
	}
	e, ok := b[key]
	if !ok {
		return false
	}
	if !time.Now().Before(e.expires) {
		nc.removeLocked(e)
		return false
	}
	return true
}

func (nc *NegCache) Add(bucket, key string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	b, ok := nc.buckets[bucket]
	if !ok {
		b = make(map[string]*negEntry)
		nc.buckets[bucket] = b
	}
	if e, ok := b[key]; ok {
		e.expires = time.Now().Add(nc.ttl)
		nc.lru.MoveToBack(e.elem)
		return
	}
	e := &negEntry{
		bucket:  bucket,
		key:     key,
		expires: time.Now().Add(nc.ttl),
	}
	e.elem = nc.lru.PushBack(e)
	b[key] = e

	for nc.lru.Len() > nc.maxSize {
		front := nc.lru.Front()
		if front == nil {
			break
		}
		nc.removeLocked(front.Value.(*negEntry))
	}
}

func (nc *NegCache) Remove(bucket, key string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if b, ok := nc.buckets[bucket]; ok {
		if e, ok := b[key]; ok {
			nc.removeLocked(e)
		}
	}
}

func (nc *NegCache) RemovePrefix(bucket, prefix string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	b, ok := nc.buckets[bucket]
	if !ok {
		return
	}
	for k, e := range b {
		if strings.HasPrefix(k, prefix) {
			nc.removeLocked(e)
		}
	}
}

func (nc *NegCache) Stop() {
	close(nc.stopCh)
}

// removeLocked removes an entry from both the bucket map and the LRU list.
// Caller must hold nc.mu.
func (nc *NegCache) removeLocked(e *negEntry) {
	nc.lru.Remove(e.elem)
	if b, ok := nc.buckets[e.bucket]; ok {
		delete(b, e.key)
		if len(b) == 0 {
			delete(nc.buckets, e.bucket)
		}
	}
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
			// Walk from the front (oldest) until we hit a still-valid entry.
			for {
				front := nc.lru.Front()
				if front == nil {
					break
				}
				e := front.Value.(*negEntry)
				if now.Before(e.expires) {
					break
				}
				nc.removeLocked(e)
			}
			nc.mu.Unlock()
		}
	}
}
