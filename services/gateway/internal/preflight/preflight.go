// Package preflight asks NASA, before a job is queued, whether it can run.
// A job that would fetch too many files, find no data, or hit a used-up quota
// is refused right away with a plain message, instead of failing in ingest a
// minute later.
//
// The checks fail open. If NASA can't be reached in time, the job goes ahead
// and ingest's own checks apply. Only a definite answer refuses a job.
package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
)

// Refusal is a definite no. Its message is shown to the user.
type Refusal struct {
	Msg string
	// Quota means a quota ran out, so trying again later can work.
	Quota bool
}

func (r *Refusal) Error() string { return r.Msg }

// Config holds the checker's settings.
type Config struct {
	CMRURL      string
	HarmonyURL  string
	GiovanniURL string
	// The team's EARTHDATA_TOKEN, for the Giovanni quota. CMR and Harmony's
	// capabilities are public.
	TeamToken string
	// Refuse Giovanni jobs once this share of the monthly quota is used.
	QuotaCutoff float64
	// How long all the checks of one job may take together.
	Timeout time.Duration
}

// Checker runs the checks.
type Checker struct {
	cfg  Config
	http *http.Client
	log  logging.Logger

	mu   sync.Mutex
	caps map[string]capsResult
}

type capsResult struct {
	ok      bool
	checked time.Time
}

// How long a collection's Harmony capabilities are trusted.
const capsTTL = 6 * time.Hour

// New returns a Checker.
func New(cfg Config, log logging.Logger) *Checker {
	return &Checker{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		log:  log.With("component", "preflight"),
		caps: map[string]capsResult{},
	}
}

// Check returns a *Refusal when the task can't run, and nil otherwise,
// including when NASA couldn't be asked. userToken is the user's own
// Earthdata token, or empty.
func (c *Checker) Check(ctx context.Context, task *ingestv1.IngestTask, userToken string) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var err error
	switch {
	case task.GetHarmonySubset() != nil:
		err = c.harmony(ctx, task)
	case task.GetGiovanniArea() != nil, task.GetGiovanniPoint() != nil:
		err = c.giovanniQuota(ctx, userToken)
	}
	var refusal *Refusal
	if err != nil && !errors.As(err, &refusal) {
		c.log.Warn(ctx, "preflight couldn't ask NASA, letting the job through", logging.Err(err))
		return nil
	}
	return err
}

func (c *Checker) harmony(ctx context.Context, task *ingestv1.IngestTask) error {
	h := task.GetHarmonySubset()
	ok, err := c.subsettable(ctx, h.GetCollectionId())
	if err != nil {
		return err
	}
	if !ok {
		return &Refusal{Msg: "NASA's Harmony service can't cut this dataset to a rectangle."}
	}
	hits, err := c.granules(ctx, h.GetCollectionId(), task.GetBbox(), task.GetDates())
	if err != nil {
		return err
	}
	if hits == 0 {
		return &Refusal{Msg: "There's no data for this place and time range."}
	}
	if limit := int(h.GetMaxGranules()); limit > 0 && hits > limit {
		return &Refusal{Msg: fmt.Sprintf("That would fetch %d files, more than the limit of %d. Pick a shorter time range.", hits, limit)}
	}
	return nil
}

// subsettable reports whether some Harmony service can subset the collection
// by area, time and variable. Answers are cached per collection.
func (c *Checker) subsettable(ctx context.Context, collection string) (bool, error) {
	c.mu.Lock()
	cached, ok := c.caps[collection]
	c.mu.Unlock()
	if ok && time.Since(cached.checked) < capsTTL {
		return cached.ok, nil
	}

	var caps struct {
		Services []struct {
			Capabilities struct {
				Subsetting struct {
					BBox     bool `json:"bbox"`
					Temporal bool `json:"temporal"`
					Variable bool `json:"variable"`
				} `json:"subsetting"`
			} `json:"capabilities"`
		} `json:"services"`
	}
	if err := c.getJSON(ctx, c.cfg.HarmonyURL+"/capabilities?collectionId="+url.QueryEscape(collection), "", &caps); err != nil {
		return false, err
	}
	result := false
	for _, s := range caps.Services {
		sub := s.Capabilities.Subsetting
		if sub.BBox && sub.Temporal && sub.Variable {
			result = true
			break
		}
	}
	c.mu.Lock()
	c.caps[collection] = capsResult{ok: result, checked: time.Now()}
	c.mu.Unlock()
	return result, nil
}

func (c *Checker) granules(ctx context.Context, collection string, b *commonv1.BBox, d *commonv1.DateRange) (int, error) {
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	q := url.Values{
		"collection_concept_id": {collection},
		"bounding_box":          {strings.Join([]string{num(b.GetWest()), num(b.GetSouth()), num(b.GetEast()), num(b.GetNorth())}, ",")},
		"temporal":              {isoDate(d.GetStart(), "00:00:00") + "," + isoDate(d.GetEnd(), "23:59:59")},
		"page_size":             {"0"},
	}
	resp, err := c.get(ctx, c.cfg.CMRURL+"/search/granules.json?"+q.Encode(), "")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return strconv.Atoi(resp.Header.Get("CMR-Hits"))
}

func (c *Checker) giovanniQuota(ctx context.Context, userToken string) error {
	token := userToken
	if token == "" {
		token = c.cfg.TeamToken
	}
	if token == "" {
		// Ingest reports the missing token with the right message.
		return nil
	}
	used, err := c.quotaUsed(ctx, token)
	if err != nil {
		return err
	}
	if used >= c.cfg.QuotaCutoff {
		return &Refusal{
			Msg:   fmt.Sprintf("%.0f%% of this month's Giovanni quota is used, so new Giovanni jobs are paused. Use another source, or add your own Earthdata token.", used*100),
			Quota: true,
		}
	}
	return nil
}

// GiovanniQuota returns the share of the team token's monthly Giovanni quota
// that's used, 0 to 1, and the share past which Giovanni jobs are refused.
func (c *Checker) GiovanniQuota(ctx context.Context) (used, cutoff float64, err error) {
	if c.cfg.TeamToken == "" {
		return 0, c.cfg.QuotaCutoff, errors.New("no EARTHDATA_TOKEN to read the Giovanni quota with")
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	used, err = c.quotaUsed(ctx, c.cfg.TeamToken)
	return used, c.cfg.QuotaCutoff, err
}

func (c *Checker) quotaUsed(ctx context.Context, token string) (float64, error) {
	var q struct {
		Quota float64 `json:"quota"`
	}
	if err := c.getJSON(ctx, c.cfg.GiovanniURL+"/quota", token, &q); err != nil {
		return 0, err
	}
	return q.Quota, nil
}

func (c *Checker) getJSON(ctx context.Context, u, token string, out any) error {
	resp, err := c.get(ctx, u, token)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// get returns the response for a 2xx status, and an error otherwise.
func (c *Checker) get(ctx context.Context, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s returned %d", strings.SplitN(u, "?", 2)[0], resp.StatusCode)
	}
	return resp, nil
}

func isoDate(d *commonv1.Date, clock string) string {
	return fmt.Sprintf("%04d-%02d-%02dT%sZ", d.GetYear(), d.GetMonth(), d.GetDay(), clock)
}
