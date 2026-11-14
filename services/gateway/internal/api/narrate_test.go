package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1/gatewayv1connect"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1/narratorv1connect"
	preprocessv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/preprocess/v1"
	"github.com/TheBaronessHotel/trend-detective/services/gateway"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/narrate"
	"github.com/alicebob/miniredis/v2"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

// secretKey must never show up in logs or errors.
const secretKey = "sk-test-SECRET-0123456789"

var analysisResult = &analysisv1.AnalysisResult{
	Series: &commonv1.BlobRef{Bucket: "analysis", Key: "jobs/x/series.json"},
	Grid:   &commonv1.BlobRef{Bucket: "analysis", Key: "jobs/x/grid.json"},
}

// fakeNarrator is a NarratorService that records each call and replays
// events, then an optional error.
type fakeNarrator struct {
	events []*narratorv1.NarrateEvent
	err    error
	// block waits for the caller to go away before returning.
	block bool

	mu      sync.Mutex
	calls   int
	got     *narratorv1.NarrateRequest
	gotKey  string
	hasKey  bool
	h2Calls int
}

func (f *fakeNarrator) Narrate(ctx context.Context, req *narratorv1.NarrateRequest, stream narratorv1connect.NarratorServiceNarrateServerStream) error {
	f.mu.Lock()
	f.calls++
	f.got = req
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		vals := info.RequestHeader().Values(narrate.APIKeyHeader)
		f.hasKey = len(vals) > 0
		f.gotKey = info.RequestHeader().Get(narrate.APIKeyHeader)
	}
	f.mu.Unlock()
	for _, ev := range f.events {
		if err := stream.Send(&narratorv1.NarrateResponse{Event: ev}); err != nil {
			return err
		}
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

// startNarrator serves f over h2c, as the real narrator does, and counts
// HTTP/2 requests.
func startNarrator(t *testing.T, f *fakeNarrator) string {
	t.Helper()
	cs := connect.NewServer()
	narratorv1connect.RegisterNarratorServiceHandler(cs, f)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, cs)
	hs := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 {
			f.mu.Lock()
			f.h2Calls++
			f.mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	}))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	hs.Config.Protocols = protocols
	hs.Start()
	t.Cleanup(hs.Close)
	return hs.URL
}

// captureCore keeps every log record as text.
type captureCore struct {
	mu    *sync.Mutex
	lines *[]string
	attrs []logging.Attr
}

func newCapture() (*captureCore, logging.Logger) {
	c := &captureCore{mu: &sync.Mutex{}, lines: &[]string{}}
	return c, logging.New(c)
}

func (c *captureCore) Enabled(context.Context, logging.Level) bool { return true }

func (c *captureCore) Log(_ context.Context, level logging.Level, msg string, attrs ...logging.Attr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.lines = append(*c.lines, fmt.Sprintf("%v %s %+v %+v", level, msg, c.attrs, attrs))
}

func (c *captureCore) With(attrs ...logging.Attr) logging.Core {
	return &captureCore{mu: c.mu, lines: c.lines, attrs: append(append([]logging.Attr{}, c.attrs...), attrs...)}
}

func (c *captureCore) WithGroup(string) logging.Core { return c }

func (c *captureCore) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(*c.lines, "\n")
}

type narrateEnv struct {
	orch     *jobs.Orchestrator
	narrator *fakeNarrator
	logs     *captureCore
	client   gatewayv1connect.GatewayServiceClient
}

// newNarrateEnv runs a gateway whose narrator lives at narratorURL, or at a
// fresh fake narrator when narratorURL is empty.
func newNarrateEnv(t *testing.T, narratorURL string, timeout time.Duration) *narrateEnv {
	t.Helper()
	e := &narrateEnv{narrator: &fakeNarrator{}}
	if narratorURL == "" {
		narratorURL = startNarrator(t, e.narrator)
	}
	mr := miniredis.RunT(t)
	cat, err := catalog.Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	var log logging.Logger
	e.logs, log = newCapture()
	e.orch = jobs.New(cat, jobs.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour),
		nopPublisher{}, jobs.Limits{SubregionN: 8, MaxGrid: 128}, log)
	nc, err := narrate.NewClient(narratorURL)
	if err != nil {
		t.Fatal(err)
	}
	cs := connect.NewServer()
	gatewayv1connect.RegisterGatewayServiceHandler(cs, New(cat, e.orch, nil, narrate.New(e.orch, cat, nc, timeout, log), nil, log))
	mux := http.NewServeMux()
	connecthttp.Mount(mux, cs)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	e.client = gatewayv1connect.NewGatewayServiceClient(connect.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL)))
	return e
}

var jobSpec = &gatewayv1.JobSpec{
	VariableId: "air-temperature-power",
	Bbox:       &commonv1.BBox{West: 77, South: 28, East: 78, North: 29},
	Dates: &commonv1.DateRange{
		Start: &commonv1.Date{Year: 1981, Month: 1, Day: 1},
		End:   &commonv1.Date{Year: 1990, Month: 12, Day: 31},
	},
}

// job creates a job and reports stages for it, up to and including upTo. A
// stage of STAGE_UNSPECIFIED reports none. fail makes the last report a
// failure.
func (e *narrateEnv) job(t *testing.T, spec *gatewayv1.JobSpec, upTo gatewayv1.Stage, fail bool) string {
	t.Helper()
	ctx := context.Background()
	job, err := e.orch.CreateJob(ctx, spec, "")
	if err != nil {
		t.Fatal(err)
	}
	id := job.GetId()
	results := map[gatewayv1.Stage]*gatewayv1.StageDone{
		gatewayv1.Stage_STAGE_INGEST: {Result: &gatewayv1.StageDone_Ingest{Ingest: &ingestv1.RawDataset{}}},
		gatewayv1.Stage_STAGE_PREPROCESS: {Result: &gatewayv1.StageDone_Preprocess{Preprocess: &preprocessv1.PreprocessResult{
			Series: &commonv1.BlobRef{Bucket: "prep", Key: "series/abc.json"},
		}}},
		gatewayv1.Stage_STAGE_ANALYSIS: {Result: &gatewayv1.StageDone_Analysis{Analysis: proto.CloneOf(analysisResult)}},
	}
	for stage := gatewayv1.Stage_STAGE_INGEST; stage <= upTo; stage++ {
		d := results[stage]
		if fail && stage == upTo {
			d = &gatewayv1.StageDone{Result: &gatewayv1.StageDone_Error{Error: &gatewayv1.TaskError{
				Code: gatewayv1.ErrorCode_ERROR_CODE_RETRIES_EXHAUSTED, Detail: "gave up",
			}}}
		}
		d.Meta = &gatewayv1.TaskMeta{JobId: id, TaskId: jobs.TaskID(id, stage, 0), Stage: stage}
		if err := e.orch.OnStageDone(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

type narrateResult struct {
	events []*narratorv1.NarrateEvent
	err    error
}

func (e *narrateEnv) call(t *testing.T, req *gatewayv1.NarrateRequest, key string) narrateResult {
	t.Helper()
	ctx, info := connect.NewClientContext(t.Context())
	if key != "" {
		info.RequestHeader().Set("x-llm-api-key", key)
	}
	var r narrateResult
	stream, err := e.client.Narrate(ctx, req)
	if err != nil {
		r.err = err
		return r
	}
	defer stream.Close()
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return r
		}
		if err != nil {
			r.err = err
			return r
		}
		r.events = append(r.events, msg.GetEvent())
	}
}

func (e *narrateEnv) checkNoKeyLeak(t *testing.T, r narrateResult) {
	t.Helper()
	if r.err != nil && strings.Contains(r.err.Error(), secretKey) {
		t.Errorf("the key leaked into the error: %v", r.err)
	}
	if logs := e.logs.text(); strings.Contains(logs, secretKey) || strings.Contains(logs, "SECRET") {
		t.Errorf("the key leaked into the logs:\n%s", logs)
	}
}

func textEvent(s string) *narratorv1.NarrateEvent {
	return &narratorv1.NarrateEvent{Event: &narratorv1.NarrateEvent_TextDelta{TextDelta: s}}
}

func doneEvent(model string, unverified ...string) *narratorv1.NarrateEvent {
	return &narratorv1.NarrateEvent{Event: &narratorv1.NarrateEvent_Done{Done: &narratorv1.NarrateDone{Model: model, UnverifiedNumbers: unverified}}}
}

func TestNarrateRelaysTheStream(t *testing.T) {
	e := newNarrateEnv(t, "", time.Minute)
	e.narrator.events = []*narratorv1.NarrateEvent{textEvent("Air got warmer "), textEvent("by 0.3 °C per decade."), doneEvent("gpt-5.5", "9.9")}
	id := e.job(t, jobSpec, gatewayv1.Stage_STAGE_ANALYSIS, false)
	req := &gatewayv1.NarrateRequest{
		JobId:    id,
		Question: "Is that a lot?",
		History: []*narratorv1.ChatMessage{
			{Role: narratorv1.Role_ROLE_USER, Text: "What changed?"},
			{Role: narratorv1.Role_ROLE_ASSISTANT, Text: "It got warmer."},
		},
		Llm: &narratorv1.LlmConfig{Provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT, Model: "gpt-5.5", BaseUrl: "https://openrouter.ai/api/v1"},
	}

	r := e.call(t, req, secretKey)
	if r.err != nil {
		t.Fatalf("Narrate: %v", r.err)
	}
	if len(r.events) != len(e.narrator.events) {
		t.Fatalf("got %d events, want %d", len(r.events), len(e.narrator.events))
	}
	for i := range r.events {
		if !proto.Equal(r.events[i], e.narrator.events[i]) {
			t.Errorf("event %d = %v, want %v", i, r.events[i], e.narrator.events[i])
		}
	}

	got := e.narrator.got
	cat, _ := catalog.Parse(gateway.CatalogYAML)
	entry, _ := cat.Get("air-temperature-power")
	wantVar := &narratorv1.VariableContext{
		Id: "air-temperature-power", Name: entry.Variable.GetName(), Definition: entry.Variable.GetDefinition(),
		Units: entry.Variable.GetUnits(), LlmHints: entry.LLMHints,
	}
	if wantVar.Name == "" || wantVar.Definition == "" || wantVar.Units == "" || wantVar.LlmHints == "" {
		t.Fatalf("the catalog entry lacks fields this test relies on: %v", wantVar)
	}
	want := &narratorv1.NarrateRequest{
		Result:   analysisResult,
		Variable: wantVar,
		Bbox:     jobSpec.GetBbox(),
		Dates:    jobSpec.GetDates(),
		Question: req.GetQuestion(),
		History:  req.GetHistory(),
		Llm:      req.GetLlm(),
	}
	if !proto.Equal(got, want) {
		t.Errorf("narrator request\n got %v\nwant %v", got, want)
	}
	if e.narrator.gotKey != secretKey {
		t.Errorf("the key was not forwarded (header set: %v)", e.narrator.hasKey)
	}
	if e.narrator.h2Calls != 1 {
		t.Errorf("want one HTTP/2 (h2c) call to the narrator, got %d", e.narrator.h2Calls)
	}
	if logs := e.logs.text(); !strings.Contains(logs, "narrated") || !strings.Contains(logs, id) {
		t.Errorf("want a narrated log record for the job, got:\n%s", logs)
	}
	e.checkNoKeyLeak(t, r)
}

func TestNarrateWithoutKeySendsNoHeader(t *testing.T) {
	e := newNarrateEnv(t, "", time.Minute)
	e.narrator.events = []*narratorv1.NarrateEvent{textEvent("Overview."), doneEvent("template")}
	id := e.job(t, jobSpec, gatewayv1.Stage_STAGE_ANALYSIS, false)
	r := e.call(t, &gatewayv1.NarrateRequest{JobId: id}, "")
	if r.err != nil || len(r.events) != 2 {
		t.Fatalf("Narrate = %v, %d events", r.err, len(r.events))
	}
	if e.narrator.hasKey {
		t.Errorf("an empty key must not be sent as a header, got %q", e.narrator.gotKey)
	}
}

func TestNarrateRefusesUnfinishedJobs(t *testing.T) {
	stopAtPreprocess := proto.CloneOf(jobSpec)
	stopAtPreprocess.StopAfter = gatewayv1.Stage_STAGE_PREPROCESS
	tests := []struct {
		name   string
		spec   *gatewayv1.JobSpec
		upTo   gatewayv1.Stage
		fail   bool
		wantIn string
	}{
		{name: "just created", spec: jobSpec, upTo: gatewayv1.Stage_STAGE_UNSPECIFIED, wantIn: "isn't finished yet"},
		{name: "preprocessed, analysis running", spec: jobSpec, upTo: gatewayv1.Stage_STAGE_PREPROCESS, wantIn: "isn't finished yet"},
		{name: "failed in analysis", spec: jobSpec, upTo: gatewayv1.Stage_STAGE_ANALYSIS, fail: true, wantIn: "failed"},
		{name: "stopped after preprocess", spec: stopAtPreprocess, upTo: gatewayv1.Stage_STAGE_PREPROCESS, wantIn: "stopped before"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newNarrateEnv(t, "", time.Minute)
			id := e.job(t, tc.spec, tc.upTo, tc.fail)
			r := e.call(t, &gatewayv1.NarrateRequest{JobId: id}, secretKey)
			if got := connect.CodeOf(r.err); got != connect.CodeFailedPrecondition {
				t.Fatalf("code = %v, want failed_precondition (%v)", got, r.err)
			}
			if !strings.Contains(r.err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", r.err, tc.wantIn)
			}
			if e.narrator.calls != 0 {
				t.Errorf("the narrator was called %d times", e.narrator.calls)
			}
			e.checkNoKeyLeak(t, r)
		})
	}
}

func TestNarrateErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*narrateEnv)
		// jobID overrides the finished job the call is for. "-" sends none.
		jobID string
		// narratorURL replaces the fake narrator, for an unreachable one.
		narratorURL string
		timeout     time.Duration
		want        connect.Code
		wantEvents  int
		wantIn      string
		// exact wants the message to equal wantIn.
		exact bool
	}{
		{
			name:  "no job id",
			setup: func(*narrateEnv) {}, jobID: "-",
			want: connect.CodeInvalidArgument, wantIn: "job_id",
		},
		{
			name:  "unknown job",
			setup: func(*narrateEnv) {}, jobID: "nope",
			want: connect.CodeNotFound,
		},
		{
			name: "narrator rejects the key, message passed on without the key",
			setup: func(e *narrateEnv) {
				e.narrator.err = connect.NewError(connect.CodeUnauthenticated, "The LLM provider rejected the API key "+secretKey+". Check the key.")
			},
			want: connect.CodeUnauthenticated, wantIn: "rejected the API key [redacted]",
		},
		{
			name: "narrator asks for a key",
			setup: func(e *narrateEnv) {
				e.narrator.err = connect.NewError(connect.CodeUnauthenticated, "Add an LLM key to ask questions.")
			},
			want: connect.CodeUnauthenticated, wantIn: "Add an LLM key to ask questions.",
		},
		{
			name: "error mid-stream after text",
			setup: func(e *narrateEnv) {
				e.narrator.events = []*narratorv1.NarrateEvent{textEvent("Air got ")}
				e.narrator.err = connect.NewError(connect.CodeResourceExhausted, "The LLM provider is rate limiting this key.")
			},
			want: connect.CodeResourceExhausted, wantEvents: 1, wantIn: "rate limiting",
		},
		{
			// The narrator's message, provider text included, reaches the
			// browser unchanged, and the code stays the narrator's.
			name: "provider message passed on unchanged",
			setup: func(e *narrateEnv) {
				e.narrator.err = connect.NewError(connect.CodeResourceExhausted,
					"This LLM key has no credits left. Add credits with the provider or use another key. Provider said: You have no credits remaining.")
			},
			want:   connect.CodeResourceExhausted,
			wantIn: "This LLM key has no credits left. Add credits with the provider or use another key. Provider said: You have no credits remaining.",
			exact:  true,
		},
		{
			name: "slow provider keeps the narrator's code",
			setup: func(e *narrateEnv) {
				e.narrator.err = connect.NewError(connect.CodeDeadlineExceeded,
					"The LLM provider took too long to start answering.")
			},
			want: connect.CodeDeadlineExceeded, wantIn: "took too long to start answering",
		},
		{
			name:        "narrator not running",
			setup:       func(*narrateEnv) {},
			narratorURL: "http://127.0.0.1:1",
			want:        connect.CodeUnavailable, wantIn: "isn't reachable",
		},
		{
			name: "narrator too slow",
			setup: func(e *narrateEnv) {
				e.narrator.events = []*narratorv1.NarrateEvent{textEvent("Air ")}
				e.narrator.block = true
			},
			timeout: 200 * time.Millisecond,
			want:    connect.CodeDeadlineExceeded, wantEvents: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			timeout := tc.timeout
			if timeout == 0 {
				timeout = time.Minute
			}
			e := newNarrateEnv(t, tc.narratorURL, timeout)
			tc.setup(e)
			id := tc.jobID
			switch id {
			case "-":
				id = ""
			case "":
				id = e.job(t, jobSpec, gatewayv1.Stage_STAGE_ANALYSIS, false)
			}
			r := e.call(t, &gatewayv1.NarrateRequest{JobId: id}, secretKey)
			if got := connect.CodeOf(r.err); got != tc.want {
				t.Fatalf("code = %v, want %v (%v)", got, tc.want, r.err)
			}
			if len(r.events) != tc.wantEvents {
				t.Errorf("got %d events before the error, want %d", len(r.events), tc.wantEvents)
			}
			if tc.wantIn != "" && !strings.Contains(r.err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", r.err, tc.wantIn)
			}
			var ce *connect.Error
			if tc.exact && (!errors.As(r.err, &ce) || ce.Message() != tc.wantIn) {
				t.Errorf("message = %q, want exactly %q", ce.Message(), tc.wantIn)
			}
			e.checkNoKeyLeak(t, r)
		})
	}
}

func TestNewClientChecksURL(t *testing.T) {
	for _, u := range []string{"", "narrator:8081", "ftp://narrator", "http://"} {
		if _, err := narrate.NewClient(u); err == nil {
			t.Errorf("NewClient(%q) should fail", u)
		}
	}
	if _, err := narrate.NewClient("http://narrator:8081"); err != nil {
		t.Errorf("NewClient: %v", err)
	}
}
