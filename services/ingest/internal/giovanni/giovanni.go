// Package giovanni is the giovanni-point adapter. It asks Giovanni's
// time-series API for one variable's values at one point and stores the CSV
// that comes back. The API answers right away, in a few seconds even for
// twenty years of daily data, so unlike Harmony there's no job to poll.
//
// Giovanni answers for the grid cell the point falls in. The CSV header gives
// that cell's center, and the dataset records it as its point.
//
// Giovanni wraps most errors in a 403 and puts the real code in a JSON body,
// so the adapter reads the body to tell a bad token from a bad variable.
package giovanni

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/earthdata"
)

// version changes when the adapter starts writing different files for the
// same task, which invalidates cached datasets.
const version = "giovanni-point/1"

// maxBody caps the response. Twenty years of daily values is about 220 KB,
// so anything near this is a mistake.
const maxBody = 50 << 20

// errLogin means Earthdata sent us to its login page.
var errLogin = errors.New("redirected to the Earthdata login page")

// Adapter implements core.Adapter for SOURCE_GIOVANNI_POINT.
type Adapter struct {
	base   string
	tokens *earthdata.Tokens
	http   *http.Client
}

// New returns an Adapter for a Giovanni API server, like
// https://api.giovanni.earthdata.nasa.gov.
func New(base string, tokens *earthdata.Tokens) *Adapter {
	return &Adapter{
		base:   strings.TrimSuffix(base, "/"),
		tokens: tokens,
		http: &http.Client{
			Timeout: 3 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if strings.HasSuffix(req.URL.Hostname(), "urs.earthdata.nasa.gov") {
					return errLogin
				}
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

// Source implements core.Adapter.
func (a *Adapter) Source() ingestv1.Source { return ingestv1.Source_SOURCE_GIOVANNI_POINT }

// Fingerprint implements core.Adapter.
func (a *Adapter) Fingerprint(context.Context, *ingestv1.IngestTask) (string, error) {
	return version, nil
}

// Fetch implements core.Adapter.
func (a *Adapter) Fetch(ctx context.Context, task *ingestv1.IngestTask, sink core.Sink) (*core.Fetched, error) {
	g := task.GetGiovanniPoint()
	start, end := task.GetDates().GetStart(), task.GetDates().GetEnd()
	if g.GetDataFieldId() == "" || g.GetPoint() == nil || start == nil || end == nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The job couldn't start.", "giovanni-point task needs a data field ID, a point and dates")
	}
	token, err := a.tokens.For(ctx, task)
	if err != nil {
		return nil, err
	}

	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	q := url.Values{
		"data":     {g.GetDataFieldId()},
		"location": {"[" + num(g.GetPoint().GetLat()) + "," + num(g.GetPoint().GetLon()) + "]"},
		"time":     {isoDate(start, "00:00:00") + "/" + isoDate(end, "23:59:59")},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/timeseries?"+q.Encode(), nil)
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false,
			"Something went wrong while fetching the data.", "build Giovanni request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.http.Do(req)
	if errors.Is(err, errLogin) {
		return nil, badToken("Giovanni redirected to the login page")
	}
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"Couldn't reach NASA Giovanni.", "Giovanni request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"Couldn't reach NASA Giovanni.", "read Giovanni response: %w", err)
	}
	if err := statusError(resp.StatusCode, body, g.GetDataFieldId()); err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
			"NASA Giovanni returned more data than expected.", "Giovanni response is over %d bytes", maxBody)
	}

	s, err := parse(body)
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA Giovanni returned something unexpected.", "Giovanni point CSV: %w", err)
	}
	if s.valid == 0 {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
			"There's no data for this variable at this point. Land variables have none over the sea, and ocean variables none on land or close to the coast.",
			"Giovanni returned %d time steps for %s, all missing", s.steps, g.GetDataFieldId())
	}
	cell := s.cell
	if cell == nil {
		cell = g.GetPoint()
	}

	err = sink.Put(ctx, core.File{
		Name:        "point.csv",
		Body:        bytes.NewReader(body),
		Size:        int64(len(body)),
		ContentType: "text/csv",
		Coverage:    task.GetDates(),
	})
	if err != nil {
		return nil, err
	}
	return &core.Fetched{
		Source:    ingestv1.Source_SOURCE_GIOVANNI_POINT,
		Format:    ingestv1.Format_FORMAT_GIOVANNI_POINT_CSV,
		Variables: []string{g.GetDataFieldId()},
		Point:     cell,
	}, nil
}

// apiError is the JSON body Giovanni sends with most errors.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Context string `json:"context"`
}

// statusError maps a Giovanni response to a core error, or nil for 200.
func statusError(status int, body []byte, dataField string) error {
	if status == http.StatusOK {
		return nil
	}
	var e apiError
	_ = json.Unmarshal(body, &e)
	code := status
	if n, err := strconv.Atoi(e.Code); err == nil {
		code = n
	}
	detail := strings.TrimSpace(e.Message + ": " + e.Context)
	if e.Code == "" {
		detail = truncate(body)
	}
	switch {
	// Checked first: how Giovanni reports a used-up quota hasn't been seen.
	case code == http.StatusTooManyRequests || strings.Contains(strings.ToLower(detail), "quota"):
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, true,
			"The Giovanni quota or rate limit ran out. Try again later, or add your own Earthdata token.",
			"Giovanni %d: %s", status, detail)
	case code == http.StatusUnauthorized:
		return badToken(detail)
	case code == http.StatusBadRequest:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"NASA Giovanni doesn't recognize this variable.", "Giovanni %d for %s: %s", status, dataField, detail)
	case code == http.StatusNotFound:
		// Giovanni says so in plain text, naming the variable and dates.
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
			"NASA Giovanni has no data for this variable in these dates.", "Giovanni %d: %s", status, detail)
	case code >= 500:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA Giovanni had a problem.", "Giovanni %d: %s", status, detail)
	}
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
		"NASA Giovanni refused this request.", "Giovanni %d: %s", status, detail)
}

func badToken(detail string) error {
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false,
		"NASA Giovanni didn't accept the Earthdata token. Check that it hasn't expired.", "Giovanni: %s", detail)
}

// series is what parse found in a point CSV.
type series struct {
	// The center of the grid cell Giovanni answered for, or nil if the
	// header doesn't say.
	cell *commonv1.LatLon
	// Time steps, and how many of them hold a value.
	steps, valid int
}

// parse checks a Giovanni point CSV: "key,value" header lines, a blank line,
// then "Timestamp (UTC),Data" and one row per time step. Missing steps hold
// the header's undef value.
func parse(body []byte) (series, error) {
	var s series
	header := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	inData := false
	undef := math.NaN()
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !inData {
			if strings.HasPrefix(line, "Timestamp") {
				inData = true
				if v, err := strconv.ParseFloat(header["undef"], 64); err == nil {
					undef = v
				}
				continue
			}
			if k, v, ok := strings.Cut(line, ","); ok {
				header[k] = v
			}
			continue
		}
		if line == "" {
			continue
		}
		_, value, ok := strings.Cut(line, ",")
		if !ok {
			return s, fmt.Errorf("row %q has no value column", line)
		}
		s.steps++
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil && !math.IsNaN(v) && v != undef {
			s.valid++
		}
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	if !inData {
		return s, errors.New(`no "Timestamp (UTC),Data" line`)
	}
	lat, errLat := strconv.ParseFloat(header["lat"], 64)
	lon, errLon := strconv.ParseFloat(header["lon"], 64)
	if errLat == nil && errLon == nil {
		s.cell = &commonv1.LatLon{Lat: lat, Lon: lon}
	}
	return s, nil
}

func isoDate(d *commonv1.Date, clock string) string {
	return fmt.Sprintf("%04d-%02d-%02dT%s", d.GetYear(), d.GetMonth(), d.GetDay(), clock)
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return fmt.Sprintf("%s...", b[:300])
	}
	return string(b)
}
