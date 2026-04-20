package fuse

import (
	"context"
	"hash/fnv"
	"log/slog"
	"path"
	"strings"
	"syscall"

	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/s3gateway/internal/catalog"
	"github.com/s3gateway/internal/s3client"
)

func stableIno(name string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	return h.Sum64()
}

// catalogDirStream serves entries from the catalog (SQLite).
type catalogDirStream struct {
	entries []gofuse.DirEntry
	pos     int
}

func newCatalogDirStream(catEntries []catalog.Entry) *catalogDirStream {
	fuseEntries := make([]gofuse.DirEntry, 0, len(catEntries))
	for _, e := range catEntries {
		mode := uint32(syscall.S_IFREG)
		if e.IsDir {
			mode = syscall.S_IFDIR
		}
		fuseEntries = append(fuseEntries, gofuse.DirEntry{
			Name: e.Name,
			Ino:  stableIno(e.Key),
			Mode: mode,
		})
	}
	return &catalogDirStream{entries: fuseEntries}
}

func (s *catalogDirStream) HasNext() bool {
	return s.pos < len(s.entries)
}

func (s *catalogDirStream) Next() (gofuse.DirEntry, syscall.Errno) {
	if s.pos >= len(s.entries) {
		return gofuse.DirEntry{}, syscall.EIO
	}
	entry := s.entries[s.pos]
	s.pos++
	return entry, 0
}

func (s *catalogDirStream) Close() {}

// S3DirStream is the original paginated S3 directory stream (fallback).
type S3DirStream struct {
	s3      *s3client.Client
	bucket  string
	prefix  string
	ctx     context.Context
	entries []gofuse.DirEntry
	pos     int
	token   *string
	done    bool
	loaded  bool
}

func (s *S3DirStream) HasNext() bool {
	if s.pos < len(s.entries) {
		return true
	}
	if s.done {
		return false
	}

	if err := s.fetchPage(); err != nil {
		slog.Error("dirstream fetch error", "prefix", s.prefix, "error", err)
		return false
	}

	return s.pos < len(s.entries)
}

func (s *S3DirStream) Next() (gofuse.DirEntry, syscall.Errno) {
	if s.pos >= len(s.entries) {
		return gofuse.DirEntry{}, syscall.EIO
	}
	entry := s.entries[s.pos]
	s.pos++
	return entry, 0
}

func (s *S3DirStream) Close() {}

func (s *S3DirStream) fetchPage() error {
	result, err := s.s3.ListObjects(s.ctx, s.prefix, "/", s.token)
	if err != nil {
		s.done = true
		return err
	}

	s.entries = s.entries[:0]
	s.pos = 0

	for _, cp := range result.CommonPrefixes {
		name := strings.TrimPrefix(cp, s.prefix)
		name = strings.TrimSuffix(name, "/")
		if name == "" {
			continue
		}
		s.entries = append(s.entries, gofuse.DirEntry{
			Name: name,
			Ino:  stableIno(cp),
			Mode: syscall.S_IFDIR,
		})
	}

	for _, obj := range result.Objects {
		name := path.Base(obj.Key)
		if obj.Key == s.prefix {
			continue
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue
		}
		s.entries = append(s.entries, gofuse.DirEntry{
			Name: name,
			Ino:  stableIno(obj.Key),
			Mode: syscall.S_IFREG,
		})
	}

	if result.ContinuationToken != nil {
		s.token = result.ContinuationToken
	} else {
		s.done = true
	}

	s.loaded = true
	return nil
}
