package cache

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var entriesBucket = []byte("entries")

type CacheEntry struct {
	Bucket       string    `json:"bucket"`
	Key          string    `json:"key"`
	LocalPath    string    `json:"local_path"`
	ETag         string    `json:"etag"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	LastAccessed time.Time `json:"last_accessed"`
	CachedAt     time.Time `json:"cached_at"`
}

func entryKey(bucket, key string) []byte {
	return []byte(fmt.Sprintf("%s/%s", bucket, key))
}

type MetadataStore struct {
	db *bolt.DB
}

func NewMetadataStore(dbPath string) (*MetadataStore, error) {
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open cache db: %w", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(entriesBucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init cache db: %w", err)
	}

	return &MetadataStore{db: db}, nil
}

func (ms *MetadataStore) Close() error {
	return ms.db.Close()
}

func (ms *MetadataStore) Get(bucket, key string) (*CacheEntry, error) {
	var entry *CacheEntry
	err := ms.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(entriesBucket)
		data := b.Get(entryKey(bucket, key))
		if data == nil {
			return nil
		}
		entry = &CacheEntry{}
		return json.Unmarshal(data, entry)
	})
	return entry, err
}

func (ms *MetadataStore) Put(entry *CacheEntry) error {
	return ms.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(entriesBucket)
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		return b.Put(entryKey(entry.Bucket, entry.Key), data)
	})
}

func (ms *MetadataStore) Delete(bucket, key string) error {
	return ms.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(entriesBucket)
		return b.Delete(entryKey(bucket, key))
	})
}

func (ms *MetadataStore) Touch(bucket, key string) error {
	return ms.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(entriesBucket)
		data := b.Get(entryKey(bucket, key))
		if data == nil {
			return nil
		}
		var entry CacheEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return err
		}
		entry.LastAccessed = time.Now()
		updated, err := json.Marshal(&entry)
		if err != nil {
			return err
		}
		return b.Put(entryKey(bucket, key), updated)
	})
}

func (ms *MetadataStore) TotalSize() (int64, error) {
	var total int64
	err := ms.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(entriesBucket)
		return b.ForEach(func(k, v []byte) error {
			var entry CacheEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return nil
			}
			total += entry.Size
			return nil
		})
	})
	return total, err
}

func (ms *MetadataStore) ListByLastAccessed(limit int) ([]*CacheEntry, error) {
	var entries []*CacheEntry
	err := ms.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(entriesBucket)
		return b.ForEach(func(k, v []byte) error {
			var entry CacheEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return nil
			}
			entries = append(entries, &entry)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastAccessed.Before(entries[j].LastAccessed)
	})

	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	return entries, nil
}
