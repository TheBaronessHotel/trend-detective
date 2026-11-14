package events

import (
	"context"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/natsx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/nats-io/nats.go/jetstream"
)

const progressDurable = "gateway-progress"

// RunProgress consumes events.task.progress until ctx ends. Updates are
// handled one at a time, in order, so a task's percent never steps back.
// Progress is best effort: an update that still can't be recorded after a
// few deliveries is dropped, and the task's StageDone settles it anyway.
func RunProgress(ctx context.Context, js jetstream.JetStream, orch *jobs.Orchestrator, log logging.Logger) error {
	log = log.With("component", "progress")
	cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       progressDurable,
		Description:   "TaskProgress events for the gateway.",
		FilterSubject: natsx.SubjectTaskProgress,
		// Progress from before the gateway first ran describes nothing now.
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       10 * time.Second,
		MaxDeliver:    3,
	})
	if err != nil {
		return err
	}
	work := context.WithoutCancel(ctx)
	cc, err := cons.Consume(func(msg jetstream.Msg) { handleProgress(work, orch, log, msg) })
	if err != nil {
		return err
	}
	log.Info(ctx, "consuming", "subject", natsx.SubjectTaskProgress)
	<-ctx.Done()
	cc.Stop()
	return nil
}

func handleProgress(ctx context.Context, orch *jobs.Orchestrator, log logging.Logger, msg jetstream.Msg) {
	ctx = natsx.ExtractTrace(ctx, msg.Headers())
	p := &gatewayv1.TaskProgress{}
	if err := natsx.Decode(msg.Data(), p); err != nil {
		log.Warn(ctx, "dropping an event that isn't a valid TaskProgress", logging.Err(err))
		_ = msg.Term()
		return
	}
	ctx = obs.WithTask(ctx, p.GetMeta().GetJobId(), p.GetMeta().GetTaskId())
	if err := orch.OnTaskProgress(ctx, p); err != nil {
		log.Warn(ctx, "couldn't record progress", logging.Err(err))
		_ = msg.NakWithDelay(time.Second)
		return
	}
	_ = msg.Ack()
}
