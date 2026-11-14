package server

import (
	"context"
	"errors"
	"fmt"
	"io"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Loader reads one protojson document named by a BlobRef into msg. It
// returns an error wrapping blob.ErrNotFound when the object doesn't exist,
// and a *StorageError when the store can't be reached or read.
type Loader interface {
	Load(ctx context.Context, ref *commonv1.BlobRef, msg proto.Message) error
}

// ObjectReader is the part of *blob.Bucket the narrator uses.
type ObjectReader interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// StorageError means the object store failed, so a retry may work. It maps
// to Unavailable. A document that was read but doesn't decode is not one.
type StorageError struct{ Err error }

func (e *StorageError) Error() string { return "storage: " + e.Err.Error() }
func (e *StorageError) Unwrap() error { return e.Err }

// maxDocBytes bounds one analysis document. Grids are the largest, at a few
// MB for a 128x128 grid with several layers.
const maxDocBytes = 64 << 20

// BucketLoader reads analysis documents from one bucket. Refs naming any
// other bucket are refused, so a caller can't make the narrator read raw
// data or uploads.
type BucketLoader struct {
	Name   string
	Bucket ObjectReader
}

// Load implements Loader. An empty ref bucket means Name.
func (l BucketLoader) Load(ctx context.Context, ref *commonv1.BlobRef, msg proto.Message) error {
	if b := ref.GetBucket(); b != "" && b != l.Name {
		return invalid(fmt.Sprintf("results must be in the %q bucket, got %q", l.Name, b))
	}
	key := ref.GetKey()
	r, err := l.Bucket.Get(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return err
	}
	if err != nil {
		return &StorageError{Err: err}
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxDocBytes+1))
	if err != nil {
		return &StorageError{Err: fmt.Errorf("read %s/%s: %w", l.Name, key, err)}
	}
	if len(data) > maxDocBytes {
		return fmt.Errorf("%s/%s is larger than %d bytes", l.Name, key, maxDocBytes)
	}
	// Same options as blob.Bucket.GetProto, so newer writers don't break us.
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, msg); err != nil {
		return fmt.Errorf("decode %s/%s: %w", l.Name, key, err)
	}
	return nil
}
