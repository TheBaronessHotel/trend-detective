package jobs

import (
	"context"
	"sync"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"google.golang.org/protobuf/proto"
)

// hub wakes Watch calls when a job changes. The gateway is the only writer
// of job state, so every change passes through it. With more than one
// gateway, a watcher on another instance would miss the wake-up, which is
// why Watch also re-reads the job every few seconds.
type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func newHub() *hub { return &hub{subs: map[string]map[chan struct{}]struct{}{}} }

func (h *hub) subscribe(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[id] == nil {
		h.subs[id] = map[chan struct{}]struct{}{}
	}
	h.subs[id][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs[id], ch)
		if len(h.subs[id]) == 0 {
			delete(h.subs, id)
		}
	}
}

// notify never blocks. A watcher that already has a wake-up pending doesn't
// need a second one, since it re-reads the whole job anyway.
func (h *hub) notify(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Watch calls send with the job as it is now, then again after every change,
// until the job finishes or ctx ends. It returns ErrNotFound for an unknown
// job, and nil when the job finished or ctx ended.
func (o *Orchestrator) Watch(ctx context.Context, id string, send func(*gatewayv1.Job) error) error {
	wake, stop := o.hub.subscribe(id)
	defer stop()
	poll := time.NewTicker(o.watchPoll)
	defer poll.Stop()

	var last []byte
	for {
		job, err := o.GetJob(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		snapshot, err := proto.MarshalOptions{Deterministic: true}.Marshal(job)
		if err != nil {
			return err
		}
		if string(snapshot) != string(last) {
			if err := send(job); err != nil {
				return err
			}
			last = snapshot
		}
		if isFinal(job.GetState()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-poll.C:
		}
	}
}

// Debug returns a job and every StageDone recorded for it, for /debug/jobs.
func (o *Orchestrator) Debug(ctx context.Context, id string) (*gatewayv1.Job, []*gatewayv1.StageDone, error) {
	job, err := o.GetJob(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var events []*gatewayv1.StageDone
	for _, stage := range []gatewayv1.Stage{gatewayv1.Stage_STAGE_INGEST, gatewayv1.Stage_STAGE_PREPROCESS, gatewayv1.Stage_STAGE_ANALYSIS} {
		dones, err := o.store.stageDones(ctx, id, stage)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, dones...)
	}
	return job, events, nil
}
