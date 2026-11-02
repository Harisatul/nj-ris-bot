package r2

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Client struct {
	Bucket  string
	API     *s3.Client
	Presign *s3.PresignClient
}

func New(account, key, secret, bucket string) *Client {
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", account)
	cfg := aws.Config{Region: "auto", Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(key, secret, ""))}
	api := s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(endpoint); o.UsePathStyle = true })
	return &Client{Bucket: bucket, API: api, Presign: s3.NewPresignClient(api)}
}
func (c *Client) Upload(ctx context.Context, key, mime string, body io.Reader, size int64) error {
	_, err := c.API.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(mime)})
	return err
}
func (c *Client) DownloadURL(ctx context.Context, key string) (string, error) {
	v, err := c.Presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)}, func(o *s3.PresignOptions) { o.Expires = 5 * time.Minute })
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(v.URL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("unexpected R2 URL scheme")
	}
	return v.URL, nil
}
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.API.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)})
	return err
}
