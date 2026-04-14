package fuse

import (
	"context"
	"log/slog"
	"path"
	"strings"
	"syscall"

	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/s3gateway/internal/s3client"
)

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
