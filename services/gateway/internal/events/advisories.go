package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/natsx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	advisoryStream  = "ADVISORIES"
	advisoryDurable = "gateway-advisories"
	tasksStream     = "TASKS"
)

// maxDeliveries is the part of NATS's max-deliveries advisory the gateway
// needs.
type maxDeliveries struct {
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	StreamSeq  uint64 `json:"stream_seq"`
	Deliveries uint64 `json:"deliveries"`
}

// taskStore is the part of the TASKS stream the handler uses.
type taskStore interface {
	GetMsg(ctx context.Context, seq uint64, opts ...jetstream.GetMsgOpt) (*jetstream.RawStreamMsg, error)
	DeleteMsg(ctx context.Context, seq uint64) error
}

// RunAdvisories consumes the ADVISORIES stream until ctx ends.
//
// NATS publishes a max-deliveries advisory when a TASKS message runs out of
// deliveries. That usually means the stage crashed on every attempt and never
// reported. The gateway reads the task, records it as failed so the job
// doesn't hang, and deletes the dead message from TASKS.
func RunAdvisories(ctx context.Context, js jetstream.JetStream, orch *jobs.Orchestrator, log logging.Logger) error {
	log = log.With("component", "advisories")
	tasks, err := js.Stream(ctx, tasksStream)
	if err != nil {
		return err
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, advisoryStream, jetstream.ConsumerConfig{
		Durable:     advisoryDurable,
		Description: "Max-deliveries advisories for the gateway.",
		AckPolicy:   jetstream.AckExplicitPolicy,
		AckWait:     30 * time.Second,
		MaxDeliver:  -1,
	})
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		wg.Go(func() { handleAdvisory(context.WithoutCancel(ctx), orch, tasks, log, msg) })
	}, jetstream.PullMaxMessages(4))
	if err != nil {
		return err
	}
	log.Info(ctx, "consuming", "stream", advisoryStream)
	<-ctx.Done()
	cc.Stop()
	wg.Wait()
	return nil
}

func handleAdvisory(ctx context.Context, orch *jobs.Orchestrator, tasks taskStore, log logging.Logger, msg jetstream.Msg) {
	var adv maxDeliveries
	if err := json.Unmarshal(msg.Data(), &adv); err != nil || adv.StreamSeq == 0 {
		log.Error(ctx, "dropping an advisory the gateway can't read", logging.Err(err))
		_ = msg.Term()
		return
	}
	raw, err := tasks.GetMsg(ctx, adv.StreamSeq)
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		// Already gone, for example acked by a late attempt. Nothing to do.
		_ = msg.Ack()
		return
	}
	if err != nil {
		log.Error(ctx, "couldn't read the dead task, retrying", logging.Err(err), "seq", adv.StreamSeq)
		_ = msg.NakWithDelay(5 * time.Second)
		return
	}

	done, err := exhausted(adv, raw.Data, time.Now())
	if err != nil {
		log.Error(ctx, "dead task isn't a valid Task, deleting it", logging.Err(err), "seq", adv.StreamSeq)
		_ = tasks.DeleteMsg(ctx, adv.StreamSeq)
		_ = msg.Ack()
		return
	}
	ctx = obs.WithTask(ctx, done.GetMeta().GetJobId(), done.GetMeta().GetTaskId())
	if err := orch.OnStageDone(ctx, done); err != nil {
		log.Error(ctx, "couldn't fail the dead task, retrying", logging.Err(err))
		_ = msg.NakWithDelay(5 * time.Second)
		return
	}
	log.Warn(ctx, "task ran out of deliveries", "consumer", adv.Consumer, "deliveries", adv.Deliveries)
	_ = tasks.DeleteMsg(ctx, adv.StreamSeq)
	_ = msg.Ack()
}

// exhausted builds the StageDone the stage never sent.
func exhausted(adv maxDeliveries, taskData []byte, now time.Time) (*gatewayv1.StageDone, error) {
	task := &gatewayv1.Task{}
	if err := natsx.Decode(taskData, task); err != nil {
		return nil, err
	}
	if task.GetMeta().GetTaskId() == "" {
		return nil, errors.New("task has no ID")
	}
	return &gatewayv1.StageDone{
		Meta:       task.GetMeta(),
		Attempt:    int32(adv.Deliveries),
		FinishedAt: timestamppb.New(now),
		Result: &gatewayv1.StageDone_Error{Error: &gatewayv1.TaskError{
			Code:        gatewayv1.ErrorCode_ERROR_CODE_RETRIES_EXHAUSTED,
			Retryable:   true,
			UserMessage: "This step kept failing, so the job stopped. Try again in a few minutes.",
			Detail:      fmt.Sprintf("%s consumer %q gave up after %d deliveries", adv.Stream, adv.Consumer, adv.Deliveries),
		}},
	}, nil
}
