// Package r2 is the S3-compatible surface Outpost needs on Cloudflare R2: put,
// stat, list and delete. That is all of it — R2 is a dumb blob shelf here, no
// metadata and no database.
//
// The orchestrator itself never writes to R2: the VPS pushes the world up with
// rclone over the tailnet, so the bytes cross the network once. What needs this
// package is `outpost publish`, which fills the buckets from the Mac.
package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound is returned for a key that does not exist. It is a normal answer:
// publish asks "is this already there?" for every file.
var ErrNotFound = errors.New("r2: object not found")

// Object is the part of a listed key Outpost cares about.
type Object struct {
	Key  string
	Size int64
}

// Client talks to one R2 account.
type Client struct {
	mc *minio.Client
}

// New returns a client for an account id and a token pair.
func New(accountID, accessKey, secretKey string) (*Client, error) {
	endpoint := fmt.Sprintf("%s.r2.cloudflarestorage.com", strings.TrimSpace(accountID))
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: true,
		// R2 does not have regions in the S3 sense; "auto" is what Cloudflare's
		// own examples use and it keeps signature errors off the table.
		Region: "auto",
	})
	if err != nil {
		return nil, fmt.Errorf("r2 client: %w", err)
	}
	return &Client{mc: mc}, nil
}

// Put uploads size bytes from r at key.
func (c *Client) Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	_, err := c.mc.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType(key),
	})
	if err != nil {
		return fmt.Errorf("put %s/%s: %w", bucket, key, err)
	}
	return nil
}

// Stat returns the size of key, or ErrNotFound.
func (c *Client) Stat(ctx context.Context, bucket, key string) (int64, error) {
	info, err := c.mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isMissing(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("stat %s/%s: %w", bucket, key, err)
	}
	return info.Size, nil
}

// List returns every key under prefix, recursively.
func (c *Client) List(ctx context.Context, bucket, prefix string) ([]Object, error) {
	var out []Object
	for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("list %s/%s: %w", bucket, prefix, obj.Err)
		}
		out = append(out, Object{Key: obj.Key, Size: obj.Size})
	}
	return out, nil
}

// Remove deletes one key. A key that is already gone is not an error.
func (c *Client) Remove(ctx context.Context, bucket, key string) error {
	err := c.mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !isMissing(err) {
		return fmt.Errorf("remove %s/%s: %w", bucket, key, err)
	}
	return nil
}

func isMissing(err error) bool {
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NoSuchObject", "NotFound", "XMinioInvalidObjectName":
		return true
	}
	return resp.StatusCode == 404
}

// contentType is a small nicety: R2 does not care, but a browser that lists a
// bucket does, and the JSON files are the ones a person might open.
func contentType(key string) string {
	switch {
	case strings.HasSuffix(key, ".json"):
		return "application/json"
	case strings.HasSuffix(key, ".txt"), strings.HasSuffix(key, ".properties"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(key, ".sh"):
		return "text/x-shellscript"
	case strings.HasSuffix(key, ".jar"):
		return "application/java-archive"
	case strings.HasSuffix(key, ".toml"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
