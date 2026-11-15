package harmony

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/earthdata"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeNASA plays CMR, Harmony and Harmony's file server.
type fakeNASA struct {
	mu        sync.Mutex
	srv       *httptest.Server
	hits      int
	token     string
	statuses  []string // returned by successive job polls; the last one repeats
	polls     int
	submits   int
	lastQuery url.Values
	lastAuth  string
	fail5xx   bool
	cmrCalls  int
	eula      bool
	login     bool
	// When set, a failed job reports this error message.
	failWith string
	cancels  int
	// Granules CMR lists, and the form of the last POSTed submit.
	granuleIDs []string
	lastMethod string
	lastForm   url.Values
}

func newFakeNASA(t *testing.T) *fakeNASA {
	f := &fakeNASA{hits: 2, token: "team-token", statuses: []string{"running", "successful"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNASA) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastAuth = r.Header.Get("Authorization")
	if f.login {
		http.Redirect(w, r, "https://urs.earthdata.nasa.gov/oauth/authorize?client_id=x", http.StatusSeeOther)
		return
	}
	if f.fail5xx {
		http.Error(w, "boom", http.StatusBadGateway)
		return
	}
	if f.lastAuth != "Bearer "+f.token {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/search/granules.json":
		f.cmrCalls++
		w.Header().Set("CMR-Hits", fmt.Sprint(f.hits))
		entries := []map[string]string{}
		if r.URL.Query().Get("page_size") != "0" {
			for _, id := range f.granuleIDs {
				entries = append(entries, map[string]string{"id": id})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"feed": map[string]any{"entry": entries}})
	case strings.HasSuffix(r.URL.Path, "/coverage/rangeset"):
		f.submits++
		f.lastMethod = r.Method
		f.lastQuery = r.URL.Query()
		if r.Method == http.MethodPost {
			if err := r.ParseMultipartForm(1 << 20); err == nil {
				f.lastForm = r.MultipartForm.Value
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobID": "job-1", "status": "accepted"})
	case r.URL.Path == "/jobs/job-1/cancel" && r.Method == http.MethodPost:
		f.cancels++
		_ = json.NewEncoder(w).Encode(map[string]any{"jobID": "job-1", "status": "canceled"})
	case r.URL.Path == "/jobs/job-1":
		status := f.statuses[min(f.polls, len(f.statuses)-1)]
		f.polls++
		resp := map[string]any{"jobID": "job-1", "status": status, "progress": 50, "message": "the service failed"}
		if status == "failed" && f.eula {
			resp["errors"] = []map[string]string{{
				"url":     "https://opendap.example/granule.HDF5",
				"message": "WorkItem failed: Subsetter failed with error: 403 error retrieving: https://opendap.example/granule.HDF5.dmr.xml",
			}}
		}
		if status == "failed" && f.failWith != "" {
			resp["errors"] = []map[string]string{{"message": f.failWith}}
		}
		if status == "successful" {
			resp["links"] = []map[string]any{
				{"href": f.srv.URL + "/stac/job-1/", "rel": "stac-catalog-json"},
				{"href": f.srv.URL + "/data/3B-MO.20000101.nc4", "rel": "data", "type": "application/x-netcdf4",
					"temporal": map[string]string{"start": "2000-01-01T00:00:00.000Z", "end": "2000-01-31T23:59:59.999Z"}},
				{"href": f.srv.URL + "/data/3B-MO.20000201.nc4", "rel": "data", "type": "application/x-netcdf4",
					"temporal": map[string]string{"start": "2000-02-01T00:00:00.000Z", "end": "2000-02-29T23:59:59.999Z"}},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	case strings.HasPrefix(r.URL.Path, "/data/"):
		_, _ = w.Write([]byte("netcdf " + strings.TrimPrefix(r.URL.Path, "/data/")))
	default:
		http.NotFound(w, r)
	}
}

type recordingSink struct {
	mu    sync.Mutex
	files map[string]core.File
	data  map[string]string
}

func newSink() *recordingSink {
	return &recordingSink{files: map[string]core.File{}, data: map[string]string{}}
}

func (s *recordingSink) Put(_ context.Context, f core.File) error {
	b, err := io.ReadAll(f.Body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[f.Name] = f
	s.data[f.Name] = string(b)
	return nil
}

type harness struct {
	nasa *fakeNASA
	mr   *miniredis.Miniredis
	a    *Adapter
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	nasa := newFakeNASA(t)
	mr := miniredis.RunT(t)
	cfg.HarmonyURL, cfg.CMRURL = nasa.srv.URL, nasa.srv.URL
	if cfg.TeamToken == "" {
		cfg.TeamToken = "team-token"
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 4
	}
	cfg.Downloads = 2
	return &harness{nasa: nasa, mr: mr, a: New(cfg, redis.NewClient(&redis.Options{Addr: mr.Addr()}))}
}

func task(west float64) *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		VariableId: "precipitation-gpm",
		Bbox:       &commonv1.BBox{West: west, South: 28, East: west + 1, North: 29},
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 2000, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 2000, Month: 2, Day: 29},
		},
		Adapter: &ingestv1.IngestTask_HarmonySubset{HarmonySubset: &ingestv1.HarmonySubset{
			CollectionId: "C2723754851-GES_DISC",
			Variables:    []string{"Grid/precipitation"},
			MaxGranules:  1000,
		}},
	}
}

func wantPending(t *testing.T, err error) {
	t.Helper()
	if _, ok := core.AsPending(err); !ok {
		t.Fatalf("want Pending, got %v", err)
	}
}

func wantCode(t *testing.T, err error, code gatewayv1.ErrorCode, transient bool) {
	t.Helper()
	if _, ok := core.AsPending(err); ok || err == nil {
		t.Fatalf("want a %v error, got %v", code, err)
	}
	e := core.AsError(err)
	if e.Code != code || e.Transient != transient {
		t.Fatalf("got %v transient=%v, want %v transient=%v (%v)", e.Code, e.Transient, code, transient, err)
	}
}

func TestSubmitPollAndDownload(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := context.Background()
	sink := newSink()

	_, err := h.a.Fetch(ctx, task(77), sink)
	wantPending(t, err)
	q := h.nasa.lastQuery
	if q.Get("variable") != "Grid/precipitation" || q.Get("forceAsync") != "true" || q.Get("skipPreview") != "true" {
		t.Errorf("submit query: %v", q)
	}
	subsets := strings.Join(q["subset"], " ")
	for _, want := range []string{"lat(28:29)", "lon(77:78)", `time("2000-01-01T00:00:00Z":"2000-02-29T23:59:59Z")`} {
		if !strings.Contains(subsets, want) {
			t.Errorf("subsets %q lack %q", subsets, want)
		}
	}
	if h.nasa.lastAuth != "Bearer team-token" {
		t.Errorf("auth header: %q", h.nasa.lastAuth)
	}

	_, err = h.a.Fetch(ctx, task(77), sink) // running
	wantPending(t, err)

	got, err := h.a.Fetch(ctx, task(77), sink) // successful
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != ingestv1.Format_FORMAT_NETCDF4 || got.Source != ingestv1.Source_SOURCE_HARMONY_SUBSET || got.Variables[0] != "Grid/precipitation" {
		t.Errorf("fetched: %+v", got)
	}
	if len(sink.files) != 2 || sink.data["3B-MO.20000201.nc4"] != "netcdf 3B-MO.20000201.nc4" {
		t.Fatalf("files: %v", sink.data)
	}
	if c := sink.files["3B-MO.20000201.nc4"].Coverage; c.GetStart().GetMonth() != 2 || c.GetEnd().GetDay() != 29 {
		t.Errorf("coverage: %v", c)
	}
	if h.nasa.submits != 1 {
		t.Errorf("submitted %d times, want 1", h.nasa.submits)
	}
	if keys := h.mr.Keys(); len(keys) != 0 {
		t.Errorf("state or slots left in Redis: %v", keys)
	}
}

// The worker turns these reports into TaskProgress, which the app shows as
// "NASA is cutting the files" with one percent that only goes up: Harmony's
// work fills the first half and the downloads the second. Before Harmony has
// a number, there's no percent, so the app shows the time instead of 0%.
func TestReportsProgress(t *testing.T) {
	h := newHarness(t, Config{})
	var (
		mu      sync.Mutex
		reports []string
	)
	ctx := core.WithProgress(context.Background(), func(percent int, activity string) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, fmt.Sprintf("%d %s", percent, activity))
	})
	sink := newSink()
	for range 3 { // submit, running, successful
		_, _ = h.a.Fetch(ctx, task(77), sink)
	}
	cutting := "NASA's Harmony service is cutting the files to your rectangle"
	want := []string{"-1 " + cutting, "25 " + cutting, "50 Downloading the files from NASA, 0 of 2"}
	if len(reports) != 5 || strings.Join(reports[:3], "|") != strings.Join(want, "|") {
		t.Fatalf("reports: %q", reports)
	}
	if reports[3] != "75 Downloading the files from NASA, 1 of 2" || reports[4] != "100 Downloading the files from NASA, 2 of 2" {
		t.Errorf("download reports: %q", reports[3:])
	}
}

func TestProgressScale(t *testing.T) {
	subset := mode{}
	average := mode{areaAverage: true}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"Harmony at 0 is unknown", subset.processing(0), core.Unknown},
		{"Harmony halfway, subset", subset.processing(50), 25},
		{"Harmony done, subset", subset.processing(100), 50},
		{"an average at 0 is unknown", average.processing(0), core.Unknown},
		{"Harmony done, average", average.processing(100), 90},
		{"downloads start where Harmony ended", subset.downloaded(0, 72), 50},
		{"downloads end at 100", subset.downloaded(72, 72), 100},
		{"an average's one file", average.downloaded(1, 1), 100},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// A job NASA can't finish, like the Giovanni averaging that sat at 0% for 15
// minutes, would hold one of the few Harmony slots for hours.
func TestTimeLimitStopsAStuckJob(t *testing.T) {
	h := newHarness(t, Config{MaxWait: 30 * time.Minute})
	h.nasa.statuses = []string{"running"}
	start := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	ctx := context.Background()

	h.a.now = func() time.Time { return start }
	_, err := h.a.Fetch(ctx, task(77), newSink()) // submit
	wantPending(t, err)
	h.a.now = func() time.Time { return start.Add(29 * time.Minute) }
	_, err = h.a.Fetch(ctx, task(77), newSink())
	wantPending(t, err)

	h.a.now = func() time.Time { return start.Add(31 * time.Minute) }
	_, err = h.a.Fetch(ctx, task(77), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false)
	if msg := core.AsError(err).UserMessage; !strings.Contains(msg, "30 minutes") || !strings.Contains(msg, "shorter date range") {
		t.Errorf("user message: %q", msg)
	}
	if h.nasa.cancels != 1 {
		t.Errorf("cancelled %d times at NASA, want 1", h.nasa.cancels)
	}
	if keys := h.mr.Keys(); len(keys) != 0 {
		t.Errorf("state or slots left in Redis: %v", keys)
	}
}

func TestInWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		40 * time.Minute: "40 minutes",
		time.Minute:      "1 minute",
		20 * time.Second: "20 seconds",
		90 * time.Minute: "90 minutes",
	} {
		if got := inWords(d); got != want {
			t.Errorf("inWords(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestCancelStopsTheHarmonyJob(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := context.Background()
	if err := h.a.Cancel(ctx, task(77)); err != nil || h.nasa.cancels != 0 {
		t.Fatalf("cancel with no job: %v, %d calls", err, h.nasa.cancels)
	}
	_, err := h.a.Fetch(ctx, task(77), newSink())
	wantPending(t, err)
	if err := h.a.Cancel(ctx, task(77)); err != nil {
		t.Fatal(err)
	}
	if h.nasa.cancels != 1 {
		t.Errorf("cancelled %d times at NASA, want 1", h.nasa.cancels)
	}
	if keys := h.mr.Keys(); len(keys) != 0 {
		t.Errorf("state or slots left in Redis: %v", keys)
	}
}

func TestGranuleChecks(t *testing.T) {
	h := newHarness(t, Config{})
	h.nasa.hits = 0
	_, err := h.a.Fetch(context.Background(), task(77), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false)

	h.nasa.hits = 5000
	_, err = h.a.Fetch(context.Background(), task(77), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_TOO_LARGE, false)
	if !strings.Contains(core.AsError(err).UserMessage, "5000") {
		t.Errorf("user message should say how many files: %q", core.AsError(err).UserMessage)
	}
	if h.nasa.submits != 0 {
		t.Error("submitted a job that failed the granule check")
	}
}

func TestTokens(t *testing.T) {
	t.Run("no token at all", func(t *testing.T) {
		h := newHarness(t, Config{})
		h.a.tokens = earthdata.NewTokens(h.a.rdb, "")
		_, err := h.a.Fetch(context.Background(), task(77), newSink())
		wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false)
	})
	t.Run("the user's own token wins", func(t *testing.T) {
		h := newHarness(t, Config{})
		h.nasa.token = "user-token"
		_ = h.mr.Set("td:job:{j}:earthdata", "user-token")
		tk := task(77)
		tk.EarthdataTokenKey = "td:job:{j}:earthdata"
		_, err := h.a.Fetch(context.Background(), tk, newSink())
		wantPending(t, err)
		if h.nasa.lastAuth != "Bearer user-token" {
			t.Errorf("auth header: %q", h.nasa.lastAuth)
		}
	})
	t.Run("login redirect means a bad token", func(t *testing.T) {
		h := newHarness(t, Config{})
		h.nasa.login = true
		_, err := h.a.Fetch(context.Background(), task(77), newSink())
		wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false)
	})
	t.Run("rejected token", func(t *testing.T) {
		h := newHarness(t, Config{})
		h.nasa.token = "something-else"
		_, err := h.a.Fetch(context.Background(), task(77), newSink())
		wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false)
	})
}

func TestSlotsLimitJobsPerToken(t *testing.T) {
	h := newHarness(t, Config{Concurrency: 1})
	ctx := context.Background()
	_, err := h.a.Fetch(ctx, task(77), newSink())
	wantPending(t, err)

	p, ok := core.AsPending(func() error { _, err := h.a.Fetch(ctx, task(10), newSink()); return err }())
	if !ok || !strings.Contains(p.Reason, "busy") {
		t.Fatalf("second request should wait for a slot, got %v", p)
	}
	if h.nasa.submits != 1 {
		t.Errorf("submitted %d jobs with one slot", h.nasa.submits)
	}
}

func TestFailedJobFreesItsSlot(t *testing.T) {
	h := newHarness(t, Config{Concurrency: 1})
	h.nasa.statuses = []string{"failed"}
	ctx := context.Background()
	_, err := h.a.Fetch(ctx, task(77), newSink())
	wantPending(t, err)
	_, err = h.a.Fetch(ctx, task(77), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false)

	// The slot is free again, so another request can submit.
	_, err = h.a.Fetch(ctx, task(10), newSink())
	wantPending(t, err)
	if h.nasa.submits != 2 {
		t.Errorf("submits: %d", h.nasa.submits)
	}
}

func TestServerErrorsAreTransient(t *testing.T) {
	h := newHarness(t, Config{})
	h.nasa.fail5xx = true
	_, err := h.a.Fetch(context.Background(), task(77), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true)
}

func TestIncompleteTaskIsRejected(t *testing.T) {
	h := newHarness(t, Config{})
	tk := task(77)
	tk.GetHarmonySubset().Variables = nil
	_, err := h.a.Fetch(context.Background(), tk, newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false)
}

func TestRefusedArchiveExplainsHowToFixIt(t *testing.T) {
	h := newHarness(t, Config{})
	h.nasa.statuses = []string{"failed"}
	h.nasa.eula = true
	ctx := context.Background()
	_, err := h.a.Fetch(ctx, task(77), newSink())
	wantPending(t, err)
	_, err = h.a.Fetch(ctx, task(77), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false)
	if msg := core.AsError(err).UserMessage; !strings.Contains(msg, gesdiscApproval) {
		t.Errorf("user message should link the approval page: %q", msg)
	}
}

// Giovanni explains a refused input, like a start before the product's
// first time step. That's the request's fault, so trying again won't help.
func TestGiovanniRefusalIsPassedOn(t *testing.T) {
	h := newHarness(t, Config{})
	h.a = NewAreaAverage(h.a.cfg, h.a.rdb)
	h.nasa.statuses = []string{"failed"}
	h.nasa.failWith = "WorkItem failed: giovanni-averaging-adapter:v1.1.14: One of your inputs in the request is not supported by the Cloud Giovanni Averaging Service: " +
		"Specified start time must be on or after the product's first time value, 2015-01-01T12:00:00Z.: WorkItem failed: giovanni-averaging-adapter:v1.1.14: the same again"
	ctx := context.Background()
	_, err := h.a.Fetch(ctx, areaTask(), newSink())
	wantPending(t, err)
	_, err = h.a.Fetch(ctx, areaTask(), newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false)
	want := "NASA's Giovanni service refused this request: Specified start time must be on or after the product's first time value, 2015-01-01T12:00:00Z."
	if msg := core.AsError(err).UserMessage; msg != want {
		t.Errorf("user message %q", msg)
	}
}

func areaTask() *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		VariableId: "no2-omi",
		Bbox:       &commonv1.BBox{West: 114, South: 30, East: 120, North: 36},
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 2010, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 2010, Month: 12, Day: 31},
		},
		Adapter: &ingestv1.IngestTask_GiovanniArea{GiovanniArea: &ingestv1.GiovanniArea{
			DataFieldId:  "OMNO2d_003_ColumnAmountNO2TropCloudScreened",
			CollectionId: "C1266136111-GES_DISC",
		}},
	}
}

func TestAreaAverageMode(t *testing.T) {
	h := newHarness(t, Config{})
	h.a = NewAreaAverage(h.a.cfg, h.a.rdb)
	ctx := context.Background()
	sink := newSink()

	if h.a.Source() != ingestv1.Source_SOURCE_GIOVANNI_AREA {
		t.Fatalf("source: %v", h.a.Source())
	}
	_, err := h.a.Fetch(ctx, areaTask(), sink)
	wantPending(t, err)
	q := h.nasa.lastQuery
	if q.Get("average") != "area" || q.Get("variable") != "OMNO2d_003_ColumnAmountNO2TropCloudScreened" {
		t.Errorf("submit query: %v", q)
	}
	if h.nasa.cmrCalls != 0 {
		t.Errorf("area averages shouldn't count granules in CMR, called it %d times", h.nasa.cmrCalls)
	}

	_, err = h.a.Fetch(ctx, areaTask(), sink) // running
	wantPending(t, err)
	got, err := h.a.Fetch(ctx, areaTask(), sink) // successful
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != ingestv1.Source_SOURCE_GIOVANNI_AREA || got.Format != ingestv1.Format_FORMAT_GIOVANNI_CSV {
		t.Errorf("fetched: %+v", got)
	}
	for name, f := range sink.files {
		if f.ContentType != "text/csv" {
			t.Errorf("%s stored as %q, want text/csv", name, f.ContentType)
		}
	}

	// The two modes key their cache differently, so they never share files.
	if k1, _ := newRequest(areaTask(), areaMode); k1 != nil {
		k2, _ := newRequest(task(77), subsetMode)
		if k1.key == k2.key {
			t.Error("the area and subset modes share a cache key")
		}
	}
}

func TestAreaAverageNeedsTheGiovanniID(t *testing.T) {
	h := newHarness(t, Config{})
	h.a = NewAreaAverage(h.a.cfg, h.a.rdb)
	tk := areaTask()
	tk.GetGiovanniArea().DataFieldId = ""
	_, err := h.a.Fetch(context.Background(), tk, newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false)
}

// NOBM's monthly files have a time axis Harmony's subsetter can't read, so
// the job names its granules by ID and asks for no time subset.
func TestByGranuleIDNamesGranulesWithoutATimeSubset(t *testing.T) {
	h := newHarness(t, Config{})
	h.nasa.granuleIDs = []string{"G1-GES_DISC", "G2-GES_DISC"}
	tk := task(77)
	tk.GetHarmonySubset().ByGranuleId = true

	_, err := h.a.Fetch(context.Background(), tk, newSink())
	wantPending(t, err)
	if h.nasa.lastMethod != http.MethodPost {
		t.Fatalf("submitted with %s, want a POSTed form", h.nasa.lastMethod)
	}
	form := h.nasa.lastForm
	if got := strings.Join(form["granuleId"], ","); got != "G1-GES_DISC,G2-GES_DISC" {
		t.Errorf("granuleId: %q", got)
	}
	if got := strings.Join(form["subset"], " "); got != "lat(28:29) lon(77:78)" {
		t.Errorf("subset: %q, want only the rectangle", got)
	}
	if got := strings.Join(form["variable"], ","); got != "Grid/precipitation" {
		t.Errorf("variable: %q", got)
	}
}

func TestByGranuleIDKeepsTheCapAndNoData(t *testing.T) {
	h := newHarness(t, Config{})
	h.nasa.granuleIDs = []string{"G1", "G2", "G3"}
	tk := task(77)
	tk.GetHarmonySubset().ByGranuleId = true
	tk.GetHarmonySubset().MaxGranules = 2
	_, err := h.a.Fetch(context.Background(), tk, newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_TOO_LARGE, false)

	h2 := newHarness(t, Config{})
	tk2 := task(78)
	tk2.GetHarmonySubset().ByGranuleId = true
	_, err = h2.a.Fetch(context.Background(), tk2, newSink())
	wantCode(t, err, gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false)
	if h2.nasa.submits != 0 {
		t.Errorf("submitted %d jobs with no granules", h2.nasa.submits)
	}
}
