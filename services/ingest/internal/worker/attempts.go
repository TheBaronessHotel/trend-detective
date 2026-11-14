package worker

import (
	"context"
	"errors"
	"time"

	"github.com/TheBaronessHotel/trend-detective/internal/jobcancel"
	"github.com/redis/go-redis/v9"
)

// RedisAttempts keeps failed-attempt counts in Redis, one key per task.
type RedisAttempts struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisAttempts returns counts that expire after ttl.
func NewRedisAttempts(rdb *redis.Client, ttl time.Duration) *RedisAttempts {
	return &RedisAttempts{rdb: rdb, ttl: ttl}
}

func attemptsKey(taskID string) string { return "td:ingest:failures:" + taskID }

// Count implements Attempts.
func (r *RedisAttempts) Count(ctx context.Context, taskID string) (int, error) {
	n, err := r.rdb.Get(ctx, attemptsKey(taskID)).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

// Failed implements Attempts.
func (r *RedisAttempts) Failed(ctx context.Context, taskID string) (int, error) {
	k := attemptsKey(taskID)
	n, err := r.rdb.Incr(ctx, k).Result()
	if err != nil {
		return 0, err
	}
	_ = r.rdb.Expire(ctx, k, r.ttl).Err()
	return int(n), nil
}

// Clear implements Attempts.
func (r *RedisAttempts) Clear(ctx context.Context, taskID string) error {
	return r.rdb.Del(ctx, attemptsKey(taskID)).Err()
}

// RedisCancels reads the cancel flag the gateway raises in Redis.
type RedisCancels struct{ rdb *redis.Client }

// NewRedisCancels returns a Cancels backed by rdb.
func NewRedisCancels(rdb *redis.Client) RedisCancels { return RedisCancels{rdb: rdb} }

// Canceled implements Cancels.
func (c RedisCancels) Canceled(ctx context.Context, jobID string) (bool, error) {
	return jobcancel.Canceled(ctx, c.rdb, jobID)
}
