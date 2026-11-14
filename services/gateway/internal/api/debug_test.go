package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
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

func TestDebugJobs(t *testing.T) {
	mr := miniredis.RunT(t)
	cat, err := catalog.Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	orch := jobs.New(cat, jobs.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour),
		nopPublisher{}, jobs.Limits{SubregionN: 8, MaxGrid: 128}, logging.Noop())
	mux := http.NewServeMux()
	mux.Handle("GET /debug/jobs/{id}", DebugJobs(orch))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/debug/jobs/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown job: %d", resp.StatusCode)
	}

	ctx := context.Background()
	job, err := orch.CreateJob(ctx, &gatewayv1.JobSpec{
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
	meta := job.GetTasks()[0].GetMeta()
	failed := &gatewayv1.StageDone{Meta: meta, Attempt: 3, Result: &gatewayv1.StageDone_Error{Error: &gatewayv1.TaskError{
		Code: gatewayv1.ErrorCode_ERROR_CODE_RETRIES_EXHAUSTED, Detail: "ingest gave up after 3 deliveries",
	}}}
	if err := orch.OnStageDone(ctx, failed); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Get(srv.URL + "/debug/jobs/" + job.GetId())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Job struct {
			State string `json:"state"`
		} `json:"job"`
		Events []struct {
			Attempt int `json:"attempt"`
			Error   struct {
				Detail string `json:"detail"`
			} `json:"error"`
		} `json:"stage_done_events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Job.State != "JOB_STATE_FAILED" || len(body.Events) != 1 || body.Events[0].Attempt != 3 || body.Events[0].Error.Detail == "" {
		t.Errorf("debug output: %+v", body)
	}
}
