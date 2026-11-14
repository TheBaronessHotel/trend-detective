package blob

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// region is fixed, so signing never asks S3 for a bucket's location. With
// SeaweedFS any region signs correctly, and us-east-1 is what minio-go would
// have looked up anyway.
const region = "us-east-1"

// Presigner signs URLs for the S3 address the browser uses, such as
// http://localhost:8333. A SigV4 signature covers the host, so a URL signed
// for the internal address seaweedfs:8333 would be rejected when the browser
// sends it to localhost:8333. Signing happens locally, with no request to S3.
type Presigner struct {
	mc *minio.Client
}

// NewPresigner returns a Presigner for publicEndpoint, a URL like
// http://localhost:8333.
func NewPresigner(publicEndpoint, accessKey, secretKey string) (*Presigner, error) {
	u, err := url.Parse(publicEndpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("public S3 endpoint must be a URL like http://localhost:8333, got %q", publicEndpoint)
	}
	mc, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: u.Scheme == "https",
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("S3 signer: %w", err)
	}
	return &Presigner{mc: mc}, nil
}

// PresignGet returns a URL that downloads bucket/key until ttl runs out.
func (p *Presigner) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	u, err := p.mc.PresignedGetObject(ctx, bucket, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("sign GET %s/%s: %w", bucket, key, err)
	}
	return u.String(), nil
}

// PresignPut returns a URL that uploads bucket/key with a PUT until ttl runs
// out. It can't limit the upload's size: the reader has to check that.
func (p *Presigner) PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	u, err := p.mc.PresignedPutObject(ctx, bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("sign PUT %s/%s: %w", bucket, key, err)
	}
	return u.String(), nil
}
