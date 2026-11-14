// Package worker is ingest's driving adapter. It reads tasks from the TASKS
// stream, runs them through core, and publishes a StageDone for each one.
package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/natsx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/lein3000zzz/the-watchers/pkg/observability"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/lein3000zzz/the-watchers/pkg/observability/metrics/redkit"
	"github.com/lein3000zzz/the-watchers/pkg/observability/tracing"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	stream      = "TASKS"
	durable     = "ingest"
	tracerName  = "github.com/TheBaronessHotel/trend-detective/services/ingest/internal/worker"
	publishWait = 5 * time.Second
)

// Config holds the worker's JetStream settings.
type Config struct {
	// Tasks in flight at once.
	Concurrency int
	// Attempts per task before the worker reports it failed.
	MaxDeliver int
	// How long JetStream waits for an ack before it redelivers. The worker
	// sends a heartbeat well inside this while it works.
	AckWait time.Duration
	// Delay before each retry after a transient error. The last entry repeats.
	Backoff []time.Duration
	// How long shutdown waits for tasks in flight.
	DrainTimeout time.Duration
}

// Worker consumes tasks.ingest.
type Worker struct {
	js    jetstream.JetStream
	svc   *core.Service
	log   logging.Logger
	kit   *redkit.Kit
	scope tracing.Scope
	cfg   Config
	now   func() time.Time
}

// New returns a Worker.
func New(js jetstream.JetStream, svc *core.Service, util *observability.Utility, kit *redkit.Kit, cfg Config) *Worker {
	return &Worker{
		js:    js,
		svc:   svc,
		log:   util.Logger.With("component", "worker"),
		kit:   kit,
		scope: tracing.Scope{Manager: util.TracerManager, TracerName: tracerName},
		cfg:   cfg,
		now:   time.Now,
	}
}

// Run consumes until ctx ends, then waits up to DrainTimeout for tasks in
// flight. A task still running after that is redelivered once its ack wait
// runs out.
func (w *Worker) Run(ctx context.Context) error {
	cons, err := w.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		Description:   "Ingest tasks.",
		FilterSubject: natsx.SubjectIngest,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       w.cfg.AckWait,
		MaxDeliver:    w.cfg.MaxDeliver,
		MaxAckPending: w.cfg.Concurrency,
	})
	if err != nil {
		return err
	}

	// Tasks keep running when ctx ends, so a shutdown doesn't abort a download
	// halfway.
	workCtx := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	sem := make(chan struct{}, w.cfg.Concurrency)
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			w.handle(workCtx, msg)
		})
	}, jetstream.PullMaxMessages(w.cfg.Concurrency))
	if err != nil {
		return err
	}
	w.log.Info(ctx, "consuming", "subject", natsx.SubjectIngest, "concurrency", w.cfg.Concurrency)

	<-ctx.Done()
	cc.Stop()
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(w.cfg.DrainTimeout):
		w.log.Warn(context.Background(), "stopped with tasks still running; JetStream will redeliver them")
	}
	return nil
}

func (w *Worker) handle(ctx context.Context, msg jetstream.Msg) {
	ctx = natsx.ExtractTrace(ctx, msg.Headers())
	attempt := 1
	if md, err := msg.Metadata(); err == nil {
		attempt = int(md.NumDelivered)
	}

	task := &gatewayv1.Task{}
	if err := natsx.Decode(msg.Data(), task); err != nil {
		// Without a task ID there's nobody to report to.
		w.log.Error(ctx, "dropping a message that isn't a valid Task", logging.Err(err))
		_ = msg.Term()
		return
	}
	meta := task.GetMeta()
	ctx = obs.WithTask(ctx, meta.GetJobId(), meta.GetTaskId())
	operation := core.AdapterName(core.SourceOf(task.GetIngest()))

	_ = w.scope.Manager.WithSpan(ctx, w.scope.TracerName, "ingest "+operation, func(ctx context.Context, span trace.Span) (err error) {
		span.SetAttributes(
			attribute.String("job.id", meta.GetJobId()),
			attribute.String("task.id", meta.GetTaskId()),
			attribute.Int("task.attempt", attempt),
		)
		done := w.kit.Start(ctx, operation)
		defer func() { done(err == nil) }()

		stopHeartbeat := heartbeat(msg, w.cfg.AckWait/3)
		ds, cacheHit, err := w.ingest(ctx, task)
		stopHeartbeat()

		report := &gatewayv1.StageDone{
			Meta:       meta,
			Attempt:    int32(attempt),
			CacheHit:   cacheHit,
			FinishedAt: timestamppb.New(w.now()),
		}
		if err == nil {
			report.Result = &gatewayv1.StageDone_Ingest{Ingest: ds}
			w.log.Info(ctx, "ingested", "files", len(ds.GetFiles()), "cache_hit", cacheHit)
			w.finish(ctx, msg, report)
			return nil
		}

		e := core.AsError(err)
		if e.Transient && attempt < w.cfg.MaxDeliver {
			delay := w.backoff(attempt)
			w.log.Warn(ctx, "retrying", logging.Err(err), "attempt", attempt, "delay", delay)
			_ = msg.NakWithDelay(delay)
			return err
		}
		w.log.Error(ctx, "task failed", logging.Err(err), "attempt", attempt, "code", e.Code.String())
		report.Result = &gatewayv1.StageDone_Error{Error: e.TaskError()}
		w.finish(ctx, msg, report)
		return err
	}, trace.WithSpanKind(trace.SpanKindConsumer))
}

func (w *Worker) ingest(ctx context.Context, task *gatewayv1.Task) (*ingestv1.RawDataset, bool, error) {
	spec := task.GetIngest()
	if spec == nil {
		return nil, false, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The job couldn't start.", "task on %s has no ingest spec", natsx.SubjectIngest)
	}
	return w.svc.Ingest(ctx, spec)
}

// finish publishes the report and acks the task. If the report can't be
// published, the task is NAKed so a later attempt reports it.
func (w *Worker) finish(ctx context.Context, msg jetstream.Msg, report *gatewayv1.StageDone) {
	pubCtx, cancel := context.WithTimeout(ctx, publishWait)
	defer cancel()
	msgID := report.GetMeta().GetTaskId() + ".done"
	if err := natsx.Publish(pubCtx, w.js, natsx.SubjectStageDone, msgID, report); err != nil {
		w.log.Error(ctx, "couldn't publish StageDone, will retry", logging.Err(err))
		_ = msg.NakWithDelay(w.backoff(1))
		return
	}
	if err := msg.DoubleAck(ctx); err != nil && !errors.Is(err, jetstream.ErrMsgAlreadyAckd) {
		// The report is out, so a redelivery only repeats it. StageDone uses
		// the same Nats-Msg-Id, and the gateway counts each task once.
		w.log.Warn(ctx, "ack failed after reporting", logging.Err(err))
	}
}

func (w *Worker) backoff(attempt int) time.Duration {
	if len(w.cfg.Backoff) == 0 {
		return 10 * time.Second
	}
	return w.cfg.Backoff[min(attempt-1, len(w.cfg.Backoff)-1)]
}

// heartbeat tells JetStream the task is still being worked on, every interval,
// until the returned func is called.
func heartbeat(msg jetstream.Msg, interval time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = msg.InProgress()
			}
		}
	}()
	return func() { close(done) }
}
