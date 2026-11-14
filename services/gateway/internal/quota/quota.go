// Package quota answers GetQuota: how much of the team's NASA allowances is
// in use right now.
package quota

import (
	"context"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/harmonyslots"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/redis/go-redis/v9"
)

// Giovanni reads the team token's Giovanni quota. preflight.Checker does.
type Giovanni interface {
	GiovanniQuota(ctx context.Context) (used, cutoff float64, err error)
}

// Unknown is the Giovanni share reported when Giovanni couldn't be asked.
const Unknown = -1

// Service answers GetQuota.
type Service struct {
	giovanni Giovanni
	rdb      redis.Cmdable
	// The team's EARTHDATA_TOKEN, whose Harmony slots are counted.
	token string
	// HARMONY_CONCURRENCY, the slots ingest allows per token.
	limit int32
	log   logging.Logger
	now   func() time.Time
}

// New returns a Service.
func New(giovanni Giovanni, rdb redis.Cmdable, token string, limit int32, log logging.Logger) *Service {
	return &Service{giovanni: giovanni, rdb: rdb, token: token, limit: limit, log: log.With("component", "quota"), now: time.Now}
}

// Get reports the Giovanni share used and the Harmony jobs in flight. A part
// that can't be read is reported as unknown rather than failing the call:
// the Giovanni share as Unknown, Harmony as nothing in flight.
func (s *Service) Get(ctx context.Context) *gatewayv1.GetQuotaResponse {
	res := &gatewayv1.GetQuotaResponse{GiovanniUsedFraction: Unknown, HarmonyLimit: s.limit}
	used, cutoff, err := s.giovanni.GiovanniQuota(ctx)
	res.GiovanniCutoff = cutoff
	if err != nil {
		s.log.Warn(ctx, "couldn't read the Giovanni quota", logging.Err(err))
	} else {
		res.GiovanniUsedFraction = used
	}
	if s.token != "" {
		n, err := harmonyslots.InFlight(ctx, s.rdb, s.token, s.now())
		if err != nil {
			s.log.Warn(ctx, "couldn't count Harmony slots", logging.Err(err))
		}
		res.HarmonyInFlight = int32(n)
	}
	return res
}
