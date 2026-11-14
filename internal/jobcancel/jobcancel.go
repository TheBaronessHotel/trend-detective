// Package jobcancel is the flag the gateway raises when a job is cancelled,
// for stages to check before they work on one of its tasks. The gateway
// writes it; stages only read it.
package jobcancel

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// Key names a job's cancel flag. It shares the job's hash tag, so it sits
// with the job's other keys.
func Key(jobID string) string { return "td:job:{" + jobID + "}:canceled" }

// Canceled reports whether a job has been cancelled.
func Canceled(ctx context.Context, rdb redis.Cmdable, jobID string) (bool, error) {
	n, err := rdb.Exists(ctx, Key(jobID)).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	return n > 0, err
}
