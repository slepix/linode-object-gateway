package cache

import (
	"log/slog"
	"sync"
	"time"
)

type Evictor struct {
	meta     *MetadataStore
	store    *DiskStore
	maxSize  int64
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewEvictor(meta *MetadataStore, store *DiskStore, maxSize int64) *Evictor {
	return &Evictor{
		meta:     meta,
		store:    store,
		maxSize:  maxSize,
		interval: 30 * time.Second,
		stopCh:   make(chan struct{}),
	}
}

func (e *Evictor) Start() {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(e.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := e.evictOnce(); err != nil {
					slog.Error("cache eviction error", "error", err)
				}
			case <-e.stopCh:
				return
			}
		}
	}()
}

func (e *Evictor) Stop() {
	close(e.stopCh)
	e.wg.Wait()
}

func (e *Evictor) evictOnce() error {
	total, err := e.meta.TotalSize()
	if err != nil {
		return err
	}

	highWater := int64(float64(e.maxSize) * 0.9)
	targetMark := int64(float64(e.maxSize) * 0.8)

	if total <= highWater {
		return nil
	}

	slog.Info("cache eviction started",
		"total_size", total,
		"max_size", e.maxSize,
		"target", targetMark,
	)

	entries, err := e.meta.ListByLastAccessed(100)
	if err != nil {
		return err
	}

	evicted := 0
	for _, entry := range entries {
		if total <= targetMark {
			break
		}

		if err := e.store.Delete(entry.Bucket, entry.Key); err != nil {
			slog.Warn("failed to delete cached file",
				"bucket", entry.Bucket,
				"key", entry.Key,
				"error", err,
			)
			continue
		}

		if err := e.meta.Delete(entry.Bucket, entry.Key); err != nil {
			slog.Warn("failed to delete cache metadata",
				"bucket", entry.Bucket,
				"key", entry.Key,
				"error", err,
			)
			continue
		}

		total -= entry.Size
		evicted++
	}

	slog.Info("cache eviction completed",
		"evicted", evicted,
		"new_total", total,
	)

	return nil
}

func (e *Evictor) TriggerEviction() {
	go func() {
		if err := e.evictOnce(); err != nil {
			slog.Error("triggered eviction error", "error", err)
		}
	}()
}
