package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/s3gateway/internal/config"
	"github.com/s3gateway/internal/gateway"
	"github.com/s3gateway/internal/logging"
)

func main() {
	configPath := flag.String("config", "/etc/s3gw/config.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	logging.Init(cfg.LogLevel)

	slog.Info("s3gw starting",
		"cache_dir", cfg.CacheDir,
		"max_cache_size", cfg.MaxCacheSize,
		"default_ttl", cfg.DefaultTTL.Duration,
		"buckets", len(cfg.Buckets),
	)

	gw := gateway.New(cfg)

	if err := gw.Start(); err != nil {
		slog.Error("failed to start gateway", "error", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig)

	gw.Stop()

	slog.Info("s3gw exited cleanly")
}
