package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
)

type fakeNASA struct {
	hits        int
	subsettable bool
	quota       float64
	status      int // non-zero makes every endpoint answer with it
	slow        time.Duration
	capsCalls   atomic.Int32
	quotaAuth   atomic.Value
}

func (f *fakeNASA) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(f.slow)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		switch r.URL.Path {
		case "/capabilities":
			f.capsCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []any{
				map[string]any{"capabilities": map[string]any{"subsetting": map[string]bool{"bbox": f.subsettable, "temporal": true, "variable": true}}},
			}})
		case "/search/granules.json":
			w.Header().Set("CMR-Hits", fmt.Sprint(f.hits))
			_, _ = w.Write([]byte(`{}`))
		case "/quota":
			f.quotaAuth.Store(r.Header.Get("Authorization"))
			_, _ = fmt.Fprintf(w, `{"status":200,"quota":%v}`, f.quota)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newChecker(t *testing.T, f *fakeNASA) *Checker {
	srv := f.server(t)
	return New(Config{
		CMRURL: srv.URL, HarmonyURL: srv.URL, GiovanniURL: srv.URL,
		TeamToken: "team", QuotaCutoff: 0.9, Timeout: time.Second,
	}, logging.Noop())
}

func harmonyTask() *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		Bbox: &commonv1.BBox{West: 77, South: 28, East: 78, North: 29},
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 2000, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 2001, Month: 12, Day: 31},
		},
		Adapter: &ingestv1.IngestTask_HarmonySubset{HarmonySubset: &ingestv1.HarmonySubset{
			CollectionId: "C2723754851-GES_DISC", MaxGranules: 100,
		}},
	}
}

func giovanniTask() *ingestv1.IngestTask {
	return &ingestv1.IngestTask{Adapter: &ingestv1.IngestTask_GiovanniArea{GiovanniArea: &ingestv1.GiovanniArea{}}}
}

func wantRefusal(t *testing.T, err error, contains string, quota bool) {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("want a Refusal, got %v", err)
	}
	if !strings.Contains(r.Msg, contains) || r.Quota != quota {
		t.Errorf("refusal %q quota=%v, want %q quota=%v", r.Msg, r.Quota, contains, quota)
	}
}

func TestHarmonyChecks(t *testing.T) {
	ctx := context.Background()

	ok := &fakeNASA{hits: 24, subsettable: true}
	if err := newChecker(t, ok).Check(ctx, harmonyTask(), ""); err != nil {
		t.Errorf("24 granules under a limit of 100: %v", err)
	}

	empty := &fakeNASA{hits: 0, subsettable: true}
	wantRefusal(t, newChecker(t, empty).Check(ctx, harmonyTask(), ""), "no data", false)

	big := &fakeNASA{hits: 5000, subsettable: true}
	wantRefusal(t, newChecker(t, big).Check(ctx, harmonyTask(), ""), "5000 files", false)

	noSubset := &fakeNASA{hits: 24, subsettable: false}
	wantRefusal(t, newChecker(t, noSubset).Check(ctx, harmonyTask(), ""), "can't cut", false)
}

func TestCapabilitiesAreCached(t *testing.T) {
	f := &fakeNASA{hits: 24, subsettable: true}
	c := newChecker(t, f)
	for range 3 {
		if err := c.Check(context.Background(), harmonyTask(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.capsCalls.Load(); n != 1 {
		t.Errorf("asked Harmony for capabilities %d times, want 1", n)
	}
}

func TestGiovanniQuota(t *testing.T) {
	ctx := context.Background()

	used := &fakeNASA{quota: 0.95}
	wantRefusal(t, newChecker(t, used).Check(ctx, giovanniTask(), ""), "95%", true)

	fine := &fakeNASA{quota: 0.2}
	if err := newChecker(t, fine).Check(ctx, giovanniTask(), "user-token"); err != nil {
		t.Errorf("quota at 20%%: %v", err)
	}
	if got := fine.quotaAuth.Load(); got != "Bearer user-token" {
		t.Errorf("quota check should use the user's token, sent %v", got)
	}
}

func TestFailsOpenWhenNASACantAnswer(t *testing.T) {
	ctx := context.Background()
	for name, f := range map[string]*fakeNASA{
		"server error": {status: http.StatusServiceUnavailable},
		"too slow":     {slow: 2 * time.Second, hits: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if err := newChecker(t, f).Check(ctx, harmonyTask(), ""); err != nil {
				t.Errorf("want nil so the job goes ahead, got %v", err)
			}
		})
	}
}

func TestOtherSourcesSkipTheChecks(t *testing.T) {
	f := &fakeNASA{status: http.StatusInternalServerError}
	power := &ingestv1.IngestTask{Adapter: &ingestv1.IngestTask_Power{Power: &ingestv1.Power{}}}
	if err := newChecker(t, f).Check(context.Background(), power, ""); err != nil {
		t.Errorf("POWER needs no preflight: %v", err)
	}
}
