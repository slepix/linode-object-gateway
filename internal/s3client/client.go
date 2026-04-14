package s3client

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Client struct {
	inner  *s3.Client
	bucket string
}

func NewClient(bucket, region, endpoint, accessKey, secretKey string) *Client {
	creds := credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")

	opts := s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       region,
		Credentials:  creds,
		UsePathStyle: true,
	}

	client := s3.New(opts)

	return &Client{
		inner:  client,
		bucket: bucket,
	}
}

func (c *Client) Bucket() string {
	return c.bucket
}

func (c *Client) Raw() *s3.Client {
	return c.inner
}

func (c *Client) ctx() context.Context {
	return context.Background()
}
