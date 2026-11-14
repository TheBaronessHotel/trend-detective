package jobs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	preprocessv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/preprocess/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/natsx"
	"github.com/TheBaronessHotel/trend-detective/services/gateway"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/alicebob/miniredis/v2"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

type published struct {
	subject, msgID string
	task           *gatewayv1.Task
}

type fakePublisher struct {
	mu   sync.Mutex
	msgs []published
	err  error
}

func (f *fakePublisher) Publish(_ context.Context, subject, msgID string, msg proto.Message) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, published{subject, msgID, proto.CloneOf(msg.(*gatewayv1.Task))})
	return nil
}

func (f *fakePublisher) last(t *testing.T) published {
	t.Helper()
	if len(f.msgs) == 0 {
		t.Fatal("nothing was published")
	}
	return f.msgs[len(f.msgs)-1]
}

type harness struct {
	o   *Orchestrator
	pub *fakePublisher
	mr  *miniredis.Miniredis
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	mr := miniredis.RunT(t)
	cat, err := catalog.Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	pub := &fakePublisher{}
	store := NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), 7*24*time.Hour)
	o := New(cat, store, pub, Limits{SubregionN: 8, MaxGrid: 128, MaxGranules: 1000}, logging.Noop())
	n := 0
	o.newID = func() string { n++; return "job" + string(rune('0'+n)) }
	return &harness{o: o, pub: pub, mr: mr}
}

func spec() *gatewayv1.JobSpec {
	return &gatewayv1.JobSpec{
		VariableId: "air-temperature-power",
		Bbox:       &commonv1.BBox{West: 77, South: 28, East: 78, North: 29},
		Dates: &commonv1.DateRange{
			Start: &commonv1.Date{Year: 1981, Month: 1, Day: 1},
			End:   &commonv1.Date{Year: 2024, Month: 12, Day: 31},
		},
	}
}

func done(task *gatewayv1.Task) *gatewayv1.StageDone {
	d := &gatewayv1.StageDone{Meta: task.GetMeta(), Attempt: 1}
	switch task.GetMeta().GetStage() {
	case gatewayv1.Stage_STAGE_INGEST:
		d.Result = &gatewayv1.StageDone_Ingest{Ingest: &ingestv1.RawDataset{VariableId: "air-temperature-power", Format: ingestv1.Format_FORMAT_POWER_JSON}}
	case gatewayv1.Stage_STAGE_PREPROCESS:
		d.Result = &gatewayv1.StageDone_Preprocess{Preprocess: &preprocessv1.PreprocessResult{Series: &commonv1.BlobRef{Bucket: "prep", Key: "s.json"}}}
	case gatewayv1.Stage_STAGE_ANALYSIS:
		d.Result = &gatewayv1.StageDone_Analysis{Analysis: &analysisv1.AnalysisResult{Series: &commonv1.BlobRef{Bucket: "analysis", Key: "a.json"}}}
	}
	return d
}

func TestCreateJobPublishesTheIngestTaskWithDefaults(t *testing.T) {
	h := newHarness(t)
	job, err := h.o.CreateJob(context.Background(), spec(), "")
	if err != nil {
		t.Fatal(err)
	}
	if job.GetState() != gatewayv1.JobState_JOB_STATE_RUNNING || len(job.GetTasks()) != 1 || job.GetTasksTotal() != 1 {
		t.Fatalf("job: %v", job)
	}
	if job.GetTasks()[0].GetState() != gatewayv1.TaskState_TASK_STATE_QUEUED {
		t.Errorf("task state: %v", job.GetTasks()[0].GetState())
	}

	s := job.GetSpec()
	if s.GetSource() != ingestv1.Source_SOURCE_POWER {
		t.Errorf("default source: %v", s.GetSource())
	}
	if len(s.GetFilters().GetOps()) == 0 || len(s.GetTests().GetSeries()) == 0 {
		t.Error("catalog filters or tests weren't filled in")
	}
	if a := s.GetAggregation(); a.GetSubregionN() != 8 || a.GetMaxGrid() != 128 {
		t.Errorf("aggregation: %v", a)
	}

	p := h.pub.last(t)
	if p.subject != natsx.SubjectIngest || p.msgID != "job1-ingest-0" {
		t.Errorf("published %s %s", p.subject, p.msgID)
	}
	in := p.task.GetIngest()
	pw := in.GetPower()
	if len(pw.GetParameters()) != 1 || pw.GetParameters()[0] != "T2M" || pw.GetCommunity() != "AG" {
		t.Errorf("ingest settings: %v", pw)
	}
	// The rectangle is 77 to 78 east and 28 to 29 north.
	if pw.GetPoint().GetLat() != 28.5 || pw.GetPoint().GetLon() != 77.5 {
		t.Errorf("POWER should sample the rectangle's center, got %v", pw.GetPoint())
	}
	if in.GetVariableId() != "air-temperature-power" || in.GetBbox().GetWest() != 77 || in.GetDates().GetEnd().GetYear() != 2024 {
		t.Errorf("ingest task: %v", in)
	}
}

func TestCreateJobRejectsBadRequests(t *testing.T) {
	tests := map[string]func(*gatewayv1.JobSpec){
		"unknown variable":   func(s *gatewayv1.JobSpec) { s.VariableId = "nope" },
		"no rectangle":       func(s *gatewayv1.JobSpec) { s.Bbox = nil },
		"west of east":       func(s *gatewayv1.JobSpec) { s.Bbox.West, s.Bbox.East = 78, 77 },
		"off the map":        func(s *gatewayv1.JobSpec) { s.Bbox.North = 95 },
		"end before start":   func(s *gatewayv1.JobSpec) { s.Dates.End.Year = 1980 },
		"impossible date":    func(s *gatewayv1.JobSpec) { s.Dates.Start.Month, s.Dates.Start.Day = 2, 30 },
		"source not offered": func(s *gatewayv1.JobSpec) { s.Source = ingestv1.Source_SOURCE_HARMONY_SUBSET },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			s := spec()
			mutate(s)
			_, err := h.o.CreateJob(context.Background(), s, "")
			var inv *InvalidError
			if !errors.As(err, &inv) {
				t.Fatalf("want an InvalidError, got %v", err)
			}
			if len(h.pub.msgs) != 0 {
				t.Error("a rejected job published a task")
			}
		})
	}
}

func TestMaxGridIsACap(t *testing.T) {
	h := newHarness(t)
	s := spec()
	s.Aggregation = &preprocessv1.Aggregation{SubregionN: 4, MaxGrid: 4096}
	job, err := h.o.CreateJob(context.Background(), s, "")
	if err != nil {
		t.Fatal(err)
	}
	if a := job.GetSpec().GetAggregation(); a.GetMaxGrid() != 128 || a.GetSubregionN() != 4 {
		t.Errorf("aggregation: %v", a)
	}
}

func TestJobRunsThroughEveryStage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	job, err := h.o.CreateJob(ctx, spec(), "")
	if err != nil {
		t.Fatal(err)
	}

	ingest := h.pub.last(t).task
	if err := h.o.OnStageDone(ctx, done(ingest)); err != nil {
		t.Fatal(err)
	}
	pre := h.pub.last(t)
	if pre.subject != natsx.SubjectPreprocess || pre.msgID != "job1-preprocess-0" {
		t.Fatalf("published %s %s", pre.subject, pre.msgID)
	}
	if raw := pre.task.GetPreprocess().GetRaw(); len(raw) != 1 || raw[0].GetFormat() != ingestv1.Format_FORMAT_POWER_JSON {
		t.Errorf("preprocess got raw %v", raw)
	}
	if pre.task.GetPreprocess().GetSourceUnits() != "°C" || len(pre.task.GetPreprocess().GetFilters().GetOps()) == 0 {
		t.Errorf("preprocess task: %v", pre.task.GetPreprocess())
	}

	if err := h.o.OnStageDone(ctx, done(pre.task)); err != nil {
		t.Fatal(err)
	}
	ana := h.pub.last(t)
	if ana.subject != natsx.SubjectAnalysis || ana.task.GetAnalysis().GetInput().GetSeries().GetKey() != "s.json" {
		t.Fatalf("analysis task: %s %v", ana.subject, ana.task)
	}
	if len(ana.task.GetAnalysis().GetTests().GetSeries()) == 0 {
		t.Error("analysis task has no tests")
	}

	if err := h.o.OnStageDone(ctx, done(ana.task)); err != nil {
		t.Fatal(err)
	}
	got, err := h.o.GetJob(ctx, job.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if got.GetState() != gatewayv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("state: %v", got.GetState())
	}
	if got.GetTasksTotal() != 3 || got.GetTasksSucceeded() != 3 || got.GetTasksFailed() != 0 {
		t.Errorf("counts: %d total, %d ok, %d failed", got.GetTasksTotal(), got.GetTasksSucceeded(), got.GetTasksFailed())
	}
	r := got.GetResults()
	if len(r.GetRaw()) != 1 || r.GetPreprocess() == nil || r.GetAnalysis() == nil {
		t.Errorf("results: %v", r)
	}
	if len(h.pub.msgs) != 3 {
		t.Errorf("published %d tasks, want 3", len(h.pub.msgs))
	}
}

func TestStopAfterIngest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	s := spec()
	s.StopAfter = gatewayv1.Stage_STAGE_INGEST
	job, err := h.o.CreateJob(ctx, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.o.OnStageDone(ctx, done(h.pub.last(t).task)); err != nil {
		t.Fatal(err)
	}
	got, _ := h.o.GetJob(ctx, job.GetId())
	if got.GetState() != gatewayv1.JobState_JOB_STATE_SUCCEEDED || len(got.GetResults().GetRaw()) != 1 {
		t.Errorf("job: %v", got)
	}
	if len(h.pub.msgs) != 1 {
		t.Errorf("published %d tasks, want only the ingest one", len(h.pub.msgs))
	}
}

func TestDuplicateStageDoneCountsOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	job, _ := h.o.CreateJob(ctx, spec(), "")
	d := done(h.pub.last(t).task)

	for range 3 {
		if err := h.o.OnStageDone(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := h.o.GetJob(ctx, job.GetId())
	if got.GetTasksSucceeded() != 1 || got.GetTasksTotal() != 2 {
		t.Errorf("counts after duplicates: %d ok of %d", got.GetTasksSucceeded(), got.GetTasksTotal())
	}
	// Each duplicate may republish the preprocess task, but always with the
	// same Nats-Msg-Id, so JetStream keeps one.
	for _, p := range h.pub.msgs[1:] {
		if p.msgID != "job1-preprocess-0" {
			t.Errorf("unexpected publish %s", p.msgID)
		}
	}
}

func TestLateDuplicateDoesntMoveTheJobBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	job, _ := h.o.CreateJob(ctx, spec(), "")
	ingestDone := done(h.pub.last(t).task)
	_ = h.o.OnStageDone(ctx, ingestDone)
	_ = h.o.OnStageDone(ctx, done(h.pub.last(t).task)) // preprocess

	if err := h.o.OnStageDone(ctx, ingestDone); err != nil {
		t.Fatal(err)
	}
	if last := h.pub.last(t); last.task.GetMeta().GetStage() != gatewayv1.Stage_STAGE_ANALYSIS {
		t.Errorf("a late ingest duplicate published %s", last.msgID)
	}
	_, stage, _ := h.o.store.load(ctx, job.GetId())
	if stage != int32(gatewayv1.Stage_STAGE_ANALYSIS) {
		t.Errorf("stage went back to %d", stage)
	}
}

func TestFailedTaskFailsTheJob(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	job, _ := h.o.CreateJob(ctx, spec(), "")
	task := h.pub.last(t).task
	failed := &gatewayv1.StageDone{Meta: task.GetMeta(), Attempt: 3, Result: &gatewayv1.StageDone_Error{Error: &gatewayv1.TaskError{
		Code: gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, UserMessage: "No data here.",
	}}}
	if err := h.o.OnStageDone(ctx, failed); err != nil {
		t.Fatal(err)
	}
	got, _ := h.o.GetJob(ctx, job.GetId())
	if got.GetState() != gatewayv1.JobState_JOB_STATE_FAILED || got.GetTasksFailed() != 1 {
		t.Fatalf("job: %v", got)
	}
	ts := got.GetTasks()[0]
	if ts.GetState() != gatewayv1.TaskState_TASK_STATE_FAILED || ts.GetError().GetUserMessage() != "No data here." || ts.GetAttempts() != 3 {
		t.Errorf("task status: %v", ts)
	}
	if len(h.pub.msgs) != 1 {
		t.Error("a failed job started another stage")
	}
}

func TestStageDoneForAnUnknownJobIsIgnored(t *testing.T) {
	h := newHarness(t)
	d := &gatewayv1.StageDone{Meta: &gatewayv1.TaskMeta{JobId: "gone", TaskId: "gone-ingest-0", Stage: gatewayv1.Stage_STAGE_INGEST}}
	if err := h.o.OnStageDone(context.Background(), d); err != nil {
		t.Fatalf("want nil so the event is acked, got %v", err)
	}
}

func TestEarthdataTokenStaysOutOfTheTask(t *testing.T) {
	h := newHarness(t)
	if _, err := h.o.CreateJob(context.Background(), spec(), "secret-token"); err != nil {
		t.Fatal(err)
	}
	in := h.pub.last(t).task.GetIngest()
	key := in.GetEarthdataTokenKey()
	if key == "" {
		t.Fatal("no token key in the task")
	}
	if got, err := h.mr.Get(key); err != nil || got != "secret-token" {
		t.Errorf("token in Redis: %q, %v", got, err)
	}
	if b, _ := proto.Marshal(in); bytes.Contains(b, []byte("secret-token")) {
		t.Error("the token itself is in the task")
	}
}

func TestPublishFailureFailsTheJobAndSaysTryAgain(t *testing.T) {
	h := newHarness(t)
	h.pub.err = errors.New("nats down")
	_, err := h.o.CreateJob(context.Background(), spec(), "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	got, _ := h.o.GetJob(context.Background(), "job1")
	if got.GetState() != gatewayv1.JobState_JOB_STATE_FAILED {
		t.Errorf("state: %v", got.GetState())
	}
}

type refusingPreflight struct{ err error }

func (r refusingPreflight) Check(context.Context, *ingestv1.IngestTask, string) error { return r.err }

func TestPreflightRefusalStopsTheJob(t *testing.T) {
	h := newHarness(t)
	refusal := errors.New("too many files")
	h.o.UsePreflight(refusingPreflight{err: refusal})
	_, err := h.o.CreateJob(context.Background(), spec(), "secret")
	if !errors.Is(err, refusal) {
		t.Fatalf("want the refusal, got %v", err)
	}
	if len(h.pub.msgs) != 0 || len(h.mr.Keys()) != 0 {
		t.Errorf("a refused job left %d messages and Redis keys %v", len(h.pub.msgs), h.mr.Keys())
	}
}

func TestDatesAreClampedToTheCatalogRange(t *testing.T) {
	h := newHarness(t)
	s := spec()
	s.Dates.Start = &commonv1.Date{Year: 1950, Month: 6, Day: 1}
	s.Dates.End = &commonv1.Date{Year: 2030, Month: 1, Day: 1}
	job, err := h.o.CreateJob(context.Background(), s, "")
	if err != nil {
		t.Fatal(err)
	}
	d := job.GetSpec().GetDates()
	if d.GetStart().GetYear() != 1981 || d.GetEnd().GetYear() != 2025 || d.GetEnd().GetMonth() != 12 {
		t.Errorf("dates: %v", d)
	}
	if in := h.pub.last(t).task.GetIngest(); in.GetDates().GetStart().GetYear() != 1981 {
		t.Errorf("ingest got unclamped dates: %v", in.GetDates())
	}

	outside := spec()
	outside.Dates.Start = &commonv1.Date{Year: 1950, Month: 1, Day: 1}
	outside.Dates.End = &commonv1.Date{Year: 1960, Month: 1, Day: 1}
	_, err = h.o.CreateJob(context.Background(), outside, "")
	var inv *InvalidError
	if !errors.As(err, &inv) || !strings.Contains(inv.Error(), "January 1981") {
		t.Errorf("dates entirely outside: %v", err)
	}
}

func TestTaskCap(t *testing.T) {
	h := newHarness(t)
	h.o.limits.TaskCap = 2
	_, err := h.o.CreateJob(context.Background(), spec(), "")
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("a 3-task job over a cap of 2: %v", err)
	}
	s := spec()
	s.StopAfter = gatewayv1.Stage_STAGE_INGEST
	if _, err := h.o.CreateJob(context.Background(), s, ""); err != nil {
		t.Errorf("a 1-task job under a cap of 2: %v", err)
	}
}
