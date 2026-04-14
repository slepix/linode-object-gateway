package gateway

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/s3gateway/internal/cache"
	"github.com/s3gateway/internal/config"
	fuseimpl "github.com/s3gateway/internal/fuse"
	"github.com/s3gateway/internal/s3client"
)

type BucketMount struct {
	cfg        config.BucketConfig
	s3         *s3client.Client
	cache      *cache.Manager
	server     *gofuse.Server
	ttl        time.Duration
	mountPoint string
}

func NewBucketMount(bcfg config.BucketConfig, cm *cache.Manager, defaultTTL time.Duration) *BucketMount {
	ttl := bcfg.EffectiveTTL(defaultTTL)

	s3c := s3client.NewClient(
		bcfg.Name,
		bcfg.Region,
		bcfg.Endpoint,
		bcfg.AccessKey,
		bcfg.SecretKey,
	)

	return &BucketMount{
		cfg:        bcfg,
		s3:         s3c,
		cache:      cm,
		ttl:        ttl,
		mountPoint: bcfg.MountPoint,
	}
}

func (bm *BucketMount) Start() error {
	if err := os.MkdirAll(bm.mountPoint, 0755); err != nil {
		return fmt.Errorf("create mount point %s: %w", bm.mountPoint, err)
	}

	root := fuseimpl.NewRoot(bm.s3, bm.cache, bm.cfg.Name, bm.ttl, bm.cfg.SoleWriter)

	opts := &fs.Options{
		MountOptions: gofuse.MountOptions{
			Name:          "s3gw",
			FsName:        fmt.Sprintf("s3gw:%s", bm.cfg.Name),
			DisableXAttrs: true,
			MaxReadAhead:  128 * 1024,
			DirectMount:   true,
		},
		AttrTimeout:  &bm.ttl,
		EntryTimeout: &bm.ttl,
	}

	server, err := fs.Mount(bm.mountPoint, root, opts)
	if err != nil {
		return fmt.Errorf("mount %s for bucket %s: %w", bm.mountPoint, bm.cfg.Name, err)
	}
	bm.server = server

	slog.Info("mounted bucket",
		"bucket", bm.cfg.Name,
		"mount_point", bm.mountPoint,
		"sole_writer", bm.cfg.SoleWriter,
		"ttl", bm.ttl,
	)

	return nil
}

func (bm *BucketMount) Stop() {
	if bm.server == nil {
		return
	}

	slog.Info("unmounting", "bucket", bm.cfg.Name, "mount_point", bm.mountPoint)

	if err := bm.server.Unmount(); err != nil {
		slog.Error("unmount failed", "bucket", bm.cfg.Name, "error", err)
		return
	}

	bm.server.Wait()
	slog.Info("unmounted", "bucket", bm.cfg.Name)
}

func (bm *BucketMount) Wait() {
	if bm.server != nil {
		bm.server.Wait()
	}
}
