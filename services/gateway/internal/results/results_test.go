package results

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	preprocessv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/preprocess/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/services/gateway"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/alicebob/miniredis/v2"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

type nopPublisher struct{}

func (nopPublisher) Publish(context.Context, string, string, proto.Message) error { return nil }

// prepDocs plays the prep bucket for cube headers and grids.
type prepDocs map[string]proto.Message

func (c prepDocs) GetProto(_ context.Context, key string, msg proto.Message) error {
	h, ok := c[key]
	if !ok {
		return blob.ErrNotFound
	}
	proto.Merge(msg, h)
	return nil
}

type harness struct {
	svc  *Service
	orch *jobs.Orchestrator
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	mr := miniredis.RunT(t)
	cat, err := catalog.Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	orch := jobs.New(cat, jobs.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour),
		nopPublisher{}, jobs.Limits{SubregionN: 8, MaxGrid: 128}, logging.Noop())
	signer, err := blob.NewPresigner("http://localhost:8333", "key", "secret")
	if err != nil {
		t.Fatal(err)
	}
	headers := prepDocs{
		"cubes/values.json": &preprocessv1.CubeHeader{Data: &commonv1.BlobRef{Bucket: "prep", Key: "cubes/values.u16"}},
		"grids/abc.json": &preprocessv1.Grid{
			Values:    &commonv1.BlobRef{Bucket: "prep", Key: "grids/abc.values.f32"},
			Anomalies: &commonv1.BlobRef{Bucket: "prep", Key: "grids/abc.anomalies.f32"},
		},
	}
	return &harness{svc: New(orch, headers, signer, 15*time.Minute, 20<<20), orch: orch}
}

// preprocessedJob creates a job and reports ingest and preprocess for it, so
// its results hold a series and a value cube but no analysis yet.
func (h *harness) preprocessedJob(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	job, err := h.orch.CreateJob(ctx, &gatewayv1.JobSpec{
		VariableId: "air-temperature-power",
		Bbox:       &commonv1.BBox{West: 77, South: 28, East: 78, North: 29},
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 1981, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 1990, Month: 12, Day: 31},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	id := job.GetId()
	report := func(stage gatewayv1.Stage, d *gatewayv1.StageDone) {
		d.Meta = &gatewayv1.TaskMeta{JobId: id, TaskId: jobs.TaskID(id, stage, 0), Stage: stage}
		if err := h.orch.OnStageDone(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	report(gatewayv1.Stage_STAGE_INGEST, &gatewayv1.StageDone{Result: &gatewayv1.StageDone_Ingest{Ingest: &ingestv1.RawDataset{}}})
	report(gatewayv1.Stage_STAGE_PREPROCESS, &gatewayv1.StageDone{Result: &gatewayv1.StageDone_Preprocess{Preprocess: &preprocessv1.PreprocessResult{
		Series:    &commonv1.BlobRef{Bucket: "prep", Key: "series/abc.json"},
		ValueCube: &commonv1.BlobRef{Bucket: "prep", Key: "cubes/values.json"},
		Grid:      &commonv1.BlobRef{Bucket: "prep", Key: "grids/abc.json"},
		SeriesCsv: &commonv1.BlobRef{Bucket: "raw", Key: "not-allowed.csv"},
	}}})
	return id
}

func TestResultURLSignsForTheBrowser(t *testing.T) {
	h := newHarness(t)
	id := h.preprocessedJob(t)

	link, err := h.svc.ResultURL(context.Background(), id, gatewayv1.Artifact_ARTIFACT_SERIES)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "localhost:8333" || u.Path != "/prep/series/abc.json" || u.Query().Get("X-Amz-Expires") != "900" || u.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("signed URL: %s", link.URL)
	}
	if link.DataURL != "" {
		t.Errorf("a series has no data URL, got %s", link.DataURL)
	}
	if d := time.Until(link.ExpiresAt); d < 14*time.Minute || d > 16*time.Minute {
		t.Errorf("expires in %v", d)
	}
}

func TestCubeGetsADataURL(t *testing.T) {
	h := newHarness(t)
	id := h.preprocessedJob(t)
	link, err := h.svc.ResultURL(context.Background(), id, gatewayv1.Artifact_ARTIFACT_VALUE_CUBE)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link.URL, "/prep/cubes/values.json") || !strings.Contains(link.DataURL, "/prep/cubes/values.u16") {
		t.Errorf("cube links: %s and %s", link.URL, link.DataURL)
	}
}

func TestGridGetsItsValuesAndAnomalies(t *testing.T) {
	h := newHarness(t)
	id := h.preprocessedJob(t)
	link, err := h.svc.ResultURL(context.Background(), id, gatewayv1.Artifact_ARTIFACT_GRID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link.URL, "/prep/grids/abc.json") ||
		!strings.Contains(link.DataURL, "/prep/grids/abc.values.f32") ||
		!strings.Contains(link.AnomaliesURL, "/prep/grids/abc.anomalies.f32") {
		t.Errorf("grid links: %s, %s and %s", link.URL, link.DataURL, link.AnomaliesURL)
	}
}

func TestResultURLErrors(t *testing.T) {
	h := newHarness(t)
	id := h.preprocessedJob(t)
	ctx := context.Background()

	_, err := h.svc.ResultURL(ctx, id, gatewayv1.Artifact_ARTIFACT_SERIES_ANALYSIS)
	var notReady *NotReadyError
	if !errors.As(err, &notReady) || !strings.Contains(notReady.Error(), "isn't ready yet") {
		t.Errorf("analysis before it ran: %v", err)
	}

	_, err = h.svc.ResultURL(ctx, id, gatewayv1.Artifact_ARTIFACT_UNSPECIFIED)
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Errorf("unspecified artifact: %v", err)
	}

	if _, err = h.svc.ResultURL(ctx, "nope", gatewayv1.Artifact_ARTIFACT_SERIES); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("unknown job: %v", err)
	}

	// A result pointing outside prep and analysis is never signed.
	if _, err = h.svc.ResultURL(ctx, id, gatewayv1.Artifact_ARTIFACT_SERIES_CSV); err == nil {
		t.Error("signed a download from the raw bucket")
	}
}

func TestUploadURL(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	up, err := h.svc.UploadURL(ctx, `C:\Users\me\my rain data (2024).CSV`, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if up.Ref.GetBucket() != "uploads" || !strings.HasSuffix(up.Ref.GetKey(), "/my_rain_data_2024_.CSV") || up.Ref.GetSizeBytes() != 1024 {
		t.Errorf("upload ref: %v", up.Ref)
	}
	u, _ := url.Parse(up.URL)
	if u.Host != "localhost:8333" || !strings.HasPrefix(u.Path, "/uploads/") {
		t.Errorf("upload URL: %s", up.URL)
	}

	for name, tc := range map[string]struct {
		file string
		size int64
	}{
		"not a CSV":   {"data.xlsx", 10},
		"only .csv":   {".csv", 10},
		"empty":       {"data.csv", 0},
		"too big":     {"data.csv", 21 << 20},
		"path escape": {"../../etc/passwd", 10},
	} {
		if _, err := h.svc.UploadURL(ctx, tc.file, tc.size); err == nil {
			t.Errorf("%s: accepted %q (%d bytes)", name, tc.file, tc.size)
		}
	}
}
