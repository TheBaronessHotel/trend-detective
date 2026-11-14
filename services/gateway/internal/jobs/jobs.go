// Package jobs orchestrates jobs. CreateJob validates a request, fills in the
// catalog's defaults, and publishes the first task. Every StageDone is
// recorded, and when the last task of a stage reports, the job moves to the
// next stage or finishes.
//
// Everything here is safe to repeat. JetStream delivers each StageDone at
// least once, so a second delivery must not count a task twice or start a
// stage twice.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	preprocessv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/preprocess/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/natsx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/internal/sources"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/google/uuid"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Publisher sends a task to a stage.
type Publisher interface {
	Publish(ctx context.Context, subject, msgID string, msg proto.Message) error
}

// Limits are the size guards and defaults from the environment.
type Limits struct {
	SubregionN  int32
	MaxGrid     int32
	MaxGranules int32
	// Most tasks one job may plan. Zero means no limit.
	TaskCap int32
}

// Preflight decides, before a job is queued, whether its ingest task can run.
// It returns an error with a message for the user when it can't.
type Preflight interface {
	Check(ctx context.Context, task *ingestv1.IngestTask, userToken string) error
}

// InvalidError is a request the gateway refuses. Its message is shown to the
// user.
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &InvalidError{msg: fmt.Sprintf(format, args...)}
}

// ErrUnavailable means NATS didn't take the first task, so the job never
// started. The caller can retry.
var ErrUnavailable = errors.New("couldn't start the job, try again")

// Orchestrator runs jobs.
type Orchestrator struct {
	cat    *catalog.Catalog
	store  *Store
	pub    Publisher
	limits Limits
	log    logging.Logger
	now    func() time.Time
	newID  func() string
	hub    *hub
	// Optional. Nil skips the checks.
	preflight Preflight
	// How often Watch re-reads a job when nothing woke it.
	watchPoll time.Duration
	// Watch sends the job again after this long without a change, so a quiet
	// stream, like one waiting on NASA's queue, isn't cut as idle.
	watchKeepalive time.Duration
}

// New returns an Orchestrator.
func New(cat *catalog.Catalog, store *Store, pub Publisher, limits Limits, log logging.Logger) *Orchestrator {
	return &Orchestrator{
		cat:    cat,
		store:  store,
		pub:    pub,
		limits: limits,
		log:    log.With("component", "jobs"),
		now:    time.Now,
		newID:  func() string { return uuid.Must(uuid.NewV7()).String() },
		hub:    newHub(),

		watchPoll:      5 * time.Second,
		watchKeepalive: 30 * time.Second,
	}
}

// UsePreflight makes CreateJob run p before queuing anything.
func (o *Orchestrator) UsePreflight(p Preflight) { o.preflight = p }

// TaskID names the n-th task of a stage. IDs are predictable, so starting a
// stage again produces the same tasks, and JetStream drops the repeated
// publishes by Nats-Msg-Id.
func TaskID(jobID string, stage gatewayv1.Stage, n int) string {
	return jobID + "-" + stageName(stage) + "-" + strconv.Itoa(n)
}

func stageName(s gatewayv1.Stage) string {
	switch s {
	case gatewayv1.Stage_STAGE_INGEST:
		return "ingest"
	case gatewayv1.Stage_STAGE_PREPROCESS:
		return "preprocess"
	case gatewayv1.Stage_STAGE_ANALYSIS:
		return "analysis"
	}
	return "unknown"
}

// CreateJob validates spec, fills in defaults, stores the job and publishes
// its ingest task. earthdataToken is the user's own token, or empty.
func (o *Orchestrator) CreateJob(ctx context.Context, spec *gatewayv1.JobSpec, earthdataToken string) (*gatewayv1.Job, error) {
	resolved, entry, err := o.resolve(spec)
	if err != nil {
		return nil, err
	}
	id := o.newID()
	ctx = obs.WithJob(ctx, id)
	now := o.now()

	ingest, _ := entry.IngestTemplate(resolved.GetSource())
	ingest.VariableId = resolved.GetVariableId()
	ingest.Bbox = resolved.GetBbox()
	ingest.Dates = resolved.GetDates()
	if h := ingest.GetHarmonySubset(); h != nil {
		h.MaxGranules = o.limits.MaxGranules
	}
	if c := ingest.GetCsvUpload(); c != nil {
		c.Upload = resolved.GetUpload()
	}
	// Point sources sample the middle of the rectangle.
	if p := ingest.GetPower(); p != nil {
		p.Point = center(resolved.GetBbox())
	}
	if g := ingest.GetGiovanniPoint(); g != nil {
		g.Point = center(resolved.GetBbox())
	}
	if planned := plannedTasks(resolved); o.limits.TaskCap > 0 && planned > o.limits.TaskCap {
		return nil, invalid("This job needs %d tasks, more than the limit of %d.", planned, o.limits.TaskCap)
	}
	if o.preflight != nil {
		if err := o.preflight.Check(ctx, ingest, earthdataToken); err != nil {
			return nil, err
		}
	}
	if earthdataToken != "" {
		key, err := o.store.saveEarthdataToken(ctx, id, earthdataToken)
		if err != nil {
			return nil, fmt.Errorf("save token: %w", err)
		}
		ingest.EarthdataTokenKey = key
	}

	meta := &gatewayv1.TaskMeta{JobId: id, TaskId: TaskID(id, gatewayv1.Stage_STAGE_INGEST, 0), Stage: gatewayv1.Stage_STAGE_INGEST}
	job := &gatewayv1.Job{Id: id, State: gatewayv1.JobState_JOB_STATE_RUNNING, Spec: resolved, CreatedAt: timestamppb.New(now)}
	queued := []*gatewayv1.TaskStatus{{Meta: meta, State: gatewayv1.TaskState_TASK_STATE_QUEUED}}
	if err := o.store.create(ctx, job, int32(gatewayv1.Stage_STAGE_INGEST), queued); err != nil {
		return nil, fmt.Errorf("store job: %w", err)
	}

	task := &gatewayv1.Task{Meta: meta, Spec: &gatewayv1.Task_Ingest{Ingest: ingest}}
	if err := o.pub.Publish(ctx, natsx.SubjectIngest, meta.GetTaskId(), task); err != nil {
		o.log.Error(ctx, "couldn't publish the ingest task", logging.Err(err))
		_ = o.store.finish(ctx, id, gatewayv1.JobState_JOB_STATE_FAILED, &gatewayv1.JobResults{}, o.now())
		o.hub.notify(id)
		return nil, ErrUnavailable
	}
	o.log.Info(ctx, "job created", "variable", resolved.GetVariableId(), "source", sources.Name(resolved.GetSource()))
	return o.GetJob(ctx, id)
}

// GetJob returns a job, or ErrNotFound.
func (o *Orchestrator) GetJob(ctx context.Context, id string) (*gatewayv1.Job, error) {
	job, _, err := o.store.load(ctx, id)
	return job, err
}

// CancelJob stops a job that hasn't finished: it and its unfinished tasks
// become cancelled, stages see the flag and drop its tasks, and later reports
// from them change nothing. A finished job comes back unchanged.
func (o *Orchestrator) CancelJob(ctx context.Context, id string) (*gatewayv1.Job, error) {
	changed, err := o.store.cancel(ctx, id, o.now())
	if err != nil {
		return nil, err
	}
	if changed {
		o.log.Info(ctx, "job cancelled", "job_id", id)
		o.hub.notify(id)
	}
	return o.GetJob(ctx, id)
}

// OnTaskProgress records how far a running task has got and tells anyone
// watching the job. Progress is best effort, so an update for a task that's
// unknown or already finished changes nothing.
func (o *Orchestrator) OnTaskProgress(ctx context.Context, p *gatewayv1.TaskProgress) error {
	changed, err := o.store.recordProgress(ctx, p, o.now())
	if err != nil {
		return fmt.Errorf("record TaskProgress: %w", err)
	}
	if changed {
		o.hub.notify(p.GetMeta().GetJobId())
	}
	return nil
}

// OnStageDone records a stage's report and advances the job when the stage
// is complete. It returns nil for a job that doesn't exist, since there is
// nothing to update and redelivering won't change that.
func (o *Orchestrator) OnStageDone(ctx context.Context, d *gatewayv1.StageDone) error {
	meta := d.GetMeta()
	defer o.hub.notify(meta.GetJobId())
	status := &gatewayv1.TaskStatus{
		Meta:       meta,
		State:      gatewayv1.TaskState_TASK_STATE_SUCCEEDED,
		Attempts:   d.GetAttempt(),
		CacheHit:   d.GetCacheHit(),
		FinishedAt: d.GetFinishedAt(),
	}
	if e := d.GetError(); e != nil {
		status.State = gatewayv1.TaskState_TASK_STATE_FAILED
		status.Error = e
	}
	finished, total, err := o.store.recordDone(ctx, d, status, o.now())
	if errors.Is(err, ErrNotFound) {
		o.log.Warn(ctx, "StageDone for a job that doesn't exist")
		return nil
	}
	if errors.Is(err, errCanceled) {
		o.log.Info(ctx, "StageDone for a cancelled job, ignored")
		return nil
	}
	if err != nil {
		return fmt.Errorf("record StageDone: %w", err)
	}
	if finished < total {
		return nil
	}
	return o.advance(ctx, meta.GetJobId(), meta.GetStage())
}

// advance acts on a completed stage: fail the job, finish it, or start the
// next stage. Running it twice for the same stage does no harm.
func (o *Orchestrator) advance(ctx context.Context, id string, stage gatewayv1.Stage) error {
	job, current, err := o.store.load(ctx, id)
	if err != nil {
		return err
	}
	if isFinal(job.GetState()) || current > int32(stage) {
		// A late duplicate. The job has already moved on.
		return nil
	}
	dones, err := o.store.stageDones(ctx, id, stage)
	if err != nil {
		return err
	}
	results := collect(job.GetResults(), stage, dones)
	now := o.now()

	for _, d := range dones {
		if d.GetError() != nil {
			o.log.Info(ctx, "job failed", "stage", stageName(stage), "code", d.GetError().GetCode().String())
			return o.store.finish(ctx, id, gatewayv1.JobState_JOB_STATE_FAILED, results, now)
		}
	}
	if stage >= lastStage(job.GetSpec()) {
		o.log.Info(ctx, "job succeeded")
		return o.store.finish(ctx, id, gatewayv1.JobState_JOB_STATE_SUCCEEDED, results, now)
	}

	next := stage + 1
	tasks, err := o.nextTasks(job, next, dones)
	if err != nil {
		o.log.Error(ctx, "couldn't build the next stage", logging.Err(err))
		return o.store.finish(ctx, id, gatewayv1.JobState_JOB_STATE_FAILED, results, now)
	}
	queued := make([]*gatewayv1.TaskStatus, len(tasks))
	for i, t := range tasks {
		queued[i] = &gatewayv1.TaskStatus{Meta: t.GetMeta(), State: gatewayv1.TaskState_TASK_STATE_QUEUED}
	}
	if err := o.store.setResults(ctx, id, results, now); err != nil {
		return err
	}
	if _, err := o.store.addStage(ctx, id, int32(next), queued, now); errors.Is(err, errCanceled) {
		// Cancelled while this stage was finishing: start nothing more.
		return nil
	} else if err != nil {
		return err
	}
	// Publish even if an earlier attempt already added the stage: it may
	// have crashed before publishing. Nats-Msg-Id drops the repeats.
	for _, t := range tasks {
		if err := o.pub.Publish(ctx, subjectFor(next), t.GetMeta().GetTaskId(), t); err != nil {
			return fmt.Errorf("publish %s: %w", t.GetMeta().GetTaskId(), err)
		}
	}
	o.log.Info(ctx, "stage started", "stage", stageName(next), "tasks", len(tasks))
	return nil
}

// nextTasks builds the tasks for stage from the previous stage's results.
func (o *Orchestrator) nextTasks(job *gatewayv1.Job, stage gatewayv1.Stage, prev []*gatewayv1.StageDone) ([]*gatewayv1.Task, error) {
	spec := job.GetSpec()
	meta := &gatewayv1.TaskMeta{JobId: job.GetId(), TaskId: TaskID(job.GetId(), stage, 0), Stage: stage}
	switch stage {
	case gatewayv1.Stage_STAGE_PREPROCESS:
		entry, ok := o.cat.Get(spec.GetVariableId())
		if !ok {
			return nil, fmt.Errorf("variable %q is no longer in the catalog", spec.GetVariableId())
		}
		raw := make([]*ingestv1.RawDataset, 0, len(prev))
		for _, d := range prev {
			raw = append(raw, d.GetIngest())
		}
		return []*gatewayv1.Task{{Meta: meta, Spec: &gatewayv1.Task_Preprocess{Preprocess: &preprocessv1.PreprocessTask{
			Raw:         raw,
			SourceUnits: entry.SourceUnits,
			Filters:     spec.GetFilters(),
			Aggregation: spec.GetAggregation(),
		}}}}, nil
	case gatewayv1.Stage_STAGE_ANALYSIS:
		if len(prev) != 1 {
			return nil, fmt.Errorf("want one preprocess result, got %d", len(prev))
		}
		return []*gatewayv1.Task{{Meta: meta, Spec: &gatewayv1.Task_Analysis{Analysis: &analysisv1.AnalysisTask{
			Input: prev[0].GetPreprocess(),
			Tests: spec.GetTests(),
		}}}}, nil
	}
	return nil, fmt.Errorf("no stage after %v", stage-1)
}

// collect adds a completed stage's outputs to the job's results.
func collect(results *gatewayv1.JobResults, stage gatewayv1.Stage, dones []*gatewayv1.StageDone) *gatewayv1.JobResults {
	r := proto.CloneOf(results)
	if r == nil {
		r = &gatewayv1.JobResults{}
	}
	switch stage {
	case gatewayv1.Stage_STAGE_INGEST:
		r.Raw = nil
		for _, d := range dones {
			if ds := d.GetIngest(); ds != nil {
				r.Raw = append(r.Raw, ds)
			}
		}
	case gatewayv1.Stage_STAGE_PREPROCESS:
		for _, d := range dones {
			if p := d.GetPreprocess(); p != nil {
				r.Preprocess = p
			}
		}
	case gatewayv1.Stage_STAGE_ANALYSIS:
		for _, d := range dones {
			if a := d.GetAnalysis(); a != nil {
				r.Analysis = a
			}
		}
	}
	return r
}

// plannedTasks counts the tasks a job will run: one ingest task, then one per
// later stage up to stop_after.
func plannedTasks(spec *gatewayv1.JobSpec) int32 {
	return int32(lastStage(spec))
}

func lastStage(spec *gatewayv1.JobSpec) gatewayv1.Stage {
	if s := spec.GetStopAfter(); s != gatewayv1.Stage_STAGE_UNSPECIFIED {
		return s
	}
	return gatewayv1.Stage_STAGE_ANALYSIS
}

func subjectFor(s gatewayv1.Stage) string {
	switch s {
	case gatewayv1.Stage_STAGE_PREPROCESS:
		return natsx.SubjectPreprocess
	case gatewayv1.Stage_STAGE_ANALYSIS:
		return natsx.SubjectAnalysis
	}
	return natsx.SubjectIngest
}

func isFinal(s gatewayv1.JobState) bool {
	return s == gatewayv1.JobState_JOB_STATE_SUCCEEDED || s == gatewayv1.JobState_JOB_STATE_FAILED ||
		s == gatewayv1.JobState_JOB_STATE_CANCELED
}

// errCanceled means the job was cancelled, so a stage's late report changes
// nothing.
var errCanceled = errors.New("job was cancelled")

// resolve checks spec and returns a copy with every default filled in.
func (o *Orchestrator) resolve(spec *gatewayv1.JobSpec) (*gatewayv1.JobSpec, *catalog.Entry, error) {
	if spec == nil {
		return nil, nil, invalid("The request has no job spec.")
	}
	entry, ok := o.cat.Get(spec.GetVariableId())
	if !ok {
		return nil, nil, invalid("Unknown variable %q.", spec.GetVariableId())
	}
	if err := checkBBox(spec.GetBbox()); err != nil {
		return nil, nil, err
	}
	if err := checkDates(spec.GetDates()); err != nil {
		return nil, nil, err
	}

	r := proto.CloneOf(spec)
	if err := clampDates(r.Dates, entry.Variable); err != nil {
		return nil, nil, err
	}
	if r.Source == ingestv1.Source_SOURCE_UNSPECIFIED {
		r.Source = entry.Variable.GetDefaultSource()
	}
	if _, ok := entry.IngestTemplate(r.GetSource()); !ok {
		return nil, nil, invalid("%s isn't available from %s.", entry.Variable.GetName(), sources.Name(r.GetSource()))
	}
	// Only files from CreateUploadURL, so a job can't read other buckets.
	if r.GetSource() == ingestv1.Source_SOURCE_CSV_UPLOAD {
		if up := r.GetUpload(); up.GetKey() == "" || up.GetBucket() != "uploads" {
			return nil, nil, invalid("A CSV upload job needs the file uploaded through CreateUploadURL.")
		}
	}
	if r.Filters == nil {
		r.Filters = entry.Filters()
	}
	if r.Tests == nil {
		r.Tests = entry.Tests()
	}
	if r.Aggregation == nil {
		r.Aggregation = entry.Aggregation()
	}
	if r.Aggregation == nil {
		r.Aggregation = &preprocessv1.Aggregation{SubregionN: o.limits.SubregionN, MinValidFraction: 0.5}
	}
	// MAX_GRID is a guard, so a request can lower it but not raise it.
	if g := r.Aggregation.GetMaxGrid(); g <= 0 || g > o.limits.MaxGrid {
		r.Aggregation.MaxGrid = o.limits.MaxGrid
	}
	return r, entry, nil
}

func center(b *commonv1.BBox) *commonv1.LatLon {
	return &commonv1.LatLon{Lat: (b.GetSouth() + b.GetNorth()) / 2, Lon: (b.GetWest() + b.GetEast()) / 2}
}

func checkBBox(b *commonv1.BBox) error {
	if b == nil {
		return invalid("Pick a rectangle on the map.")
	}
	if b.GetWest() < -180 || b.GetEast() > 180 || b.GetSouth() < -90 || b.GetNorth() > 90 {
		return invalid("The rectangle goes past the edge of the map.")
	}
	if b.GetWest() >= b.GetEast() || b.GetSouth() >= b.GetNorth() {
		return invalid("The rectangle needs its west edge left of its east edge, and its south edge below its north edge.")
	}
	return nil
}

func checkDates(r *commonv1.DateRange) error {
	start, ok1 := toTime(r.GetStart())
	end, ok2 := toTime(r.GetEnd())
	if !ok1 || !ok2 {
		return invalid("Pick a start date and an end date.")
	}
	if end.Before(start) {
		return invalid("The start date is after the end date.")
	}
	return nil
}

// clampDates trims a valid range to the dates the variable covers. A range
// entirely outside them is refused, with the available range in the message.
func clampDates(r *commonv1.DateRange, v *gatewayv1.Variable) error {
	availStart, ok1 := toTime(v.GetAvailable().GetStart())
	availEnd, ok2 := toTime(v.GetAvailable().GetEnd())
	if !ok1 || !ok2 {
		return nil
	}
	start, _ := toTime(r.GetStart())
	end, _ := toTime(r.GetEnd())
	if end.Before(availStart) || start.After(availEnd) {
		return invalid("%s has data from %s to %s. Pick dates in that range.",
			v.GetName(), availStart.Format("January 2006"), availEnd.Format("January 2006"))
	}
	if start.Before(availStart) {
		r.Start = proto.CloneOf(v.GetAvailable().GetStart())
	}
	if end.After(availEnd) {
		r.End = proto.CloneOf(v.GetAvailable().GetEnd())
	}
	return nil
}

// toTime reports false for a missing or impossible date, like February 30.
func toTime(d *commonv1.Date) (time.Time, bool) {
	if d == nil {
		return time.Time{}, false
	}
	t := time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC)
	return t, t.Year() == int(d.GetYear()) && int(t.Month()) == int(d.GetMonth()) && t.Day() == int(d.GetDay())
}
