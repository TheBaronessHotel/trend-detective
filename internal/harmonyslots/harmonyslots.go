// Package harmonyslots names and counts the Harmony job slots that ingest
// takes per Earthdata token. Ingest writes them; the gateway reads them for
// GetQuota. Both use this package, so they agree on the keys.
package harmonyslots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Stale is how long a slot counts without being freed. A slot older than
// this belongs to a job that ingest lost track of.
const Stale = 6 * time.Hour

// Key names a token's slots: a sorted set of request keys scored by the Unix
// time each slot was taken. The token goes in as a hash, so it never appears
// in Redis keys.
func Key(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "td:ingest:harmony:slots:" + hex.EncodeToString(sum[:8])
}

// InFlight counts a token's slots taken within Stale of now.
func InFlight(ctx context.Context, rdb redis.Cmdable, token string, now time.Time) (int64, error) {
	return rdb.ZCount(ctx, Key(token), strconv.FormatInt(now.Add(-Stale).Unix(), 10), "+inf").Result()
}
