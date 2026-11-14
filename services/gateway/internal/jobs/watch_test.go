package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
)

func TestWatchSendsEachChangeAndEndsWhenTheJobFinishes(t *testing.T) {
	h := newHarness(t)
	// Only the hub can wake Watch in time, so this tests the hub.
	h.o.watchPoll = time.Hour
	ctx := context.Background()
	s := spec()
	s.StopAfter = gatewayv1.Stage_STAGE_INGEST
	job, err := h.o.CreateJob(ctx, s, "")
	if err != nil {
		t.Fatal(err)
	}

	sent := make(chan *gatewayv1.Job, 10)
	errc := make(chan error, 1)
	go func() {
		errc <- h.o.Watch(ctx, job.GetId(), func(j *gatewayv1.Job) error { sent <- j; return nil })
	}()

	first := <-sent
	if first.GetState() != gatewayv1.JobState_JOB_STATE_RUNNING {
		t.Fatalf("first update: %v", first.GetState())
	}
	if err := h.o.OnStageDone(ctx, done(h.pub.last(t).task)); err != nil {
		t.Fatal(err)
	}
	select {
	case last := <-sent:
		if last.GetState() != gatewayv1.JobState_JOB_STATE_SUCCEEDED {
			t.Fatalf("second update: %v", last.GetState())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch didn't send the change")
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch didn't end after the job finished")
	}
}

func TestWatchUnknownJob(t *testing.T) {
	h := newHarness(t)
	err := h.o.Watch(context.Background(), "nope", func(*gatewayv1.Job) error { return nil })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestWatchStopsWhenTheClientLeaves(t *testing.T) {
	h := newHarness(t)
	job, err := h.o.CreateJob(context.Background(), spec(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- h.o.Watch(ctx, job.GetId(), func(*gatewayv1.Job) error { return nil })
	}()
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("want nil after the client left, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch kept running after its context ended")
	}
}

// A job can sit in NASA's queue for many minutes with nothing to report.
// Resending it keeps the stream from looking idle to clients and proxies.
func TestWatchResendsAQuietJob(t *testing.T) {
	h := newHarness(t)
	h.o.watchPoll = 10 * time.Millisecond
	h.o.watchKeepalive = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job, err := h.o.CreateJob(ctx, spec(), "")
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan *gatewayv1.Job, 100)
	go func() { _ = h.o.Watch(ctx, job.GetId(), func(j *gatewayv1.Job) error { sent <- j; return nil }) }()
	<-sent
	select {
	case again := <-sent:
		if again.GetId() != job.GetId() {
			t.Errorf("resent a different job: %v", again.GetId())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no keepalive while the job was quiet")
	}
}
