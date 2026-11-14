package harmonyslots

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInFlightSkipsStaleSlots(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	key := Key("team-token")
	rdb.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-time.Minute).Unix()), Member: "a"})
	rdb.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-2 * time.Hour).Unix()), Member: "b"})
	rdb.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-7 * time.Hour).Unix()), Member: "lost"})

	n, err := InFlight(ctx, rdb, "team-token", now)
	if err != nil || n != 2 {
		t.Errorf("in flight: %d, %v; want 2", n, err)
	}
	if n, _ := InFlight(ctx, rdb, "someone-else", now); n != 0 {
		t.Errorf("another token's slots: %d", n)
	}
}

func TestKeyHidesTheToken(t *testing.T) {
	if k := Key("secret-token"); k == "" || len(k) != len("td:ingest:harmony:slots:")+16 {
		t.Errorf("key %q", k)
	}
}
