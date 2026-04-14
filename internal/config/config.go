package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	CacheDir     string         `yaml:"cache_dir"`
	MaxCacheSize int64          `yaml:"max_cache_size"`
	DefaultTTL   Duration       `yaml:"default_ttl"`
	LogLevel     string         `yaml:"log_level"`
	Buckets      []BucketConfig `yaml:"buckets"`
}

type BucketConfig struct {
	Name       string    `yaml:"name"`
	Region     string    `yaml:"region"`
	Endpoint   string    `yaml:"endpoint"`
	AccessKey  string    `yaml:"access_key"`
	SecretKey  string    `yaml:"secret_key"`
	MountPoint string    `yaml:"mount_point"`
	TTL        *Duration `yaml:"ttl,omitempty"`
	SoleWriter bool      `yaml:"sole_writer"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

func (bc *BucketConfig) EffectiveTTL(defaultTTL time.Duration) time.Duration {
	if bc.TTL != nil {
		return bc.TTL.Duration
	}
	return defaultTTL
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		CacheDir:     "/var/cache/s3gw",
		MaxCacheSize: 10 * 1024 * 1024 * 1024,
		DefaultTTL:   Duration{5 * time.Minute},
		LogLevel:     "info",
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
