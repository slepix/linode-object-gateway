package config

import (
	"fmt"
	"path/filepath"
)

func Validate(cfg *Config) error {
	if cfg.CacheDir == "" {
		return fmt.Errorf("cache_dir is required")
	}
	if !filepath.IsAbs(cfg.CacheDir) {
		return fmt.Errorf("cache_dir must be an absolute path")
	}
	if cfg.MaxCacheSize <= 0 {
		return fmt.Errorf("max_cache_size must be positive")
	}
	if len(cfg.Buckets) == 0 {
		return fmt.Errorf("at least one bucket must be configured")
	}

	mountPoints := make(map[string]bool)
	bucketNames := make(map[string]bool)

	for i, b := range cfg.Buckets {
		prefix := fmt.Sprintf("buckets[%d]", i)

		if b.Name == "" {
			return fmt.Errorf("%s: name is required", prefix)
		}
		if b.Endpoint == "" {
			return fmt.Errorf("%s: endpoint is required", prefix)
		}
		if b.AccessKey == "" {
			return fmt.Errorf("%s: access_key is required", prefix)
		}
		if b.SecretKey == "" {
			return fmt.Errorf("%s: secret_key is required", prefix)
		}
		if b.MountPoint == "" {
			return fmt.Errorf("%s: mount_point is required", prefix)
		}
		if !filepath.IsAbs(b.MountPoint) {
			return fmt.Errorf("%s: mount_point must be an absolute path", prefix)
		}
		if b.Region == "" {
			return fmt.Errorf("%s: region is required", prefix)
		}

		if mountPoints[b.MountPoint] {
			return fmt.Errorf("%s: duplicate mount_point %q", prefix, b.MountPoint)
		}
		mountPoints[b.MountPoint] = true

		if bucketNames[b.Name] {
			return fmt.Errorf("%s: duplicate bucket name %q", prefix, b.Name)
		}
		bucketNames[b.Name] = true
	}

	return nil
}
