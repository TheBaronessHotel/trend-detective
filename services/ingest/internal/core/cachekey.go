package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"google.golang.org/protobuf/proto"
)

// keyVersion changes when the bucket layout or the dataset manifest changes,
// which invalidates every cached dataset at once.
const keyVersion = "ingest/v1"

// CacheKey identifies what a task would fetch. Two tasks with the same key
// write the same files, so the second one reuses the first one's dataset and
// costs no NASA quota.
//
// The key covers the variable, rectangle, dates and adapter settings, plus the
// adapter's fingerprint. It leaves out the Earthdata token key, which changes
// per job without changing the data.
func CacheKey(task *ingestv1.IngestTask, fingerprint string) (string, error) {
	t := proto.CloneOf(task)
	t.EarthdataTokenKey = ""
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("cache key: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", keyVersion, fingerprint)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}
