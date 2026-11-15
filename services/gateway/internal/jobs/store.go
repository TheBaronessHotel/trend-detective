package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/jobcancel"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Redis layout. Every key of a job shares the {id} hash tag, so a Lua script
// can touch all of them in a Redis Cluster too.
//
//	td:job:{id}              hash   spec, state, stage, results, created_at,
//	                                updated_at, total, succeeded, failed
//	td:job:{id}:tasks        hash   task ID -> TaskStatus (protojson)
//	td:job:{id}:done         hash   task ID -> StageDone (protojson), first one wins
//	td:job:{id}:stage:<n>    hash   total, finished
//	td:job:{id}:earthdata    string the user's own Earthdata token
func jobKey(id string) string            { return "td:job:{" + id + "}" }
func tasksKey(id string) string          { return jobKey(id) + ":tasks" }
func doneKey(id string) string           { return jobKey(id) + ":done" }
func stageKey(id string, n int32) string { return jobKey(id) + ":stage:" + strconv.Itoa(int(n)) }
func earthdataKey(id string) string      { return jobKey(id) + ":earthdata" }

// ErrNotFound means the job doesn't exist, or expired.
var ErrNotFound = errors.New("job not found")

// Store keeps job state in Redis. The gateway is its only writer.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewStore returns a Store whose keys expire after ttl.
func NewStore(rdb *redis.Client, ttl time.Duration) *Store {
	return &Store{rdb: rdb, ttl: ttl}
}

var marshal = protojson.MarshalOptions{}
var unmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}

// create writes a new job and its first stage's tasks.
func (s *Store) create(ctx context.Context, job *gatewayv1.Job, stage int32, tasks []*gatewayv1.TaskStatus) error {
	spec, err := marshal.Marshal(job.GetSpec())
	if err != nil {
		return err
	}
	id := job.GetId()
	now := ts(job.GetCreatedAt())
	_, err = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, jobKey(id),
			"spec", spec,
			"state", job.GetState().String(),
			"stage", stage,
			"created_at", now,
			"updated_at", now,
			"total", len(tasks), "succeeded", 0, "failed", 0,
		)
		if err := s.putTasks(ctx, p, id, tasks); err != nil {
			return err
		}
		p.HSet(ctx, stageKey(id, stage), "total", len(tasks), "finished", 0)
		for _, k := range []string{jobKey(id), tasksKey(id), stageKey(id, stage)} {
			p.Expire(ctx, k, s.ttl)
		}
		return nil
	})
	return err
}

func (s *Store) putTasks(ctx context.Context, p redis.Pipeliner, id string, tasks []*gatewayv1.TaskStatus) error {
	for _, t := range tasks {
		b, err := marshal.Marshal(t)
		if err != nil {
			return err
		}
		p.HSet(ctx, tasksKey(id), t.GetMeta().GetTaskId(), b)
	}
	return nil
}

// saveEarthdataToken stores a user's token for ingest to read. It returns the
// key, which goes in the IngestTask instead of the token.
func (s *Store) saveEarthdataToken(ctx context.Context, id, token string) (string, error) {
	k := earthdataKey(id)
	return k, s.rdb.Set(ctx, k, token, s.ttl).Err()
}

// recordDone stores a StageDone and counts its task once. It returns the
// stage's finished and total counts after this call. A repeated StageDone for
// the same task changes nothing but still reports the counts, so the caller
// can finish advancing a stage that a crash interrupted.
var recordDoneScript = redis.NewScript(`
local done, tasks, stage, job = KEYS[1], KEYS[2], KEYS[3], KEYS[4]
local task_id, done_json, status_json, outcome, now, ttl = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6]
if redis.call('EXISTS', job) == 0 then return {-1, -1} end
if redis.call('HGET', job, 'state') == 'JOB_STATE_CANCELED' then return {-2, -2} end
if redis.call('HSETNX', done, task_id, done_json) == 1 then
  redis.call('HSET', tasks, task_id, status_json)
  redis.call('HINCRBY', stage, 'finished', 1)
  redis.call('HINCRBY', job, outcome, 1)
  redis.call('HSET', job, 'updated_at', now)
  redis.call('PEXPIRE', done, ttl)
end
local finished = tonumber(redis.call('HGET', stage, 'finished') or '0')
local total = tonumber(redis.call('HGET', stage, 'total') or '0')
return {finished, total}
`)

func (s *Store) recordDone(ctx context.Context, d *gatewayv1.StageDone, status *gatewayv1.TaskStatus, now time.Time) (finished, total int64, err error) {
	doneJSON, err := marshal.Marshal(d)
	if err != nil {
		return 0, 0, err
	}
	statusJSON, err := marshal.Marshal(status)
	if err != nil {
		return 0, 0, err
	}
	outcome := "succeeded"
	if status.GetState() == gatewayv1.TaskState_TASK_STATE_FAILED {
		outcome = "failed"
	}
	meta := d.GetMeta()
	id := meta.GetJobId()
	res, err := recordDoneScript.Run(ctx, s.rdb,
		[]string{doneKey(id), tasksKey(id), stageKey(id, int32(meta.GetStage())), jobKey(id)},
		meta.GetTaskId(), doneJSON, statusJSON, outcome, now.UTC().Format(time.RFC3339Nano), s.ttl.Milliseconds(),
	).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	switch res[0] {
	case -1:
		return 0, 0, ErrNotFound
	case -2:
		return 0, 0, errCanceled
	}
	return res[0], res[1], nil
}

// recordProgress marks a task running and stores how far it has got. It
// reports whether anything changed. A task that's unknown, or already
// finished, is left alone, so a late update can't undo a StageDone.
func (s *Store) recordProgress(ctx context.Context, p *gatewayv1.TaskProgress, now time.Time) (bool, error) {
	meta := p.GetMeta()
	key, field := tasksKey(meta.GetJobId()), meta.GetTaskId()
	changed := false
	update := func(tx *redis.Tx) error {
		changed = false
		raw, err := tx.HGet(ctx, key, field).Result()
		if errors.Is(err, redis.Nil) {
			return nil
		}
		if err != nil {
			return err
		}
		t := &gatewayv1.TaskStatus{}
		if err := unmarshal.Unmarshal([]byte(raw), t); err != nil {
			return fmt.Errorf("decode task: %w", err)
		}
		if t.GetState() == gatewayv1.TaskState_TASK_STATE_SUCCEEDED || t.GetState() == gatewayv1.TaskState_TASK_STATE_FAILED {
			return nil
		}
		t.State = gatewayv1.TaskState_TASK_STATE_RUNNING
		if t.GetStartedAt() == nil {
			t.StartedAt = p.GetStartedAt()
			if t.StartedAt == nil {
				t.StartedAt = timestamppb.New(now)
			}
		}
		if p.Percent != nil {
			t.Percent = p.Percent
		}
		if p.GetActivity() != "" {
			t.Activity = p.GetActivity()
		}
		b, err := marshal.Marshal(t)
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, key, field, b)
			return nil
		})
		changed = err == nil
		return err
	}
	// StageDone writes the same hash; if it lands in between, read again.
	for range 3 {
		err := s.rdb.Watch(ctx, update, key)
		if !errors.Is(err, redis.TxFailedErr) {
			return changed, err
		}
	}
	return false, redis.TxFailedErr
}

// addStage creates a stage's tasks unless an earlier call already did, and
// moves the job to that stage. It reports whether it created them.
var addStageScript = redis.NewScript(`
local job, tasks, stage = KEYS[1], KEYS[2], KEYS[3]
local n, stage_num, now, ttl = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
if redis.call('HGET', job, 'state') == 'JOB_STATE_CANCELED' then return -1 end
if redis.call('HSETNX', stage, 'total', n) == 0 then return 0 end
redis.call('HSET', stage, 'finished', 0)
redis.call('PEXPIRE', stage, ttl)
for i = 5, #ARGV, 2 do
  redis.call('HSET', tasks, ARGV[i], ARGV[i + 1])
end
redis.call('HINCRBY', job, 'total', n)
redis.call('HSET', job, 'stage', stage_num, 'updated_at', now)
return 1
`)

func (s *Store) addStage(ctx context.Context, id string, stage int32, tasks []*gatewayv1.TaskStatus, now time.Time) (bool, error) {
	args := []any{len(tasks), stage, now.UTC().Format(time.RFC3339Nano), s.ttl.Milliseconds()}
	for _, t := range tasks {
		b, err := marshal.Marshal(t)
		if err != nil {
			return false, err
		}
		args = append(args, t.GetMeta().GetTaskId(), b)
	}
	created, err := addStageScript.Run(ctx, s.rdb, []string{jobKey(id), tasksKey(id), stageKey(id, stage)}, args...).Int()
	if created < 0 {
		return false, errCanceled
	}
	return created == 1, err
}

// finish sets a job's final state and results.
func (s *Store) finish(ctx context.Context, id string, state gatewayv1.JobState, results *gatewayv1.JobResults, now time.Time) error {
	return s.setResults(ctx, id, results, now, "state", state.String())
}

// setResultsScript writes job fields unless the job was cancelled, so a
// stage finishing late can't turn a cancelled job into a finished one.
var setResultsScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'state') == 'JOB_STATE_CANCELED' then return 0 end
redis.call('HSET', KEYS[1], unpack(ARGV))
return 1
`)

// setResults stores the job's results so far, plus any extra fields. It
// changes nothing on a cancelled job.
func (s *Store) setResults(ctx context.Context, id string, results *gatewayv1.JobResults, now time.Time, extra ...any) error {
	b, err := marshal.Marshal(results)
	if err != nil {
		return err
	}
	fields := append([]any{"results", b, "updated_at", now.UTC().Format(time.RFC3339Nano)}, extra...)
	return setResultsScript.Run(ctx, s.rdb, []string{jobKey(id)}, fields...).Err()
}

// cancel marks a job cancelled, and its unfinished tasks with it, and raises
// the flag stages check. It reports whether it changed anything: a job that
// already finished stays as it is.
func (s *Store) cancel(ctx context.Context, id string, now time.Time) (bool, error) {
	at := now.UTC().Format(time.RFC3339Nano)
	changed := false
	update := func(tx *redis.Tx) error {
		changed = false
		state, err := tx.HGet(ctx, jobKey(id), "state").Result()
		if errors.Is(err, redis.Nil) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if isFinal(gatewayv1.JobState(gatewayv1.JobState_value[state])) {
			return nil
		}
		raw, err := tx.HGetAll(ctx, tasksKey(id)).Result()
		if err != nil {
			return err
		}
		tasks := map[string]any{}
		for taskID, r := range raw {
			t := &gatewayv1.TaskStatus{}
			if err := unmarshal.Unmarshal([]byte(r), t); err != nil {
				return fmt.Errorf("decode task: %w", err)
			}
			if t.GetState() == gatewayv1.TaskState_TASK_STATE_SUCCEEDED || t.GetState() == gatewayv1.TaskState_TASK_STATE_FAILED {
				continue
			}
			t.State = gatewayv1.TaskState_TASK_STATE_CANCELED
			t.FinishedAt = timestamppb.New(now)
			t.Activity = ""
			b, err := marshal.Marshal(t)
			if err != nil {
				return err
			}
			tasks[taskID] = b
		}
		_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.HSet(ctx, jobKey(id), "state", gatewayv1.JobState_JOB_STATE_CANCELED.String(), "updated_at", at)
			if len(tasks) > 0 {
				p.HSet(ctx, tasksKey(id), tasks)
			}
			p.Set(ctx, jobcancel.Key(id), "1", s.ttl)
			return nil
		})
		changed = err == nil
		return err
	}
	// A StageDone landing in between changes the job; read it again.
	for range 3 {
		err := s.rdb.Watch(ctx, update, jobKey(id), tasksKey(id))
		if !errors.Is(err, redis.TxFailedErr) {
			return changed, err
		}
	}
	return false, redis.TxFailedErr
}

// stageDones returns every StageDone recorded for a stage, by task ID.
func (s *Store) stageDones(ctx context.Context, id string, stage gatewayv1.Stage) ([]*gatewayv1.StageDone, error) {
	all, err := s.rdb.HGetAll(ctx, doneKey(id)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]*gatewayv1.StageDone, 0, len(all))
	for _, raw := range all {
		d := &gatewayv1.StageDone{}
		if err := unmarshal.Unmarshal([]byte(raw), d); err != nil {
			return nil, fmt.Errorf("decode StageDone: %w", err)
		}
		if d.GetMeta().GetStage() == stage {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b *gatewayv1.StageDone) int {
		return strings.Compare(a.GetMeta().GetTaskId(), b.GetMeta().GetTaskId())
	})
	return out, nil
}

// load reads a whole job.
// runningJobIDs lists the jobs whose state is running. It scans the job
// keys, which is fine at a demo's scale of hundreds of jobs a week.
func (s *Store) runningJobIDs(ctx context.Context) ([]string, error) {
	var ids []string
	iter := s.rdb.Scan(ctx, 0, "td:job:{*}", 500).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		// Only the job hashes, not their :tasks, :done or :stage:n keys.
		if !strings.HasSuffix(key, "}") {
			continue
		}
		state, err := s.rdb.HGet(ctx, key, "state").Result()
		if err != nil || state != gatewayv1.JobState_JOB_STATE_RUNNING.String() {
			continue
		}
		ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(key, "td:job:{"), "}"))
	}
	return ids, iter.Err()
}

func (s *Store) load(ctx context.Context, id string) (*gatewayv1.Job, int32, error) {
	h, err := s.rdb.HGetAll(ctx, jobKey(id)).Result()
	if err != nil {
		return nil, 0, err
	}
	if len(h) == 0 {
		return nil, 0, ErrNotFound
	}
	job := &gatewayv1.Job{
		Id:             id,
		State:          gatewayv1.JobState(gatewayv1.JobState_value[h["state"]]),
		Spec:           &gatewayv1.JobSpec{},
		Results:        &gatewayv1.JobResults{},
		TasksTotal:     atoi(h["total"]),
		TasksSucceeded: atoi(h["succeeded"]),
		TasksFailed:    atoi(h["failed"]),
		CreatedAt:      parseTS(h["created_at"]),
		UpdatedAt:      parseTS(h["updated_at"]),
	}
	if err := unmarshal.Unmarshal([]byte(h["spec"]), job.Spec); err != nil {
		return nil, 0, fmt.Errorf("decode spec: %w", err)
	}
	if r := h["results"]; r != "" {
		if err := unmarshal.Unmarshal([]byte(r), job.Results); err != nil {
			return nil, 0, fmt.Errorf("decode results: %w", err)
		}
	}

	tasks, err := s.rdb.HGetAll(ctx, tasksKey(id)).Result()
	if err != nil {
		return nil, 0, err
	}
	for _, raw := range tasks {
		t := &gatewayv1.TaskStatus{}
		if err := unmarshal.Unmarshal([]byte(raw), t); err != nil {
			return nil, 0, fmt.Errorf("decode task: %w", err)
		}
		job.Tasks = append(job.Tasks, t)
	}
	slices.SortFunc(job.Tasks, func(a, b *gatewayv1.TaskStatus) int {
		if c := int(a.GetMeta().GetStage()) - int(b.GetMeta().GetStage()); c != 0 {
			return c
		}
		return strings.Compare(a.GetMeta().GetTaskId(), b.GetMeta().GetTaskId())
	})
	return job, atoi(h["stage"]), nil
}

func ts(t *timestamppb.Timestamp) string { return t.AsTime().UTC().Format(time.RFC3339Nano) }

func parseTS(s string) *timestamppb.Timestamp {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

func atoi(s string) int32 {
	n, _ := strconv.Atoi(s)
	return int32(n)
}
