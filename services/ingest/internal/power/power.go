// Package power is the power adapter. It fetches NASA POWER monthly means for
// one point: a single JSON file, with no login, usually in a second or two.
//
// The monthly endpoint takes whole years, so a task for March 2001 to June
// 2003 gets 2001 through 2003, and preprocess trims it to the task's dates.
// Every complete year also has a 13th "month" with the annual mean, and
// missing values are -999. Preprocess handles both.
package power

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
)

// version changes when the adapter starts writing different files for the
// same task, which invalidates cached datasets.
const version = "power/1"

// maxBody caps the response. Forty years of a few parameters is well under
// 100 KB, so anything near this is a mistake.
const maxBody = 10 << 20

// Adapter implements core.Adapter for SOURCE_POWER.
type Adapter struct {
	base string
	http *http.Client
}

// New returns an Adapter for a POWER server, like
// https://power.larc.nasa.gov.
func New(base string) *Adapter {
	return &Adapter{base: strings.TrimSuffix(base, "/"), http: &http.Client{Timeout: 2 * time.Minute}}
}

// Source implements core.Adapter.
func (a *Adapter) Source() ingestv1.Source { return ingestv1.Source_SOURCE_POWER }

// Fingerprint implements core.Adapter.
func (a *Adapter) Fingerprint(context.Context, *ingestv1.IngestTask) (string, error) {
	return version, nil
}

// Fetch implements core.Adapter.
func (a *Adapter) Fetch(ctx context.Context, task *ingestv1.IngestTask, sink core.Sink) (*core.Fetched, error) {
	p := task.GetPower()
	start, end := task.GetDates().GetStart().GetYear(), task.GetDates().GetEnd().GetYear()
	if len(p.GetParameters()) == 0 || p.GetPoint() == nil || start == 0 || end < start {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The job couldn't start.", "power task needs parameters, a point and dates")
	}
	community := p.GetCommunity()
	if community == "" {
		community = "AG"
	}
	q := url.Values{
		"parameters": {strings.Join(p.GetParameters(), ",")},
		"community":  {community},
		"latitude":   {strconv.FormatFloat(p.GetPoint().GetLat(), 'f', -1, 64)},
		"longitude":  {strconv.FormatFloat(p.GetPoint().GetLon(), 'f', -1, 64)},
		"start":      {strconv.Itoa(int(start))},
		"end":        {strconv.Itoa(int(end))},
		"format":     {"JSON"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/api/temporal/monthly/point?"+q.Encode(), nil)
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false,
			"Something went wrong while fetching the data.", "build POWER request: %w", err)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"Couldn't reach NASA POWER.", "POWER request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"Couldn't reach NASA POWER.", "read POWER response: %w", err)
	}
	if err := statusError(resp.StatusCode, body); err != nil {
		return nil, err
	}
	if len(body) > maxBody || !json.Valid(body) {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA POWER returned something unexpected.", "POWER response isn't JSON, or is over %d bytes", maxBody)
	}

	err = sink.Put(ctx, core.File{
		Name:        "power.json",
		Body:        bytes.NewReader(body),
		Size:        int64(len(body)),
		ContentType: "application/json",
		Coverage: &commonv1.DateRange{
			Start: &commonv1.Date{Year: start, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: end, Month: 12, Day: 31},
		},
	})
	if err != nil {
		return nil, err
	}
	return &core.Fetched{
		Source:    ingestv1.Source_SOURCE_POWER,
		Format:    ingestv1.Format_FORMAT_POWER_JSON,
		Variables: p.GetParameters(),
		Point:     p.GetPoint(),
	}, nil
}

// statusError maps a POWER response status to a core error, or nil for 200.
// POWER explains a 422 in plain English, so that text goes to the user.
func statusError(code int, body []byte) error {
	switch {
	case code == http.StatusOK:
		return nil
	case code == http.StatusUnprocessableEntity:
		var e struct {
			Messages []string `json:"messages"`
		}
		_ = json.Unmarshal(body, &e)
		msg := "NASA POWER refused this request."
		if len(e.Messages) > 0 {
			msg = "NASA POWER refused this request: " + e.Messages[0]
		}
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false, msg, "POWER 422: %s", strings.Join(e.Messages, "; "))
	case code == http.StatusTooManyRequests:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, true,
			"NASA POWER is rate-limiting us. Try again in a few minutes.", "POWER returned 429")
	case code >= 500:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA POWER had a problem.", "POWER returned %d", code)
	}
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
		"NASA POWER refused this request.", "POWER returned %d: %s", code, truncate(body))
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return fmt.Sprintf("%s...", b[:300])
	}
	return string(b)
}
