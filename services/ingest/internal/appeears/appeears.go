// Package appeears is ingest's SOURCE_APPEEARS adapter. It asks NASA
// AppEEARS for one MODIS layer over the task's rectangle and dates, waits
// while AppEEARS queues and processes the request, and then keeps the
// statistics file AppEEARS writes for every area request: the mean over the
// rectangle for each date. That's stored as the same cleaned date,value CSV
// as a user upload, FORMAT_USER_CSV, which preprocess already reads.
//
// AppEEARS doesn't take Earthdata tokens. It logs in with an Earthdata
// username and password and hands out its own token, which is cached in
// Redis until it nears expiry.
package appeears

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/redis/go-redis/v9"
)

const (
	// Changes when the adapter starts writing different files for the same
	// task, which invalidates cached datasets.
	version     = "appeears/1"
	submitCheck = 15 * time.Second
	pollEvery   = 30 * time.Second
	stateTTL    = 48 * time.Hour
	tokenKey    = "td:ingest:appeears:token"
)

// Config holds the adapter's settings.
type Config struct {
	// For example https://appeears.earthdatacloud.nasa.gov/api.
	URL string
	// An Earthdata Login account. EARTHDATA_USERNAME and EARTHDATA_PASSWORD.
	Username, Password string
	// How long AppEEARS may queue and process a request before it's deleted
	// and the task fails. APPEEARS_MAX_WAIT; zero means no limit.
	MaxWait time.Duration
}

// Adapter implements core.Adapter for SOURCE_APPEEARS.
type Adapter struct {
	cfg  Config
	rdb  *redis.Client
	http *http.Client
	now  func() time.Time
}

// New returns the adapter.
func New(cfg Config, rdb *redis.Client) *Adapter {
	return &Adapter{cfg: cfg, rdb: rdb, http: &http.Client{Timeout: 2 * time.Minute}, now: time.Now}
}

// Source implements core.Adapter.
func (a *Adapter) Source() ingestv1.Source { return ingestv1.Source_SOURCE_APPEEARS }

// Fingerprint implements core.Adapter.
func (a *Adapter) Fingerprint(context.Context, *ingestv1.IngestTask) (string, error) {
	return version, nil
}

// request is a validated task.
type request struct {
	key            string
	product, layer string
	bbox           *commonv1.BBox
	start, end     *commonv1.Date
}

func newRequest(task *ingestv1.IngestTask) (*request, error) {
	s := task.GetAppeears()
	r := &request{product: s.GetProduct(), bbox: task.GetBbox(), start: task.GetDates().GetStart(), end: task.GetDates().GetEnd()}
	if len(s.GetLayers()) > 0 {
		r.layer = s.GetLayers()[0]
	}
	if r.product == "" || r.layer == "" || r.bbox == nil || r.start.GetYear() == 0 || r.end.GetYear() == 0 {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The job couldn't start.", "appeears task needs a product, a layer, a rectangle and dates")
	}
	var err error
	if r.key, err = core.CacheKey(task, version); err != nil {
		return nil, err
	}
	return r, nil
}

func stateKey(r *request) string { return "td:ingest:appeears:task:" + r.key }

// Fetch implements core.Adapter. It returns core.Pending until AppEEARS has
// finished the request.
func (a *Adapter) Fetch(ctx context.Context, task *ingestv1.IngestTask, sink core.Sink) (*core.Fetched, error) {
	req, err := newRequest(task)
	if err != nil {
		return nil, err
	}
	if a.cfg.Username == "" || a.cfg.Password == "" {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false,
			"NASA AppEEARS needs an Earthdata username and password, and the stack has none set.",
			"EARTHDATA_USERNAME or EARTHDATA_PASSWORD is empty")
	}
	key := stateKey(req)
	taskID, err := a.rdb.HGet(ctx, key, "task_id").Result()
	if errors.Is(err, redis.Nil) {
		return nil, a.submit(ctx, req, key)
	}
	if err != nil {
		return nil, core.Transient(err)
	}

	st, err := a.status(ctx, taskID)
	if errors.Is(err, errGone) {
		_ = a.rdb.Del(ctx, key).Err()
		return nil, core.Pending(time.Second, "AppEEARS task "+taskID+" is gone, resubmitting")
	}
	if err != nil {
		return nil, err
	}
	switch st.status {
	case "done":
		return a.collect(ctx, taskID, req, key, sink)
	case "error":
		_ = a.rdb.Del(ctx, key).Err()
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
			"NASA AppEEARS couldn't process this request. Try again, or pick a smaller rectangle.",
			"AppEEARS task %s ended with an error", taskID)
	}
	if waited := a.waited(ctx, key); a.cfg.MaxWait > 0 && waited > a.cfg.MaxWait {
		a.delete(ctx, taskID)
		_ = a.rdb.Del(ctx, key).Err()
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
			fmt.Sprintf("NASA AppEEARS didn't finish this within %s, so it was stopped. Try again later, or pick a shorter date range or a smaller rectangle.", inWords(a.cfg.MaxWait)),
			"AppEEARS task %s still %s after %s", taskID, st.status, waited.Round(time.Second))
	}
	core.ReportProgress(ctx, st.percent, st.activity())
	return nil, core.Pending(pollEvery, fmt.Sprintf("AppEEARS task %s is %s, %d%%", taskID, st.status, st.percent))
}

// Cancel stops the task's AppEEARS request, if it has one.
func (a *Adapter) Cancel(ctx context.Context, task *ingestv1.IngestTask) error {
	req, err := newRequest(task)
	if err != nil {
		return err
	}
	key := stateKey(req)
	taskID, err := a.rdb.HGet(ctx, key, "task_id").Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return core.Transient(err)
	}
	a.delete(ctx, taskID)
	return a.rdb.Del(ctx, key).Err()
}

func (a *Adapter) submit(ctx context.Context, r *request, key string) error {
	ring := [][2]float64{
		{r.bbox.GetWest(), r.bbox.GetSouth()}, {r.bbox.GetEast(), r.bbox.GetSouth()},
		{r.bbox.GetEast(), r.bbox.GetNorth()}, {r.bbox.GetWest(), r.bbox.GetNorth()},
		{r.bbox.GetWest(), r.bbox.GetSouth()},
	}
	body := map[string]any{
		"task_type": "area",
		"task_name": "trend-detective " + r.key[:min(12, len(r.key))],
		"params": map[string]any{
			"dates":  []map[string]string{{"startDate": usDate(r.start), "endDate": usDate(r.end)}},
			"layers": []map[string]string{{"product": r.product, "layer": r.layer}},
			"geo": map[string]any{
				"type": "FeatureCollection",
				"features": []map[string]any{{
					"type": "Feature", "properties": map[string]any{},
					"geometry": map[string]any{"type": "Polygon", "coordinates": [][][2]float64{ring}},
				}},
			},
			"output": map[string]any{"format": map[string]string{"type": "netcdf4"}, "projection": "geographic"},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false, "The job couldn't start.", "encode AppEEARS task: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost, "/task", b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"NASA AppEEARS refused this request.", "AppEEARS 400: %s", msg)
	}
	if err := statusError(resp); err != nil {
		return err
	}
	var t struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil || t.TaskID == "" {
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA AppEEARS returned something unexpected.", "decode AppEEARS task: %v", err)
	}
	if err := a.rdb.HSet(ctx, key, "task_id", t.TaskID, "submitted_at", a.now().Unix()).Err(); err != nil {
		return core.Transient(err)
	}
	_ = a.rdb.Expire(ctx, key, stateTTL).Err()
	core.ReportProgress(ctx, core.Unknown, "Sent the request to NASA AppEEARS")
	return core.Pending(submitCheck, "submitted AppEEARS task "+t.TaskID)
}

// errGone means AppEEARS no longer knows the task, for example because it
// expired. The adapter submits it again.
var errGone = errors.New("AppEEARS task is gone")

type taskStatus struct {
	status string
	// Overall percent, or core.Unknown.
	percent int
	// The step AppEEARS is on, such as "Subsetting".
	step string
}

func (s taskStatus) activity() string {
	switch {
	case s.status == "pending" || s.status == "queued":
		return "Waiting in line at NASA AppEEARS, which takes requests in turn"
	case s.step != "":
		return "NASA AppEEARS is processing the images: " + strings.ToLower(s.step)
	}
	return "NASA AppEEARS is processing the images"
}

// status asks how far a task has got. /status/{id} has the progress while a
// task runs; /task/{id} has the final state.
func (a *Adapter) status(ctx context.Context, taskID string) (taskStatus, error) {
	st := taskStatus{percent: core.Unknown}
	resp, err := a.do(ctx, http.MethodGet, "/task/"+taskID, nil)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return st, errGone
	}
	if err := statusError(resp); err != nil {
		return st, err
	}
	var t struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return st, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA AppEEARS returned something unexpected.", "decode AppEEARS task status: %v", err)
	}
	st.status = t.Status
	if st.status != "processing" {
		return st, nil
	}

	// Progress is a nicety; without it the task just shows as working.
	pr, err := a.do(ctx, http.MethodGet, "/status/"+taskID, nil)
	if err != nil || pr.StatusCode != http.StatusOK {
		if pr != nil {
			_ = pr.Body.Close()
		}
		return st, nil
	}
	defer pr.Body.Close()
	var p struct {
		Progress struct {
			Summary *float64 `json:"summary"`
			Details []struct {
				Desc string  `json:"desc"`
				Pct  float64 `json:"pct_complete"`
			} `json:"details"`
		} `json:"progress"`
	}
	if json.NewDecoder(pr.Body).Decode(&p) != nil {
		return st, nil
	}
	details := p.Progress.Details
	if p.Progress.Summary != nil {
		st.percent = int(*p.Progress.Summary)
	} else if len(details) > 0 {
		sum := 0.0
		for _, d := range details {
			sum += d.Pct
		}
		st.percent = int(sum / float64(len(details)))
	}
	for _, d := range details {
		if d.Pct < 100 {
			st.step = d.Desc
			break
		}
	}
	return st, nil
}

// collect downloads the statistics file of a finished task and stores it as
// a cleaned date,value CSV.
func (a *Adapter) collect(ctx context.Context, taskID string, r *request, key string, sink core.Sink) (*core.Fetched, error) {
	core.ReportProgress(ctx, core.Unknown, "Downloading the averages from NASA AppEEARS")
	fileID, err := a.statisticsFile(ctx, taskID)
	if err != nil {
		return nil, err
	}
	resp, err := a.do(ctx, http.MethodGet, "/bundle/"+taskID+"/"+fileID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"Couldn't download the data from NASA AppEEARS.", "read AppEEARS statistics: %w", err)
	}
	rows, err := parseStatistics(data, r.layer)
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
			"NASA AppEEARS returned a file we couldn't read.", "AppEEARS statistics: %w", err)
	}
	if !slices.ContainsFunc(rows, func(s stat) bool { return s.ok }) {
		_ = a.rdb.Del(ctx, key).Err()
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
			"There's no data for this place and time range. MODIS land layers have nothing over water.",
			"AppEEARS task %s: no valid means", taskID)
	}
	out := writeCSV(rows)
	err = sink.Put(ctx, core.File{
		Name:        "appeears.csv",
		Body:        bytes.NewReader(out),
		Size:        int64(len(out)),
		ContentType: "text/csv",
		Coverage: &commonv1.DateRange{
			Start: dateOf(rows[0].date),
			End:   dateOf(rows[len(rows)-1].date),
		},
	})
	if err != nil {
		return nil, err
	}
	_ = a.rdb.Del(ctx, key).Err()
	return &core.Fetched{
		Source:    ingestv1.Source_SOURCE_APPEEARS,
		Format:    ingestv1.Format_FORMAT_USER_CSV,
		Variables: []string{r.product + " " + r.layer},
		Step:      stepOf(rows),
	}, nil
}

// statisticsFile finds the task's per-date statistics among its files.
func (a *Adapter) statisticsFile(ctx context.Context, taskID string) (string, error) {
	resp, err := a.do(ctx, http.MethodGet, "/bundle/"+taskID, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return "", err
	}
	var b struct {
		Files []struct {
			ID   string `json:"file_id"`
			Name string `json:"file_name"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA AppEEARS returned something unexpected.", "decode AppEEARS bundle: %v", err)
	}
	for _, f := range b.Files {
		if strings.HasSuffix(strings.ToLower(f.Name), "-statistics.csv") {
			return f.ID, nil
		}
	}
	return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
		"There's no data for this place and time range.", "AppEEARS task %s has no statistics file", taskID)
}

type stat struct {
	date  time.Time
	value float64
	ok    bool
}

// parseStatistics reads the Date and Mean columns of an AppEEARS statistics
// file, for the given layer when the file names one per row. Values are
// already scaled. A date with no valid pixels has no mean, or a count of zero.
func parseStatistics(data []byte, layer string) ([]stat, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	dateCol, okDate := col["date"]
	meanCol, okMean := col["mean"]
	countCol, okCount := col["count"]
	datasetCol, okDataset := col["dataset"]
	if !okDate || !okMean {
		return nil, fmt.Errorf("no Date and Mean columns in %v", header)
	}
	byDate := map[time.Time]stat{}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) <= max(dateCol, meanCol) {
			continue
		}
		if okDataset && len(rec) > datasetCol && layer != "" && strings.TrimSpace(rec[datasetCol]) != layer {
			continue
		}
		d, err := time.Parse("2006-01-02", strings.TrimSpace(rec[dateCol]))
		if err != nil {
			continue
		}
		s := stat{date: d}
		v, err := strconv.ParseFloat(strings.TrimSpace(rec[meanCol]), 64)
		s.ok = err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
		if okCount && len(rec) > countCol {
			if n, err := strconv.ParseFloat(strings.TrimSpace(rec[countCol]), 64); err == nil && n == 0 {
				s.ok = false
			}
		}
		if s.ok {
			s.value = v
		}
		byDate[d] = s
	}
	rows := make([]stat, 0, len(byDate))
	for _, s := range byDate {
		rows = append(rows, s)
	}
	slices.SortFunc(rows, func(a, b stat) int { return a.date.Compare(b.date) })
	if len(rows) == 0 {
		return nil, errors.New("no dated rows")
	}
	return rows, nil
}

// writeCSV renders the same cleaned format as a user upload.
func writeCSV(rows []stat) []byte {
	var b bytes.Buffer
	b.WriteString("date,value\n")
	for _, s := range rows {
		b.WriteString(s.date.Format("2006-01-02"))
		b.WriteByte(',')
		if s.ok {
			b.WriteString(strconv.FormatFloat(s.value, 'g', -1, 64))
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// stepOf tells the data's time step from the usual gap between dates: MODIS
// monthly products are monthly, 8- and 16-day composites count as weekly.
func stepOf(rows []stat) commonv1.TimeStep {
	if len(rows) < 2 {
		return commonv1.TimeStep_TIME_STEP_UNSPECIFIED
	}
	gaps := make([]float64, 0, len(rows)-1)
	for i := 1; i < len(rows); i++ {
		gaps = append(gaps, rows[i].date.Sub(rows[i-1].date).Hours()/24)
	}
	slices.Sort(gaps)
	switch median := gaps[len(gaps)/2]; {
	case median <= 1:
		return commonv1.TimeStep_TIME_STEP_DAILY
	case median >= 28:
		return commonv1.TimeStep_TIME_STEP_MONTHLY
	default:
		return commonv1.TimeStep_TIME_STEP_WEEKLY
	}
}

// waited is how long ago the task was submitted.
func (a *Adapter) waited(ctx context.Context, key string) time.Duration {
	now := a.now()
	_ = a.rdb.HSetNX(ctx, key, "submitted_at", now.Unix()).Err()
	at, err := a.rdb.HGet(ctx, key, "submitted_at").Int64()
	if err != nil {
		return 0
	}
	return now.Sub(time.Unix(at, 0))
}

// delete removes a task at AppEEARS. Best effort.
func (a *Adapter) delete(ctx context.Context, taskID string) {
	if resp, err := a.do(ctx, http.MethodDelete, "/task/"+taskID, nil); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// do sends an authenticated request. A rejected token is dropped and the
// request tried once more with a fresh login.
func (a *Adapter) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		token, err := a.token(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, a.cfg.URL+path, bytes.NewReader(body))
		if err != nil {
			return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false,
				"Something went wrong while fetching the data.", "build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := a.http.Do(req)
		if err != nil {
			return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
				"Couldn't reach NASA AppEEARS.", "%s %s: %w", method, path, err)
		}
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && attempt == 0 {
			_ = resp.Body.Close()
			_ = a.rdb.Del(ctx, tokenKey).Err()
			continue
		}
		return resp, nil
	}
}

// token returns a cached AppEEARS token, or logs in for a new one.
func (a *Adapter) token(ctx context.Context) (string, error) {
	if t, err := a.rdb.Get(ctx, tokenKey).Result(); err == nil && t != "" {
		return t, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.URL+"/login", nil)
	if err != nil {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false,
			"Something went wrong while fetching the data.", "build login: %w", err)
	}
	req.SetBasicAuth(a.cfg.Username, a.cfg.Password)
	resp, err := a.http.Do(req)
	if err != nil {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"Couldn't reach NASA AppEEARS.", "AppEEARS login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false,
			"NASA AppEEARS refused the Earthdata username and password. Check EARTHDATA_USERNAME and EARTHDATA_PASSWORD.",
			"AppEEARS login returned %d", resp.StatusCode)
	}
	if err := statusError(resp); err != nil {
		return "", err
	}
	var l struct {
		Token      string    `json:"token"`
		Expiration time.Time `json:"expiration"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil || l.Token == "" {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA AppEEARS returned something unexpected.", "decode AppEEARS login: %v", err)
	}
	// Keep it until an hour before it expires, and never more than a day.
	ttl := min(l.Expiration.Sub(a.now())-time.Hour, 24*time.Hour)
	if ttl > time.Minute {
		_ = a.rdb.Set(ctx, tokenKey, l.Token, ttl).Err()
	}
	return l.Token, nil
}

// statusError maps a response status to a core error, or nil for 2xx.
func statusError(resp *http.Response) error {
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return nil
	case c == http.StatusUnauthorized || c == http.StatusForbidden:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false,
			"NASA AppEEARS refused our login.", "AppEEARS returned %d", c)
	case c == http.StatusTooManyRequests:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, true,
			"NASA AppEEARS is rate-limiting us. Try again in a few minutes.", "AppEEARS returned 429")
	case c >= 500:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA AppEEARS had a problem.", "AppEEARS returned %d", c)
	}
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
		"NASA AppEEARS returned something unexpected.", "AppEEARS returned %d", resp.StatusCode)
}

// usDate is AppEEARS's date format, MM-DD-YYYY.
func usDate(d *commonv1.Date) string {
	return fmt.Sprintf("%02d-%02d-%04d", d.GetMonth(), d.GetDay(), d.GetYear())
}

func dateOf(t time.Time) *commonv1.Date {
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}

// inWords says a duration for people: "2 hours", "90 minutes", "1 minute".
func inWords(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return plural(int(d.Hours()), "hour")
	case d >= time.Minute:
		return plural(int(d.Round(time.Minute).Minutes()), "minute")
	}
	return plural(int(d.Round(time.Second).Seconds()), "second")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
