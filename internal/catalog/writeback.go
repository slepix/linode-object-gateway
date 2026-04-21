package catalog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/s3gateway/internal/s3client"
)

type UploadJob struct {
	Bucket   string
	Key      string
	FilePath string
	Size     int64
	Retries  int
	DoneCh   chan UploadResult
}

type UploadResult struct {
	Meta *s3client.ObjectMeta
	Err  error
}

type WriteBackQueue struct {
	jobs       chan *UploadJob
	workers    int
	maxRetries int
	retryDelay time.Duration
	wg         sync.WaitGroup
	stopCh     chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	getClient  func(bucket string) *s3client.Client
}

func NewWriteBackQueue(workers, queueSize, maxRetries int, retryDelay time.Duration, getClient func(string) *s3client.Client) *WriteBackQueue {
	ctx, cancel := context.WithCancel(context.Background())
	return &WriteBackQueue{
		jobs:       make(chan *UploadJob, queueSize),
		workers:    workers,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
		stopCh:     make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		getClient:  getClient,
	}
}

func (wbq *WriteBackQueue) Start() {
	for i := 0; i < wbq.workers; i++ {
		wbq.wg.Add(1)
		go wbq.worker(i)
	}
	slog.Info("write-back queue started", "workers", wbq.workers)
}

func (wbq *WriteBackQueue) Stop() {
	close(wbq.stopCh)
	wbq.wg.Wait()
	wbq.cancel()
	slog.Info("write-back queue stopped")
}

func (wbq *WriteBackQueue) Submit(job *UploadJob) error {
	select {
	case wbq.jobs <- job:
		return nil
	default:
		return fmt.Errorf("write-back queue full")
	}
}

func (wbq *WriteBackQueue) worker(id int) {
	defer wbq.wg.Done()
	for {
		select {
		case <-wbq.stopCh:
			wbq.drain()
			return
		case job := <-wbq.jobs:
			wbq.process(job)
		}
	}
}

func (wbq *WriteBackQueue) drain() {
	for {
		select {
		case job := <-wbq.jobs:
			wbq.process(job)
		default:
			return
		}
	}
}

func (wbq *WriteBackQueue) process(job *UploadJob) {
	s3c := wbq.getClient(job.Bucket)
	if s3c == nil {
		result := UploadResult{Err: fmt.Errorf("no s3 client for bucket %s", job.Bucket)}
		sendResult(job, result)
		return
	}

	var lastErr error
	for attempt := 0; attempt <= wbq.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-wbq.stopCh:
				sendResult(job, UploadResult{Err: fmt.Errorf("shutting down, upload aborted")})
				return
			case <-time.After(wbq.retryDelay):
			}
		}

		meta, err := wbq.upload(s3c, job)
		if err == nil {
			slog.Debug("write-back upload complete", "bucket", job.Bucket, "key", job.Key)
			sendResult(job, UploadResult{Meta: meta})
			os.Remove(job.FilePath)
			return
		}

		lastErr = err
		slog.Warn("write-back upload attempt failed",
			"bucket", job.Bucket, "key", job.Key,
			"attempt", attempt+1, "error", err,
		)
	}

	slog.Error("write-back upload exhausted retries",
		"bucket", job.Bucket, "key", job.Key, "error", lastErr,
	)
	sendResult(job, UploadResult{Err: lastErr})
}

func (wbq *WriteBackQueue) upload(s3c *s3client.Client, job *UploadJob) (*s3client.ObjectMeta, error) {
	f, err := os.Open(job.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open upload file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat upload file: %w", err)
	}

	meta, err := s3c.PutObject(wbq.ctx, job.Key, io.Reader(f), stat.Size())
	if err != nil {
		return nil, fmt.Errorf("s3 put: %w", err)
	}
	return meta, nil
}

func sendResult(job *UploadJob, result UploadResult) {
	if job.DoneCh != nil {
		select {
		case job.DoneCh <- result:
		default:
		}
	}
}
