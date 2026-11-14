// Package events consumes StageDone events from the EVENTS stream and hands
// them to the orchestrator.
package events

import (
	"context"
	"sync"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/natsx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	stream  = "EVENTS"
	durable = "gateway"
)

// Run consumes events.stage.done until ctx ends. An event is acked only after
// the orchestrator has handled it. If that fails, for example because Redis is
// down, the event is redelivered with a growing delay and never dropped.
func Run(ctx context.Context, js jetstream.JetStream, orch *jobs.Orchestrator, log logging.Logger, concurrency int) error {
	log = log.With("component", "events")
	cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		Description:   "StageDone events for the gateway.",
		FilterSubject: natsx.SubjectStageDone,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    -1,
		MaxAckPending: concurrency,
	})
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			handle(context.WithoutCancel(ctx), orch, log, msg)
		})
	}, jetstream.PullMaxMessages(concurrency))
	if err != nil {
		return err
	}
	log.Info(ctx, "consuming", "subject", natsx.SubjectStageDone)

	<-ctx.Done()
	cc.Stop()
	wg.Wait()
	return nil
}

func handle(ctx context.Context, orch *jobs.Orchestrator, log logging.Logger, msg jetstream.Msg) {
	ctx = natsx.ExtractTrace(ctx, msg.Headers())
	d := &gatewayv1.StageDone{}
	if err := natsx.Decode(msg.Data(), d); err != nil {
		log.Error(ctx, "dropping an event that isn't a valid StageDone", logging.Err(err))
		_ = msg.Term()
		return
	}
	ctx = obs.WithTask(ctx, d.GetMeta().GetJobId(), d.GetMeta().GetTaskId())
	if err := orch.OnStageDone(ctx, d); err != nil {
		attempt := 1
		if md, err := msg.Metadata(); err == nil {
			attempt = int(md.NumDelivered)
		}
		delay := min(time.Duration(attempt)*2*time.Second, 30*time.Second)
		log.Error(ctx, "couldn't handle StageDone, retrying", logging.Err(err), "attempt", attempt, "delay", delay)
		_ = msg.NakWithDelay(delay)
		return
	}
	_ = msg.Ack()
}
