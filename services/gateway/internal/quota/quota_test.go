package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TheBaronessHotel/trend-detective/internal/harmonyslots"
	"github.com/alicebob/miniredis/v2"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/redis/go-redis/v9"
)

type fakeGiovanni struct {
	used float64
	err  error
}

func (f fakeGiovanni) GiovanniQuota(context.Context) (float64, float64, error) {
	return f.used, 0.9, f.err
}

func setup(t *testing.T, g Giovanni) (*Service, *redis.Client) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	s := New(g, rdb, "team-token", 4, logging.Noop())
	s.now = func() time.Time { return time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC) }
	return s, rdb
}

func TestGetReportsBothAllowances(t *testing.T) {
	s, rdb := setup(t, fakeGiovanni{used: 0.12})
	rdb.ZAdd(context.Background(), harmonyslots.Key("team-token"), redis.Z{Score: float64(s.now().Unix() - 60), Member: "job-a"})

	got := s.Get(context.Background())
	if got.GetGiovanniUsedFraction() != 0.12 || got.GetGiovanniCutoff() != 0.9 || got.GetHarmonyInFlight() != 1 || got.GetHarmonyLimit() != 4 {
		t.Errorf("quota: %v", got)
	}
}

func TestGiovanniDownStillReportsHarmony(t *testing.T) {
	s, rdb := setup(t, fakeGiovanni{err: errors.New("giovanni is down")})
	rdb.ZAdd(context.Background(), harmonyslots.Key("team-token"), redis.Z{Score: float64(s.now().Unix() - 60), Member: "job-a"})

	got := s.Get(context.Background())
	if got.GetGiovanniUsedFraction() != Unknown || got.GetHarmonyInFlight() != 1 {
		t.Errorf("quota: %v", got)
	}
}
