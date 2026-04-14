# Architecture & Design

Technical deep-dive into s3gw internals for contributors and operators who need to understand the system's behavior in detail.

## Design Principles

1. **Consistency over performance**: Writes always hit S3 before being acknowledged. A slow but correct write is preferred over a fast but potentially lost one.
2. **Fail loud**: S3 failures propagate as I/O errors to the application. No silent fallbacks.
3. **Simplicity**: Single Go binary, no external databases or services. BoltDB is embedded. FUSE is the only kernel dependency beyond standard Linux.
4. **Shared nothing between buckets**: Each bucket has its own FUSE mount, S3 client, and credentials. The only shared resource is the cache pool.

## Component Architecture

```
┌──────────────────────────────────────────────────────┐
│                      main.go                          │
│              CLI parsing, signal handling              │
└─────────────────────────┬────────────────────────────┘
                          │
┌─────────────────────────▼────────────────────────────┐
│                    Gateway                            │
│            Multi-bucket orchestrator                  │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐ │
│  │ BucketMount  │ │ BucketMount  │ │ BucketMount  │ │
│  │  (bucket A)  │ │  (bucket B)  │ │  (bucket C)  │ │
│  └──────┬───────┘ └──────┬───────┘ └──────┬───────┘ │
└─────────┼────────────────┼────────────────┼──────────┘
          │                │                │
┌─────────▼────────────────▼────────────────▼──────────┐
│                   FUSE Layer                          │
│     RootNode -> DirNode -> FileNode -> Handle         │
│                                                       │
│  Per-mount: own FUSE server, own /dev/fuse fd         │
└─────────┬────────────────┬────────────────┬──────────┘
          │                │                │
┌─────────▼──┐    ┌───────▼───────┐  ┌─────▼──────────┐
│  S3 Client │    │ Cache Manager │  │  S3 Client     │
│ (bucket A) │    │   (shared)    │  │  (bucket C)    │
└────────────┘    └───────┬───────┘  └────────────────┘
                          │
              ┌───────────┼───────────┐
              │           │           │
        ┌─────▼───┐ ┌────▼────┐ ┌────▼─────┐
        │Metadata │ │  Disk   │ │ Evictor  │
        │ Store   │ │  Store  │ │          │
        │(BoltDB) │ │(files)  │ │(LRU,30s) │
        └─────────┘ └─────────┘ └──────────┘
```

## FUSE Node Hierarchy

The FUSE filesystem uses `go-fuse/v2`'s node-based API (`fs` package), where each file and directory is represented by an `InodeEmbedder` that implements specific operation interfaces.

### Node Types

**RootNode**: The root of a bucket mount. Embeds `DirNode` -- it is just a directory with an empty prefix. Exists as a separate type for type-switch clarity in `Rename`.

**DirNode**: Represents an S3 "prefix" (directory). Holds the prefix string (e.g., `"photos/"`) and a pointer to the shared bucket context. Implements:
- `Lookup` - Resolves a child name to a DirNode or FileNode
- `Readdir` - Returns a paginated DirStream
- `Mkdir` - Creates a zero-byte marker object
- `Create` - Creates a new file (returns a Handle)
- `Unlink` - Deletes a file object
- `Rmdir` - Checks empty, then deletes marker object
- `Rename` - CopyObject + DeleteObject (S3 has no rename)

**FileNode**: Represents an S3 object. Holds the key, size, ETag, and mtime. Implements:
- `Getattr` - Returns file attributes
- `Setattr` - Handles truncate
- `Open` - Creates and returns a Handle

**Handle**: Per-open-file state. Created on each `Open` or `Create`. This is where the write-through and cache-read logic lives. Implements:
- `Read` - Cache-first read with TTL/ETag validation
- `Write` - Appends to local temp file
- `Flush` - Uploads temp file to S3 (the consistency-critical path)
- `Fsync` - Same as Flush
- `Release` - Cleanup temp files

### Why FOPEN_DIRECT_IO

Every `Open` returns the `FOPEN_DIRECT_IO` flag. This tells the Linux kernel not to use its page cache for this file. Every Read and Write goes through our FUSE handlers.

This is essential because our cache layer is the authoritative cache. If the kernel also cached pages, we'd have two uncoordinated cache layers, and stale data would be served from the kernel's cache even after our cache expired.

The cost is that small sequential reads lose kernel readahead optimization, but for a network-backed filesystem where the bottleneck is S3 latency, this is the right tradeoff.

## Write Path Deep Dive

The write path is the most consistency-critical component.

### Lifecycle of a Write

```
1. Application opens file for writing
   -> DirNode.Create() or FileNode.Open()
   -> A temp file is created in /tmp
   -> Handle returned with FOPEN_DIRECT_IO

2. Application writes data (may be multiple write() calls)
   -> Handle.Write() appends to temp file at given offset
   -> dirty flag set to true
   -> No S3 communication at this point

3. Application closes file (or calls fsync)
   -> Kernel sends FLUSH to FUSE
   -> Handle.Flush():
      a. Seek temp file to beginning
      b. Stat temp file for size
      c. PutObject(key, tempFile, size) to S3
      d. If S3 returns error:
         - Log "S3 PUT FAILED"
         - Return syscall.EIO
         - Application's close() returns -1 with errno=EIO
         - Temp file remains (cleaned up in Release)
         - File is NOT in cache
      e. If S3 succeeds:
         - Open temp file again for reading
         - cache.Put(bucket, key, file, s3Meta)
         - Update FileNode's size, etag, mtime
         - Return 0 (success)

4. Kernel sends RELEASE
   -> Handle.Release():
      - Close and remove temp file
      - This is cleanup only, not data-path
```

### Why Buffer to Temp File

- Files can be arbitrarily large. In-memory buffers risk OOM.
- The temp file can be directly passed to `PutObject` as an `io.Reader`.
- If the process crashes mid-write, the temp file is automatically cleaned up by the OS (it's in `/tmp`).

### Why Flush, Not Write

S3 PutObject is an atomic operation -- you upload the entire object in one API call. There's no "append" or "partial update" in S3. We can't upload on each `Write` call because:
1. Each Write might be a tiny chunk (4KB kernel buffer).
2. We'd need to re-upload the entire file on each Write.
3. Intermediate states would be visible to other readers.

By buffering locally and uploading on Flush (triggered by `close()`), we get a single atomic upload of the final state.

## Read Path Deep Dive

### Cache Validation Logic

```go
func (h *Handle) Read(ctx, dest, off):
    // 1. If this handle has pending writes, read from temp file
    if h.dirty:
        return h.tmpFile.ReadAt(dest, off)

    // 2. Check cache
    _, entry, valid, _ := cache.Get(bucket, key, ttl, soleWriter)

    // 3. Cache hit + valid (within TTL)
    if valid:
        return cache.Store().Read(bucket, key, off, dest)

    // 4. Cache hit + expired + not sole writer: ETag revalidation
    if !soleWriter && entry != nil:
        s3Meta := s3.HeadObject(key)
        if s3Meta.ETag == entry.ETag:
            cache.Touch(bucket, key)  // refresh LRU
            return cache.Store().Read(bucket, key, off, dest)

    // 5. Cache miss or ETag mismatch: full fetch
    body, meta := s3.GetObject(key)
    // download to temp, cache it, serve from cache
```

### Sole Writer Optimization

When `sole_writer: true`, the gateway skips ETag revalidation (step 4). Since no other entity writes to the bucket, the cached data is guaranteed to be current until the TTL expires. This eliminates a `HeadObject` API call per expired cache entry, which can significantly reduce latency and API costs.

When `sole_writer: false`, expired cache entries trigger a `HeadObject` to check if the ETag still matches. If it does, the cache is refreshed without re-downloading the file. If not, a full `GetObject` is performed.

## Cache Internals

### Disk Layout

```
/var/cache/s3gw/
├── cache.db                        # BoltDB metadata database
└── data/
    ├── my-bucket/
    │   ├── a3/
    │   │   └── a3f5b8c9d1e2...     # SHA-256 of "photos/img001.jpg"
    │   └── 7b/
    │       └── 7b2c4e8f1a3d...     # SHA-256 of "docs/report.pdf"
    └── other-bucket/
        └── ...
```

The two-character hash prefix directory prevents any single directory from having too many entries (which degrades ext4/xfs performance).

### BoltDB Schema

Single bucket named `entries`. Keys are `<bucket>/<s3key>`. Values are JSON:

```json
{
  "bucket": "my-bucket",
  "key": "photos/img001.jpg",
  "local_path": "/var/cache/s3gw/data/my-bucket/a3/a3f5b8...",
  "etag": "\"d41d8cd98f00b204e9800998ecf8427e\"",
  "size": 1048576,
  "last_modified": "2024-01-15T10:30:00Z",
  "last_accessed": "2024-01-15T14:22:00Z",
  "cached_at": "2024-01-15T10:30:05Z"
}
```

Fields:
- `last_accessed`: Updated on every cache hit. Used for LRU ordering.
- `cached_at`: Set once when the entry is created. Used for TTL expiry calculation: `time.Since(cached_at) > ttl` means expired.
- `etag`: Used for revalidation against S3 in multi-writer mode.

### Eviction Algorithm

The evictor is a background goroutine running on a 30-second ticker.

```
Every 30 seconds:
  total = metadata.TotalSize()

  if total <= maxSize * 0.9:     # high-water mark
      return                      # nothing to do

  entries = metadata.ListByLastAccessed(100)  # oldest first

  for entry in entries:
      disk.Delete(entry)
      metadata.Delete(entry)
      total -= entry.Size

      if total <= maxSize * 0.8:  # target mark
          break
```

The 90%/80% hysteresis (high-water/low-water) prevents thrashing. Without it, adding one file over the limit would trigger eviction, then the next file would trigger it again.

The evictor can also be triggered immediately by `cache.Put()` when it detects the total size has crossed the high-water mark. This prevents unbounded growth between ticker intervals.

### Concurrency

The cache manager uses an `RWMutex`:
- `Get` and `Touch` acquire a read lock
- `Put`, `Invalidate`, and eviction acquire a write lock

This allows concurrent reads while serializing writes and evictions. Since FUSE operations are already concurrent (multiple file handles reading simultaneously), this lock granularity keeps reads fast.

## S3 Client Details

### Linode Compatibility

The S3 client is configured with:
- `UsePathStyle: true` - Linode uses path-style URLs (`endpoint/bucket/key`), not virtual-hosted-style (`bucket.endpoint/key`)
- Static credentials per bucket - each bucket can use different access keys
- Custom endpoint - each bucket can target a different Linode region

### Error Translation

S3 errors are translated to POSIX errno values so applications see standard file operation errors:

| S3 Error | HTTP Status | errno | Application sees |
|----------|-------------|-------|-----------------|
| NoSuchKey | 404 | ENOENT | "No such file or directory" |
| AccessDenied | 403 | EACCES | "Permission denied" |
| Conflict | 409 | EEXIST | "File exists" |
| SlowDown | 503 | EAGAIN | "Resource temporarily unavailable" |
| Any other | varies | EIO | "Input/output error" |

### S3 Operation Mapping

| Filesystem op | S3 API call(s) |
|---------------|----------------|
| stat (file) | HeadObject |
| stat (dir) | ListObjectsV2 (1 result, prefix+delimiter) |
| read | GetObject (on cache miss) |
| write + close | PutObject |
| create | (deferred to PutObject on close) |
| delete file | DeleteObject |
| mkdir | PutObject (zero-byte, key ending in `/`) |
| rmdir | ListObjectsV2 (check empty) + DeleteObject |
| rename | CopyObject + DeleteObject |
| ls | ListObjectsV2 (paginated, with delimiter) |

## Lifecycle and Shutdown

### Startup Sequence

```
1. Parse CLI flags (--config path)
2. Load and validate YAML config
3. Initialize structured logger
4. Create shared CacheManager:
   a. Open/create BoltDB at <cache_dir>/cache.db
   b. Create DiskStore at <cache_dir>/data/
   c. Start Evictor goroutine
5. For each configured bucket:
   a. Create S3 client with bucket's credentials/endpoint
   b. mkdir -p the mount point
   c. Create FUSE RootNode
   d. Call fs.Mount() to mount the filesystem
   e. Spawn goroutine running server.Wait()
6. Block on SIGINT/SIGTERM
```

### Shutdown Sequence

```
1. Signal received (SIGINT or SIGTERM)
2. Stop evictor goroutine (prevent evictions during unmount)
3. For each mount (reverse order):
   a. server.Unmount()
      - Kernel flushes all open handles (triggers Flush -> S3 upload)
      - Kernel releases all handles (triggers Release -> cleanup)
      - Mount is removed from VFS
   b. server.Wait() (blocks until FUSE event loop exits)
4. Close CacheManager:
   a. Close BoltDB (waits for active transactions)
5. Exit
```

### Crash Behavior

If s3gw crashes without graceful shutdown:
- **Dirty writes**: Any data written but not yet `close()`d by the application is lost (it's in a temp file that the OS may clean up). This is the same behavior as a local filesystem crash.
- **Cache**: May have orphaned entries (metadata pointing to nonexistent files, or files without metadata). The cache self-heals: on next `Get()`, if the disk file is missing, the metadata entry is deleted.
- **Stale mounts**: The kernel mount remains in a broken state. On restart, s3gw will fail to mount at the same point. Use `fusermount -uz <path>` to clean up before restarting.

## Performance Characteristics

### Latency Budget

| Operation | Best case (cache hit) | Worst case (cache miss) |
|-----------|----------------------|------------------------|
| Read (small file) | < 1ms (local disk) | 50-200ms (S3 GET, depends on region/size) |
| Read (large file) | < 10ms (local disk seek) | Seconds (download from S3) |
| Write (any size) | N/A (always hits S3) | 50-500ms+ (S3 PUT, depends on size) |
| ls (directory) | 50-200ms (always hits S3) | Same |
| stat (file) | < 1ms (FUSE attr cache) | 50-100ms (S3 HEAD) |

### Throughput

Throughput is bounded by:
1. **Network bandwidth** between the VM and Linode Object Storage
2. **Local disk I/O** for cache reads/writes
3. **FUSE overhead**: ~10-20us per context switch between kernel and userspace

For cached reads, expect near-local-disk performance. For uncached reads and all writes, expect network-limited performance.

### Scaling Limits

- **File count**: BoltDB handles millions of entries. Directory listing is paginated. No practical limit.
- **File size**: Limited by available temp space (writes buffer the entire file before upload) and S3's 5TB object limit.
- **Concurrent operations**: FUSE handles are independent. Many files can be read/written simultaneously. The cache RWMutex serializes cache writes but allows concurrent reads.
- **Number of buckets**: Each mount uses one goroutine. Hundreds of mounts are fine.
