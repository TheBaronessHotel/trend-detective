package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
)

func TestFailStuckFailsAStageThatNeverFinishes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	h.o.now = func() time.Time { return start }
	h.o.limits.IngestDeadline = 3 * time.Hour
	h.o.limits.StageDeadline = 30 * time.Minute

	job, err := h.o.CreateJob(ctx, spec(), "")
	if err != nil {
		t.Fatal(err)
	}
	// Ingest finishes; preprocess is queued and nobody ever runs it.
	if err := h.o.OnStageDone(ctx, &gatewayv1.StageDone{
		Meta:   &gatewayv1.TaskMeta{JobId: job.GetId(), TaskId: TaskID(job.GetId(), gatewayv1.Stage_STAGE_INGEST, 0), Stage: gatewayv1.Stage_STAGE_INGEST},
		Result: &gatewayv1.StageDone_Ingest{Ingest: &ingestv1.RawDataset{}},
	}); err != nil {
		t.Fatal(err)
	}

	// Before the deadline nothing happens.
	h.o.now = func() time.Time { return start.Add(29 * time.Minute) }
	if n, err := h.o.FailStuck(ctx); err != nil || n != 0 {
		t.Fatalf("before the deadline: failed %d, err %v", n, err)
	}

	// After it, the job fails with a message for people.
	h.o.now = func() time.Time { return start.Add(31 * time.Minute) }
	if n, err := h.o.FailStuck(ctx); err != nil || n != 1 {
		t.Fatalf("after the deadline: failed %d, err %v", n, err)
	}
	got, err := h.o.GetJob(ctx, job.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if got.GetState() != gatewayv1.JobState_JOB_STATE_FAILED {
		t.Fatalf("state %v", got.GetState())
	}
	var msg string
	for _, task := range got.GetTasks() {
		if e := task.GetError(); e != nil {
			msg = e.GetUserMessage()
		}
	}
	if !strings.Contains(msg, "30 minutes") || !strings.Contains(msg, "try again") {
		t.Errorf("user message: %q", msg)
	}

	// A finished job is left alone, and a late real report changes nothing.
	if n, _ := h.o.FailStuck(ctx); n != 0 {
		t.Errorf("failed a finished job again: %d", n)
	}
}

func TestIngestGetsTheLongerDeadline(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	h.o.now = func() time.Time { return start }
	h.o.limits.IngestDeadline = 3 * time.Hour
	h.o.limits.StageDeadline = 30 * time.Minute
	if _, err := h.o.CreateJob(ctx, spec(), ""); err != nil {
		t.Fatal(err)
	}
	// An AppEEARS queue can run for two hours; that's not stuck.
	h.o.now = func() time.Time { return start.Add(2 * time.Hour) }
	if n, _ := h.o.FailStuck(ctx); n != 0 {
		t.Errorf("failed an ingest stage after 2 hours: %d", n)
	}
	h.o.now = func() time.Time { return start.Add(3*time.Hour + time.Minute) }
	if n, _ := h.o.FailStuck(ctx); n != 1 {
		t.Errorf("didn't fail an ingest stage after 3 hours: %d", n)
	}
}

func TestDurationWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Minute: "30 minutes",
		3 * time.Hour:    "3 hours",
		time.Hour:        "1 hour",
		90 * time.Minute: "90 minutes",
		45 * time.Second: "45 seconds",
	} {
		if got := durationWords(d); got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
	}
}
