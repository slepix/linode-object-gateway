package s3client

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type ObjectMeta struct {
	ETag         string
	Size         int64
	LastModified time.Time
	ContentType  string
}

type ObjectEntry struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

type ListResult struct {
	Objects           []ObjectEntry
	CommonPrefixes    []string
	ContinuationToken *string
}

func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, *ObjectMeta, error) {
	out, err := c.inner.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, nil, err
	}

	meta := &ObjectMeta{
		Size:         aws.ToInt64(out.ContentLength),
		ContentType:  aws.ToString(out.ContentType),
		LastModified: aws.ToTime(out.LastModified),
	}
	if out.ETag != nil {
		meta.ETag = *out.ETag
	}

	return out.Body, meta, nil
}

func (c *Client) PutObject(ctx context.Context, key string, body io.Reader, size int64) (*ObjectMeta, error) {
	input := &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
	}

	out, err := c.inner.PutObject(ctx, input)
	if err != nil {
		return nil, err
	}

	meta := &ObjectMeta{
		Size: size,
	}
	if out.ETag != nil {
		meta.ETag = *out.ETag
	}

	return meta, nil
}

func (c *Client) HeadObject(ctx context.Context, key string) (*ObjectMeta, error) {
	out, err := c.inner.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}

	meta := &ObjectMeta{
		Size:         aws.ToInt64(out.ContentLength),
		ContentType:  aws.ToString(out.ContentType),
		LastModified: aws.ToTime(out.LastModified),
	}
	if out.ETag != nil {
		meta.ETag = *out.ETag
	}

	return meta, nil
}

func (c *Client) DeleteObject(ctx context.Context, key string) error {
	_, err := c.inner.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (c *Client) ListObjects(ctx context.Context, prefix, delimiter string, contToken *string) (*ListResult, error) {
	input := &s3.ListObjectsV2Input{
		Bucket:    aws.String(c.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String(delimiter),
		MaxKeys:   aws.Int32(1000),
	}
	if contToken != nil {
		input.ContinuationToken = contToken
	}

	out, err := c.inner.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, err
	}

	result := &ListResult{}

	for _, obj := range out.Contents {
		key := aws.ToString(obj.Key)
		if key == prefix {
			continue
		}
		result.Objects = append(result.Objects, ObjectEntry{
			Key:          key,
			Size:         aws.ToInt64(obj.Size),
			LastModified: aws.ToTime(obj.LastModified),
			ETag:         aws.ToString(obj.ETag),
		})
	}

	for _, cp := range out.CommonPrefixes {
		result.CommonPrefixes = append(result.CommonPrefixes, aws.ToString(cp.Prefix))
	}

	if aws.ToBool(out.IsTruncated) {
		result.ContinuationToken = out.NextContinuationToken
	}

	return result, nil
}

func (c *Client) CopyObject(ctx context.Context, srcKey, dstKey string) error {
	copySource := c.bucket + "/" + srcKey
	_, err := c.inner.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(c.bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(dstKey),
	})
	return err
}

func (c *Client) IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nsk *types.NoSuchKey
	var nsb *types.NotFound
	if ok := isErrorType(err, &nsk); ok {
		return true
	}
	if ok := isErrorType(err, &nsb); ok {
		return true
	}
	return strings.Contains(err.Error(), "StatusCode: 404") ||
		strings.Contains(err.Error(), "NoSuchKey")
}

func isErrorType[T error](err error, target *T) bool {
	for err != nil {
		if e, ok := err.(T); ok {
			*target = e
			return true
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
		} else {
			break
		}
	}
	return false
}
