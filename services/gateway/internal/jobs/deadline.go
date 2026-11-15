package jobs

import (
	"context"
	"fmt"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FailStuck fails running jobs whose current stage has gone on longer than
// its deadline: Limits.IngestDeadline for ingest, which already gives up at
// NASA on its own, and Limits.StageDeadline for the rest. A worker that's
// down, or a task message that was lost, would otherwise leave a job running,
// and its WatchJob stream open, forever. Each unfinished task gets a
// StageDone with an error, as if its worker had sent it, so the job fails
// the usual way; a real report arriving later changes nothing. It returns
// how many jobs it failed.
func (o *Orchestrator) FailStuck(ctx context.Context) (int, error) {
	ids, err := o.store.runningJobIDs(ctx)
	if err != nil {
		return 0, err
	}
	now := o.now()
	failed := 0
	for _, id := range ids {
		job, current, err := o.store.load(ctx, id)
		if err != nil || job.GetState() != gatewayv1.JobState_JOB_STATE_RUNNING {
			continue
		}
		stage := gatewayv1.Stage(current)
		limit := o.limits.StageDeadline
		if stage == gatewayv1.Stage_STAGE_INGEST {
			limit = o.limits.IngestDeadline
		}
		since := job.GetUpdatedAt().AsTime()
		if limit <= 0 || job.GetUpdatedAt() == nil || now.Sub(since) < limit {
			continue
		}
		jctx := obs.WithJob(ctx, id)
		for _, t := range job.GetTasks() {
			if t.GetMeta().GetStage() != stage || t.GetState() == gatewayv1.TaskState_TASK_STATE_SUCCEEDED || t.GetState() == gatewayv1.TaskState_TASK_STATE_FAILED {
				continue
			}
			d := &gatewayv1.StageDone{
				Meta:       t.GetMeta(),
				Attempt:    t.GetAttempts(),
				FinishedAt: timestamppb.New(now),
				Result: &gatewayv1.StageDone_Error{Error: &gatewayv1.TaskError{
					Code:      gatewayv1.ErrorCode_ERROR_CODE_INTERNAL,
					Retryable: true,
					UserMessage: fmt.Sprintf("This step didn't finish within %s, so the job stopped. A service may be down; try again in a few minutes.",
						durationWords(limit)),
					Detail: fmt.Sprintf("no StageDone for %s within %s of the stage starting at %s", t.GetMeta().GetTaskId(), limit, since.Format(time.RFC3339)),
				}},
			}
			if err := o.OnStageDone(jctx, d); err != nil {
				o.log.Error(jctx, "couldn't fail a stuck task", logging.Err(err))
				continue
			}
		}
		o.log.Warn(jctx, "failed a stuck job", "stage", stageName(stage), "after", limit.String())
		failed++
	}
	return failed, nil
}

// WatchDeadlines runs FailStuck every interval until ctx ends.
func (o *Orchestrator) WatchDeadlines(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := o.FailStuck(ctx); err != nil && ctx.Err() == nil {
				o.log.Error(ctx, "couldn't check for stuck jobs", logging.Err(err))
			}
		}
	}
}

// durationWords says a deadline for people: "30 minutes", "3 hours".
func durationWords(d time.Duration) string {
	unit := func(n int, word string) string {
		if n == 1 {
			return "1 " + word
		}
		return fmt.Sprintf("%d %ss", n, word)
	}
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return unit(int(d/time.Hour), "hour")
	case d >= time.Minute:
		return unit(int(d.Round(time.Minute)/time.Minute), "minute")
	}
	return unit(int(d.Round(time.Second)/time.Second), "second")
}
