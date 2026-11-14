// Package earthdata picks the Earthdata token a task's requests use. Every
// adapter that talks to a login-protected NASA service shares it.
package earthdata

import (
	"context"
	"errors"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/redis/go-redis/v9"
)

// Tokens looks up a task's token.
type Tokens struct {
	rdb  *redis.Client
	team string
}

// NewTokens returns Tokens. rdb holds the users' own tokens, which the
// gateway saves there. team is EARTHDATA_TOKEN, or empty.
func NewTokens(rdb *redis.Client, team string) *Tokens {
	return &Tokens{rdb: rdb, team: team}
}

// For returns the user's own token when the task has one, else the team's.
func (t *Tokens) For(ctx context.Context, task *ingestv1.IngestTask) (string, error) {
	if key := task.GetEarthdataTokenKey(); key != "" {
		tok, err := t.rdb.Get(ctx, key).Result()
		if err == nil && tok != "" {
			return tok, nil
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			return "", core.Transient(err)
		}
		// The user's token expired from Redis. The team token still works.
	}
	if t.team == "" {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false,
			"Fetching NASA data needs an Earthdata token.", "EARTHDATA_TOKEN is empty and the task has no user token")
	}
	return t.team, nil
}
