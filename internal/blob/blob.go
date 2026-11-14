// Package blob reads and writes objects in SeaweedFS through its S3 API.
package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ErrNotFound is returned when an object doesn't exist.
var ErrNotFound = errors.New("object not found")

// Store is an S3 client.
type Store struct {
	mc *minio.Client
}

// New connects to an S3 endpoint given as a URL, like http://seaweedfs:8333.
func New(endpoint, accessKey, secretKey string) (*Store, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("S3 endpoint must be a URL like http://seaweedfs:8333, got %q", endpoint)
	}
	mc, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: u.Scheme == "https",
	})
	if err != nil {
		return nil, fmt.Errorf("S3 client: %w", err)
	}
	return &Store{mc: mc}, nil
}

// Bucket returns a handle scoped to one bucket.
func (s *Store) Bucket(name string) *Bucket {
	return &Bucket{mc: s.mc, name: name}
}

// Bucket reads and writes objects in one bucket.
type Bucket struct {
	mc   *minio.Client
	name string
}

// Put uploads body under key. Pass size -1 when it's unknown, and the upload
// is streamed in parts.
func (b *Bucket) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (*commonv1.BlobRef, error) {
	info, err := b.mc.PutObject(ctx, b.name, key, body, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return nil, fmt.Errorf("put %s/%s: %w", b.name, key, err)
	}
	return &commonv1.BlobRef{Bucket: b.name, Key: key, SizeBytes: info.Size, ContentType: contentType}, nil
}

// Get opens an object. The caller closes it. It returns ErrNotFound when the
// object doesn't exist.
func (b *Bucket) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := b.mc.GetObject(ctx, b.name, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", b.name, key, err)
	}
	// GetObject is lazy. Stat makes it report a missing object now.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get %s/%s: %w", b.name, key, err)
	}
	return obj, nil
}

// PutProto writes msg as protojson.
func (b *Bucket) PutProto(ctx context.Context, key string, msg proto.Message) (*commonv1.BlobRef, error) {
	data, err := protojson.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", key, err)
	}
	return b.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "application/json")
}

// GetProto reads protojson into msg. It returns ErrNotFound when the object
// doesn't exist.
func (b *Bucket) GetProto(ctx context.Context, key string, msg proto.Message) error {
	r, err := b.Get(ctx, key)
	if err != nil {
		return err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read %s/%s: %w", b.name, key, err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, msg); err != nil {
		return fmt.Errorf("decode %s/%s: %w", b.name, key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	code := minio.ToErrorResponse(err).Code
	return code == "NoSuchKey" || code == "NotFound"
}
