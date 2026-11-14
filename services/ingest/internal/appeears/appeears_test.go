package appeears

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// The layout AppEEARS writes, trimmed to the columns that matter here. March
// has no valid pixels.
const statistics = `File Name,Dataset,aid,Date,Count,Minimum,Maximum,Range,Mean,Standard Deviation
MOD13A3_061__1_km_monthly_NDVI_doy2019001000000_aid0001,_1_km_monthly_NDVI,aid0001,2019-01-01,2304.0,0.09,0.79,"(0.09,0.79)",0.3185227,0.11
MOD13A3_061__1_km_monthly_NDVI_doy2019032000000_aid0001,_1_km_monthly_NDVI,aid0001,2019-02-01,2304.0,0.10,0.77,"(0.10,0.77)",0.3431596,0.12
MOD13A3_061__1_km_monthly_NDVI_doy2019060000000_aid0001,_1_km_monthly_NDVI,aid0001,2019-03-01,0.0,,,,,
`

type fakeAppeears struct {
	mu       sync.Mutex
	srv      *httptest.Server
	statuses []string // successive task statuses; the last one repeats
	polls    int
	logins   int
	submits  int
	deletes  int
	body     map[string]any
	stats    string
}

func newFake(t *testing.T) *fakeAppeears {
	f := &fakeAppeears{statuses: []string{"queued", "processing", "done"}, stats: statistics}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAppeears) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/login" {
		if u, p, ok := r.BasicAuth(); !ok || u != "ed-user" || p != "ed-pass" {
			http.Error(w, `{"message": "bad credentials"}`, http.StatusUnauthorized)
			return
		}
		f.logins++
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok-1", "expiration": time.Now().Add(48 * time.Hour)})
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok-1" {
		http.Error(w, `{"message": "forbidden"}`, http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/task":
		f.submits++
		_ = json.NewDecoder(r.Body).Decode(&f.body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id": "t-1", "status": "pending"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/task/t-1":
		status := f.statuses[min(f.polls, len(f.statuses)-1)]
		f.polls++
		_ = json.NewEncoder(w).Encode(map[string]string{"task_id": "t-1", "status": status})
	case r.Method == http.MethodDelete && r.URL.Path == "/task/t-1":
		f.deletes++
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/status/t-1":
		_, _ = w.Write([]byte(`{"progress": {"details": [{"desc": "Initializing", "pct_complete": 100}, {"desc": "Subsetting", "pct_complete": 40}]}}`))
	case r.URL.Path == "/bundle/t-1":
		_, _ = w.Write([]byte(`{"files": [
			{"file_id": "f-nc", "file_name": "MOD13A3.061_1km_aid0001.nc"},
			{"file_id": "f-qa", "file_name": "MOD13A3-061-1-km-monthly-VI-Quality-Statistics-QA.csv"},
			{"file_id": "f-stats", "file_name": "MOD13A3-061-Statistics.csv"}]}`))
	case r.URL.Path == "/bundle/t-1/f-stats":
		_, _ = w.Write([]byte(f.stats))
	default:
		http.NotFound(w, r)
	}
}

type sink struct {
	files map[string]core.File
	data  map[string]string
}

func (s *sink) Put(_ context.Context, f core.File) error {
	b, err := io.ReadAll(f.Body)
	if err != nil {
		return err
	}
	s.files[f.Name], s.data[f.Name] = f, string(b)
	return nil
}

func newSink() *sink { return &sink{files: map[string]core.File{}, data: map[string]string{}} }

type harness struct {
	fake *fakeAppeears
	mr   *miniredis.Miniredis
	a    *Adapter
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	fake := newFake(t)
	mr := miniredis.RunT(t)
	cfg.URL = fake.srv.URL
	if cfg.Username == "" {
		cfg.Username, cfg.Password = "ed-user", "ed-pass"
	}
	return &harness{fake: fake, mr: mr, a: New(cfg, redis.NewClient(&redis.Options{Addr: mr.Addr()}))}
}

func task() *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		VariableId: "vegetation-modis",
		Bbox:       &commonv1.BBox{West: 77, South: 28.4, East: 77.4, North: 28.8},
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 2019, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 2020, Month: 12, Day: 31},
		},
		Adapter: &ingestv1.IngestTask_Appeears{Appeears: &ingestv1.Appeears{Product: "MOD13A3.061", Layers: []string{"_1_km_monthly_NDVI"}}},
	}
}

func wantPending(t *testing.T, err error) {
	t.Helper()
	if _, ok := core.AsPending(err); !ok {
		t.Fatalf("want pending, got %v", err)
	}
}

func wantCode(t *testing.T, err error, code gatewayv1.ErrorCode) {
	t.Helper()
	if e := core.AsError(err); e == nil || e.Code != code {
		t.Fatalf("want %v, got %v", code, err)
	}
}

func TestSubmitWaitAndCollect(t *testing.T) {
	h := newHarness(t, Config{})
	var reports []string
	ctx := core.WithProgress(context.Background(), func(p int, activity string) {
		reports = append(reports, fmt.Sprintf("%d %s", p, activity))
	})
	s := newSink()

	_, err := h.a.Fetch(ctx, task(), s) // submit
	wantPending(t, err)
	params, _ := h.fake.body["params"].(map[string]any)
	got, _ := json.Marshal(params)
	for _, want := range []string{`"startDate":"01-01-2019"`, `"endDate":"12-31-2020"`, `"layer":"_1_km_monthly_NDVI"`, `"product":"MOD13A3.061"`, `[[77,28.4],[77.4,28.4],[77.4,28.8],[77,28.8],[77,28.4]]`} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("request params %s lack %s", got, want)
		}
	}
	if h.fake.body["task_type"] != "area" {
		t.Errorf("task type: %v", h.fake.body["task_type"])
	}

	_, err = h.a.Fetch(ctx, task(), s) // queued
	wantPending(t, err)
	_, err = h.a.Fetch(ctx, task(), s) // processing
	wantPending(t, err)
	fetched, err := h.a.Fetch(ctx, task(), s) // done
	if err != nil {
		t.Fatal(err)
	}

	if fetched.Format != ingestv1.Format_FORMAT_USER_CSV || fetched.Source != ingestv1.Source_SOURCE_APPEEARS || fetched.Step != commonv1.TimeStep_TIME_STEP_MONTHLY {
		t.Errorf("fetched: %+v", fetched)
	}
	if want := "date,value\n2019-01-01,0.3185227\n2019-02-01,0.3431596\n2019-03-01,\n"; s.data["appeears.csv"] != want {
		t.Errorf("csv:\n%s\nwant:\n%s", s.data["appeears.csv"], want)
	}
	if c := s.files["appeears.csv"].Coverage; c.GetStart().GetMonth() != 1 || c.GetEnd().GetMonth() != 3 {
		t.Errorf("coverage: %v", c)
	}
	wantReports := []string{
		"-1 Sent the request to NASA AppEEARS",
		"-1 Waiting in line at NASA AppEEARS, which takes requests in turn",
		"70 NASA AppEEARS is processing the images: subsetting",
		"-1 Downloading the averages from NASA AppEEARS",
	}
	if strings.Join(reports, "|") != strings.Join(wantReports, "|") {
		t.Errorf("reports:\n%q\nwant:\n%q", reports, wantReports)
	}
	if h.fake.logins != 1 {
		t.Errorf("logged in %d times, want once and then the cached token", h.fake.logins)
	}
	if keys := h.mr.Keys(); len(keys) != 1 || keys[0] != tokenKey {
		t.Errorf("Redis keys left: %v, want only the token", keys)
	}
}

func TestStaleTokenLogsInAgain(t *testing.T) {
	h := newHarness(t, Config{})
	h.mr.Set(tokenKey, "expired")
	_, err := h.a.Fetch(context.Background(), task(), newSink())
	wantPending(t, err)
	if h.fake.logins != 1 || h.fake.submits != 1 {
		t.Errorf("logins %d, submits %d", h.fake.logins, h.fake.submits)
	}
}

func TestBadCredentials(t *testing.T) {
	h := newHarness(t, Config{Username: "ed-user", Password: "wrong"})
	_, err := h.a.Fetch(context.Background(), task(), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	if !strings.Contains(core.AsError(err).UserMessage, "EARTHDATA_USERNAME") {
		t.Errorf("message: %q", core.AsError(err).UserMessage)
	}
}

func TestNoCredentials(t *testing.T) {
	fake := newFake(t)
	a := New(Config{URL: fake.srv.URL}, redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()}))
	_, err := a.Fetch(context.Background(), task(), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	if fake.logins != 0 {
		t.Error("tried to log in without credentials")
	}
}

func TestNothingValidIsNoData(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.statuses = []string{"done"}
	h.fake.stats = "File Name,Dataset,aid,Date,Count,Mean\nx,_1_km_monthly_NDVI,aid0001,2019-01-01,0.0,\n"
	ctx := context.Background()
	_, _ = h.a.Fetch(ctx, task(), newSink())
	_, err := h.a.Fetch(ctx, task(), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_NO_DATA)
}

func TestErrorStatus(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.statuses = []string{"error"}
	ctx := context.Background()
	_, _ = h.a.Fetch(ctx, task(), newSink())
	_, err := h.a.Fetch(ctx, task(), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM)
}

func TestTimeLimitDeletesTheTask(t *testing.T) {
	h := newHarness(t, Config{MaxWait: time.Hour})
	h.fake.statuses = []string{"queued"}
	start := time.Date(2026, 11, 14, 21, 0, 0, 0, time.UTC)
	ctx := context.Background()
	h.a.now = func() time.Time { return start }
	_, _ = h.a.Fetch(ctx, task(), newSink())
	h.a.now = func() time.Time { return start.Add(61 * time.Minute) }
	_, err := h.a.Fetch(ctx, task(), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM)
	if !strings.Contains(core.AsError(err).UserMessage, "1 hour") || h.fake.deletes != 1 {
		t.Errorf("message %q, deletes %d", core.AsError(err).UserMessage, h.fake.deletes)
	}
}

func TestCancelDeletesTheTask(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := context.Background()
	if err := h.a.Cancel(ctx, task()); err != nil || h.fake.deletes != 0 {
		t.Fatalf("cancel with no task: %v, %d deletes", err, h.fake.deletes)
	}
	_, _ = h.a.Fetch(ctx, task(), newSink())
	if err := h.a.Cancel(ctx, task()); err != nil || h.fake.deletes != 1 {
		t.Errorf("cancel: %v, %d deletes", err, h.fake.deletes)
	}
}

func TestStepOf(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	eightDay := []stat{{date: day("2020-01-01")}, {date: day("2020-01-09")}, {date: day("2020-01-17")}}
	if got := stepOf(eightDay); got != commonv1.TimeStep_TIME_STEP_WEEKLY {
		t.Errorf("8-day composites: %v", got)
	}
}
