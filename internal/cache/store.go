package cache

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type DiskStore struct {
	baseDir string
}

func NewDiskStore(baseDir string) (*DiskStore, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache store dir: %w", err)
	}
	return &DiskStore{baseDir: baseDir}, nil
}

func (ds *DiskStore) Path(bucket, key string) string {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	prefix := hash[:2]
	return filepath.Join(ds.baseDir, bucket, prefix, hash)
}

func (ds *DiskStore) Exists(bucket, key string) bool {
	_, err := os.Stat(ds.Path(bucket, key))
	return err == nil
}

func (ds *DiskStore) Write(bucket, key string, src *os.File) error {
	destPath := ds.Path(bucket, key)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek source: %w", err)
	}

	dest, err := os.CreateTemp(filepath.Dir(destPath), ".cache-*")
	if err != nil {
		return fmt.Errorf("create temp for cache: %w", err)
	}
	tmpName := dest.Name()

	if _, err := io.Copy(dest, src); err != nil {
		dest.Close()
		os.Remove(tmpName)
		return fmt.Errorf("copy to cache: %w", err)
	}
	if err := dest.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close cache file: %w", err)
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename cache file: %w", err)
	}

	return nil
}

func (ds *DiskStore) Read(bucket, key string, off int64, dest []byte) (int, error) {
	f, err := os.Open(ds.Path(bucket, key))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.ReadAt(dest, off)
}

func (ds *DiskStore) Delete(bucket, key string) error {
	return os.Remove(ds.Path(bucket, key))
}

func (ds *DiskStore) OpenForRead(bucket, key string) (*os.File, error) {
	return os.Open(ds.Path(bucket, key))
}
