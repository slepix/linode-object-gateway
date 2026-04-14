# s3gw - S3 File Gateway for Linode/Akamai Object Storage

A self-hosted FUSE filesystem daemon that mounts S3-compatible object storage buckets (Linode/Akamai) as local directories. Designed for use with NFS and Samba to provide network file access to cloud object storage with configurable local caching.

## Overview

`s3gw` acts as a bridge between traditional file-based access patterns (NFS/SMB) and S3-compatible object storage. It mounts each configured bucket as a directory on the host VM, which can then be exported via standard Linux NFS or Samba services.

```
NFS/SMB Clients
      |
  OS NFS/Samba Server
      |
  FUSE Mount Point (/mnt/s3/<bucket>)
      |
  s3gw daemon
      |
  ┌─────────────────┐
  │  Local Cache     │  LRU eviction, configurable size/TTL
  └─────────────────┘
      |
  Linode Object Storage (S3-compatible API)
```

## Data Consistency Guarantees

Data consistency is the primary design goal:

- **Write-through**: All writes go directly to object storage first. The write only succeeds (and the file is only cached) after S3 confirms the upload. If the S3 write fails, the application receives an I/O error on `close()`.
- **Read validation**: Reads check the local cache first. Cache validity depends on the configured mode:
  - **Sole-writer mode** (`sole_writer: true`): Trusts the cache within the TTL window. No remote validation. Use when this gateway is the only entity writing to the bucket.
  - **Multi-writer mode** (`sole_writer: false`): When the cache entry has expired (past TTL), validates the ETag against S3 before serving cached data. If the ETag differs, re-fetches from S3.
- **No silent data loss**: A failed S3 upload always propagates as an error to the calling application. Data is never reported as "written" if it only exists locally.

## Requirements

- Linux with FUSE support (kernel 2.6.14+ with `fuse` or `fuse3` package)
- Go 1.22+ (for building from source)
- Linode/Akamai Object Storage credentials (access key + secret key per bucket)
- Sufficient local disk space for the cache

## Quick Start

### 1. Build

```bash
make build
```

The binary is output to `bin/s3gw`.

### 2. Configure

Copy the example configuration:

```bash
sudo mkdir -p /etc/s3gw
sudo cp configs/s3gw.example.yaml /etc/s3gw/config.yaml
```

Edit `/etc/s3gw/config.yaml` with your bucket details:

```yaml
cache_dir: /var/cache/s3gw
max_cache_size: 10737418240  # 10 GB
default_ttl: 5m
log_level: info

buckets:
  - name: my-bucket
    region: us-east-1
    endpoint: https://us-east-1.linodeobjects.com
    access_key: YOUR_ACCESS_KEY
    secret_key: YOUR_SECRET_KEY
    mount_point: /mnt/s3/my-bucket
    ttl: 10m
    sole_writer: true
```

### 3. Run

```bash
# Direct
sudo ./bin/s3gw --config /etc/s3gw/config.yaml

# Or install as a systemd service
sudo make install
sudo systemctl enable --now s3gw
```

### 4. Export via NFS or Samba

Once mounted, the directories at your configured mount points (e.g., `/mnt/s3/my-bucket`) behave like normal local directories. Export them using standard tooling:

**NFS example** (`/etc/exports`):
```
/mnt/s3/my-bucket  192.168.1.0/24(rw,sync,no_subtree_check)
```

**Samba example** (`/etc/samba/smb.conf`):
```ini
[my-bucket]
   path = /mnt/s3/my-bucket
   browsable = yes
   writable = yes
   valid users = @smbusers
```

## Configuration Reference

### Global Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cache_dir` | string | `/var/cache/s3gw` | Directory for cached files and the metadata database |
| `max_cache_size` | int64 | `10737418240` (10 GB) | Maximum total cache size in bytes |
| `default_ttl` | duration | `5m` | Default cache TTL for all buckets (Go duration format: `30s`, `5m`, `1h`) |
| `log_level` | string | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `buckets` | list | required | List of bucket configurations |

### Per-Bucket Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | required | S3 bucket name |
| `region` | string | required | S3 region (e.g., `us-east-1`) |
| `endpoint` | string | required | S3-compatible endpoint URL |
| `access_key` | string | required | S3 access key |
| `secret_key` | string | required | S3 secret key |
| `mount_point` | string | required | Absolute path where this bucket will be mounted |
| `ttl` | duration | uses `default_ttl` | Override cache TTL for this bucket |
| `sole_writer` | bool | `false` | If `true`, skip ETag revalidation on reads (faster, assumes no external writers) |

### Duration Format

TTL values use Go's duration format:
- `30s` - 30 seconds
- `5m` - 5 minutes
- `1h` - 1 hour
- `2h30m` - 2 hours and 30 minutes

## Architecture

### Filesystem Operations

| Operation | Behavior |
|-----------|----------|
| **Read** | Check cache (TTL + optional ETag validation) -> serve from cache or fetch from S3 |
| **Write** | Buffer to temp file -> upload to S3 on `close()` -> cache on success, error on failure |
| **Create** | Create empty file node -> writes buffered -> uploaded on `close()` |
| **Delete** | Delete from S3 -> invalidate cache |
| **Mkdir** | Create zero-byte marker object (`prefix/`) in S3 |
| **Rmdir** | Verify directory empty -> delete marker object |
| **Rename** | S3 CopyObject + DeleteObject (S3 has no native rename) -> invalidate both cache entries |
| **List** | Paginated S3 ListObjectsV2 with delimiter |

### Cache Layer

- **Storage**: Files are stored on disk under `<cache_dir>/data/<bucket>/<hash_prefix>/<sha256_of_key>`
- **Metadata**: BoltDB database at `<cache_dir>/cache.db` tracks ETag, size, timestamps, and LRU ordering
- **Eviction**: Background goroutine checks every 30 seconds. Triggers at 90% capacity, evicts LRU entries down to 80% (hysteresis prevents thrashing)
- **Atomic writes**: Cache files are written to a temp file and atomically renamed to prevent partial reads

### Multi-Bucket Support

Each bucket gets:
- Its own FUSE mount (separate kernel `/dev/fuse` fd)
- Its own S3 client with independent credentials and endpoint
- Shared cache pool (single LRU budget across all buckets, adapts to access patterns automatically)

### Write-Through Flow

```
Application writes data
    |
    v
FUSE Handle.Write() -> appends to local temp file
    |
Application calls close()
    |
    v
FUSE Handle.Flush() -> PutObject to S3
    |
    +--> S3 FAILS  -> return EIO to application (data NOT cached, temp file cleaned up)
    |
    +--> S3 OK     -> copy temp file into cache store
                   -> update BoltDB metadata (ETag, size, mtime)
                   -> return success to application
```

### Read Flow

```
Application reads data
    |
    v
FUSE Handle.Read()
    |
    v
Cache lookup (BoltDB metadata + disk file exists?)
    |
    +--> HIT + within TTL           -> serve from disk cache
    |
    +--> HIT + expired TTL
    |       |
    |       +--> sole_writer=true   -> re-fetch from S3
    |       |
    |       +--> sole_writer=false  -> HeadObject for ETag
    |               |
    |               +--> ETag match -> refresh TTL, serve from cache
    |               |
    |               +--> ETag diff  -> re-fetch from S3
    |
    +--> MISS                       -> fetch from S3, cache, return
```

## Deployment

### Prerequisites

```bash
# Install FUSE
sudo apt install fuse3

# Create cache directory
sudo mkdir -p /var/cache/s3gw

# Create mount point parent
sudo mkdir -p /mnt/s3
```

### Install from Source

```bash
make build
sudo make install
```

This installs:
- Binary to `/usr/local/bin/s3gw`
- Example config to `/etc/s3gw/config.yaml.example`
- systemd unit to `/etc/systemd/system/s3gw.service`

### systemd Service

```bash
# Enable and start
sudo systemctl enable --now s3gw

# Check status
sudo systemctl status s3gw

# View logs
sudo journalctl -u s3gw -f
```

### Uninstall

```bash
sudo systemctl stop s3gw
sudo systemctl disable s3gw
sudo make uninstall
```

## Operations

### Monitoring

Logs are structured JSON written to stderr (captured by systemd journal):

```bash
# Follow live logs
sudo journalctl -u s3gw -f

# Filter by level
sudo journalctl -u s3gw -f | jq 'select(.level == "ERROR")'
```

Key log events:
- `s3gw starting` - daemon startup with config summary
- `mounted bucket` - successful FUSE mount per bucket
- `cache eviction started/completed` - eviction activity with stats
- `S3 PUT FAILED` - write failure (critical, data not persisted)
- `gateway shutting down` - graceful shutdown initiated

### Cache Management

The cache is self-managing via LRU eviction. To manually clear:

```bash
# Stop the service
sudo systemctl stop s3gw

# Clear cache data (metadata DB is rebuilt automatically)
sudo rm -rf /var/cache/s3gw/data
sudo rm -f /var/cache/s3gw/cache.db

# Restart
sudo systemctl start s3gw
```

### Sizing the Cache

Guidelines for `max_cache_size`:
- Set to the amount of disk space you can dedicate to caching
- Leave at least 10-20% of total disk free for temp files and OS operations
- The working set (frequently accessed files) should ideally fit within the cache
- Monitor eviction frequency in logs -- frequent eviction means the cache is too small

### Graceful Shutdown

On SIGINT or SIGTERM:
1. Cache evictor stops (no evictions during shutdown)
2. All FUSE mounts are unmounted in reverse order
3. Open file handles are flushed (dirty files are uploaded to S3)
4. BoltDB is closed cleanly
5. Process exits

If a file is being written during shutdown, the flush will attempt the S3 upload. If it fails, the application holding the file descriptor will receive an error.

## Linode Object Storage Setup

### Create Bucket

1. Log into the Linode Cloud Manager
2. Navigate to Object Storage
3. Create a bucket, note the bucket name and region

### Generate Access Keys

1. In Object Storage, go to Access Keys
2. Create an access key with read/write permissions for your bucket(s)
3. Save the access key and secret key

### Find Your Endpoint

Linode endpoints follow the pattern:
```
https://<region>.linodeobjects.com
```

Common regions:
| Region | Endpoint |
|--------|----------|
| US East (Newark) | `https://us-east-1.linodeobjects.com` |
| EU Central (Frankfurt) | `https://eu-central-1.linodeobjects.com` |
| AP South (Singapore) | `https://ap-south-1.linodeobjects.com` |
| US Southeast (Atlanta) | `https://us-southeast-1.linodeobjects.com` |

## Troubleshooting

### Mount fails with "Permission denied"

Ensure the user running `s3gw` has permission to use FUSE:
```bash
# Add user to fuse group
sudo usermod -aG fuse <username>

# Or run as root (recommended for systemd service)
```

### Mount fails with "Transport endpoint is not connected"

A previous mount was not cleanly unmounted:
```bash
sudo fusermount -uz /mnt/s3/<bucket>
```

### Writes fail with I/O error

Check S3 connectivity and credentials:
```bash
# Test with AWS CLI (using Linode endpoint)
aws s3 ls --endpoint-url https://us-east-1.linodeobjects.com s3://your-bucket/
```

Check logs for specific S3 errors:
```bash
sudo journalctl -u s3gw -f | jq 'select(.msg | contains("S3 PUT FAILED"))'
```

### High latency on reads

- Increase the TTL to reduce revalidation frequency
- Enable `sole_writer: true` if no external writers exist
- Increase `max_cache_size` to reduce cache misses
- Check network latency to the Linode endpoint

### Cache not being used

Verify the cache directory exists and is writable:
```bash
ls -la /var/cache/s3gw/
```

Check BoltDB for entries:
```bash
sudo journalctl -u s3gw | grep "cache"
```

## Project Structure

```
s3gw/
├── cmd/s3gw/main.go              # CLI entry point, signal handling
├── internal/
│   ├── config/
│   │   ├── config.go              # YAML config structs and loading
│   │   └── validate.go            # Config validation
│   ├── gateway/
│   │   ├── gateway.go             # Multi-bucket mount orchestrator
│   │   └── bucket.go              # Per-bucket FUSE mount lifecycle
│   ├── fuse/
│   │   ├── root.go                # FUSE root node (bucket root)
│   │   ├── dir.go                 # Directory operations (lookup, readdir, mkdir, etc.)
│   │   ├── file.go                # File node (getattr, open)
│   │   ├── handle.go              # File handle (read, write, flush) - write-through logic
│   │   ├── dirstream.go           # Paginated S3 directory listing
│   │   └── common.go              # Shared bucket context
│   ├── s3client/
│   │   ├── client.go              # S3 SDK client wrapper
│   │   ├── operations.go          # S3 operations (get, put, head, list, copy, delete)
│   │   └── errors.go              # S3 error to errno translation
│   ├── cache/
│   │   ├── manager.go             # Cache orchestrator (metadata + disk + eviction)
│   │   ├── metadata.go            # BoltDB metadata store
│   │   ├── store.go               # Disk file store with hash-based layout
│   │   └── evictor.go             # LRU eviction goroutine
│   └── logging/
│       └── logging.go             # Structured JSON logging setup
├── configs/
│   └── s3gw.example.yaml          # Example configuration
├── init/
│   └── s3gw.service               # systemd unit file
├── Makefile
├── go.mod
└── go.sum
```

## License

MIT
