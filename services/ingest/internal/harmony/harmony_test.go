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

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
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
	eula      bool
	login     bool
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
		w.Header().Set("CMR-Hits", fmt.Sprint(f.hits))
		_, _ = w.Write([]byte(`{"feed":{"entry":[]}}`))
	case strings.HasSuffix(r.URL.Path, "/coverage/rangeset"):
		f.submits++
		f.lastQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{"jobID": "job-1", "status": "accepted"})
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
		h.a.cfg.TeamToken = ""
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
