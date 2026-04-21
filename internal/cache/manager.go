package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/s3gateway/internal/s3client"
)

type Manager struct {
	meta    *MetadataStore
	store   *DiskStore
	evictor *Evictor
	maxSize int64
	mu      sync.RWMutex
}

func NewManager(cacheDir string, maxSize int64) (*Manager, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	dbPath := filepath.Join(cacheDir, "cache.db")
	meta, err := NewMetadataStore(dbPath)
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(cacheDir, "data")
	store, err := NewDiskStore(dataDir)
	if err != nil {
		meta.Close()
		return nil, err
	}

	evictor := NewEvictor(meta, store, maxSize)

	m := &Manager{
		meta:    meta,
		store:   store,
		evictor: evictor,
		maxSize: maxSize,
	}

	evictor.Start()

	return m, nil
}

func (m *Manager) Close() error {
	m.evictor.Stop()
	return m.meta.Close()
}

func (m *Manager) Get(bucket, key string, ttl time.Duration, soleWriter bool) (string, *CacheEntry, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, err := m.meta.Get(bucket, key)
	if err != nil {
		return "", nil, false, err
	}
	if entry == nil {
		return "", nil, false, nil
	}

	if !m.store.Exists(bucket, key) {
		m.meta.Delete(bucket, key)
		return "", nil, false, nil
	}

	if soleWriter {
		m.meta.Touch(bucket, key)
		return entry.LocalPath, entry, true, nil
	}

	if time.Since(entry.CachedAt) > ttl {
		return entry.LocalPath, entry, false, nil
	}

	m.meta.Touch(bucket, key)
	return entry.LocalPath, entry, true, nil
}

func (m *Manager) Put(bucket, key string, srcFile *os.File, meta s3client.ObjectMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.store.Write(bucket, key, srcFile); err != nil {
		return fmt.Errorf("write to cache store: %w", err)
	}

	now := time.Now()
	entry := &CacheEntry{
		Bucket:       bucket,
		Key:          key,
		LocalPath:    m.store.Path(bucket, key),
		ETag:         meta.ETag,
		Size:         meta.Size,
		LastModified: meta.LastModified,
		LastAccessed: now,
		CachedAt:     now,
	}

	if err := m.meta.Put(entry); err != nil {
		return fmt.Errorf("write cache metadata: %w", err)
	}

	total, _ := m.meta.TotalSize()
	if total > int64(float64(m.maxSize)*0.9) {
		m.evictor.TriggerEviction()
	}

	return nil
}

func (m *Manager) Invalidate(bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.store.Delete(bucket, key)
	return m.meta.Delete(bucket, key)
}

func (m *Manager) Touch(bucket, key string) error {
	return m.meta.Touch(bucket, key)
}

func (m *Manager) Store() *DiskStore {
	return m.store
}

func (m *Manager) StopEvictor() {
	m.evictor.Stop()
}
