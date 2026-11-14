package server

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
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1/narratorv1connect"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/egress"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/overview"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"google.golang.org/protobuf/proto"
)

// secretKey must never show up in logs or errors.
const secretKey = "sk-test-SECRET-0123456789"

const (
	seriesKey = "jobs/j1/series.json"
	gridKey   = "jobs/j1/grid.json"
)

// fakeLoader serves documents from memory, or an error per key.
type fakeLoader struct {
	docs map[string]proto.Message
	errs map[string]error
	// block makes Load wait for ctx, then fail like the object store does,
	// and close stopped when it returns.
	block   bool
	entered chan struct{}
	stopped chan struct{}
}

func (f *fakeLoader) Load(ctx context.Context, ref *commonv1.BlobRef, msg proto.Message) error {
	if f.block {
		if f.entered != nil {
			close(f.entered)
		}
		<-ctx.Done()
		if f.stopped != nil {
			close(f.stopped)
		}
		return &StorageError{Err: ctx.Err()}
	}
	if err, ok := f.errs[ref.GetKey()]; ok {
		return err
	}
	doc, ok := f.docs[ref.GetKey()]
	if !ok {
		return blob.ErrNotFound
	}
	proto.Merge(msg, doc)
	return nil
}

// fakeBuilder records its input and returns a fixed request.
type fakeBuilder struct {
	mu    sync.Mutex
	got   *prompt.Input
	model string
	err   error
}

func (f *fakeBuilder) Build(in prompt.Input) (llm.Request, []prompt.Fact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = &in
	if f.err != nil {
		return llm.Request{}, nil, f.err
	}
	return llm.Request{System: "system", Model: f.model, Messages: []llm.Message{{Role: llm.RoleUser, Text: "overview"}}},
		[]prompt.Fact{{Name: "regression.slope_per_decade", Value: 0.42, Units: "mm/day"}}, nil
}

// fakeVerifier flags every listed number that appears in the answer.
type fakeVerifier struct {
	flag []string
	mu   sync.Mutex
	got  string
}

func (f *fakeVerifier) Unverified(answer string, _ []prompt.Fact) []string {
	f.mu.Lock()
	f.got = answer
	f.mu.Unlock()
	var out []string
	for _, n := range f.flag {
		if strings.Contains(answer, n) {
			out = append(out, n)
		}
	}
	return out
}

// fakeLLM streams deltas, or fails at Stream.
type fakeLLM struct {
	deltas    []llm.Delta
	streamErr error
	// block makes the stream wait for ctx after sending deltas.
	block bool

	mu      sync.Mutex
	gotReq  llm.Request
	stopped chan struct{}
}

func (f *fakeLLM) Stream(ctx context.Context, req llm.Request) (<-chan llm.Delta, error) {
	f.mu.Lock()
	f.gotReq = req
	f.mu.Unlock()
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	ch := make(chan llm.Delta)
	go func() {
		defer close(ch)
		for _, d := range f.deltas {
			select {
			case ch <- d:
			case <-ctx.Done():
				return
			}
		}
		if f.block {
			<-ctx.Done()
			if f.stopped != nil {
				close(f.stopped)
			}
		}
	}()
	return ch, nil
}

// factory records how it was called.
type factory struct {
	client llm.Client
	err    error

	mu       sync.Mutex
	calls    int
	provider narratorv1.Provider
	cfg      llm.Config
}

func (f *factory) New(p narratorv1.Provider, cfg llm.Config) (llm.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.provider, f.cfg = p, cfg
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
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

type env struct {
	loader   *fakeLoader
	builder  *fakeBuilder
	verifier *fakeVerifier
	llm      *fakeLLM
	factory  *factory
	logs     *captureCore
	client   narratorv1connect.NarratorServiceClient
	// allowlist is passed as Deps.BaseURLAllowlist.
	allowlist []string
	// timeout is Deps.Timeout. Zero means 5s.
	timeout time.Duration
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		loader: &fakeLoader{
			docs: map[string]proto.Message{
				seriesKey: &analysisv1.SeriesAnalysis{VariableId: "gpm_precip", Units: "mm/day", NValid: 240},
				gridKey:   &analysisv1.GridAnalysis{VariableId: "gpm_precip", Rows: 4, Cols: 5},
			},
			errs: map[string]error{},
		},
		builder:  &fakeBuilder{},
		verifier: &fakeVerifier{flag: []string{"9.9"}},
		llm: &fakeLLM{deltas: []llm.Delta{
			{Text: "Rainfall rose "},
			{Text: "0.42 mm/day", Model: "gpt-5.5-2026-10-01"},
			{Text: " per decade, maybe 9.9."},
		}},
	}
	e.factory = &factory{client: e.llm}
	return e
}

// start serves the env's server over HTTP and returns a client for it.
func (e *env) start(t *testing.T) {
	t.Helper()
	var log logging.Logger
	e.logs, log = newCapture()
	timeout := e.timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	srv, err := New(Deps{Loader: e.loader, Builder: e.builder, Verifier: e.verifier, NewClient: e.factory.New, Log: log, Timeout: timeout, BaseURLAllowlist: e.allowlist})
	if err != nil {
		t.Fatal(err)
	}
	cs := connect.NewServer()
	narratorv1connect.RegisterNarratorServiceHandler(cs, srv)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, cs)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	e.client = narratorv1connect.NewNarratorServiceClient(connect.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL)))
}

func baseRequest() *narratorv1.NarrateRequest {
	return &narratorv1.NarrateRequest{
		Result: &analysisv1.AnalysisResult{
			Series: &commonv1.BlobRef{Bucket: "analysis", Key: seriesKey},
			Grid:   &commonv1.BlobRef{Bucket: "analysis", Key: gridKey},
		},
		Variable: &narratorv1.VariableContext{Id: "gpm_precip", Name: "Precipitation", Units: "mm/day"},
		Llm:      &narratorv1.LlmConfig{Provider: narratorv1.Provider_PROVIDER_OPENAI_CHAT, Model: "gpt-5.5"},
	}
}

type result struct {
	texts []string
	done  *narratorv1.NarrateDone
	// order is "t" per text delta and "d" for done, as received.
	order string
	err   error
}

// call runs Narrate with key in the header and collects every event.
func (e *env) call(t *testing.T, ctx context.Context, req *narratorv1.NarrateRequest, key string) result {
	t.Helper()
	ctx, info := connect.NewClientContext(ctx)
	if key != "" {
		info.RequestHeader().Set("x-llm-api-key", key)
	}
	var r result
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
		switch ev := msg.GetEvent().GetEvent().(type) {
		case *narratorv1.NarrateEvent_TextDelta:
			r.texts = append(r.texts, ev.TextDelta)
			r.order += "t"
		case *narratorv1.NarrateEvent_Done:
			r.done = ev.Done
			r.order += "d"
		default:
			t.Fatalf("unexpected event %v", msg)
		}
	}
}

func TestNarrateStreamsTextThenDone(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	r := e.call(t, t.Context(), baseRequest(), secretKey)
	if r.err != nil {
		t.Fatalf("Narrate: %v", r.err)
	}
	if r.order != "tttd" {
		t.Fatalf("event order = %q, want tttd", r.order)
	}
	want := []string{"Rainfall rose ", "0.42 mm/day", " per decade, maybe 9.9."}
	if strings.Join(r.texts, "|") != strings.Join(want, "|") {
		t.Fatalf("texts = %q, want %q", r.texts, want)
	}
	if r.done.GetModel() != "gpt-5.5-2026-10-01" {
		t.Errorf("done.model = %q, want the model the provider reported", r.done.GetModel())
	}
	if got := r.done.GetUnverifiedNumbers(); len(got) != 1 || got[0] != "9.9" {
		t.Errorf("unverified = %q, want [9.9]", got)
	}
	if e.verifier.got != strings.Join(want, "") {
		t.Errorf("verifier saw %q, want the full answer", e.verifier.got)
	}

	// The builder got both documents and the original request.
	in := e.builder.got
	if in.Series.GetNValid() != 240 || in.Grid.GetRows() != 4 || in.Req.GetVariable().GetId() != "gpm_precip" {
		t.Errorf("builder input = %+v", in)
	}
	// The model falls back to the request's when the builder leaves it empty.
	if e.llm.gotReq.Model != "gpt-5.5" || e.llm.gotReq.System != "system" {
		t.Errorf("llm request = %+v", e.llm.gotReq)
	}
	if e.factory.provider != narratorv1.Provider_PROVIDER_OPENAI_CHAT || e.factory.cfg.APIKey != secretKey || e.factory.cfg.BaseURL != "" {
		t.Errorf("factory called with %v, base_url %q, key set %v", e.factory.provider, e.factory.cfg.BaseURL, e.factory.cfg.APIKey == secretKey)
	}
}

func TestNarrateDoneWithoutModelOrNumbers(t *testing.T) {
	e := newEnv(t)
	e.llm.deltas = []llm.Delta{{Text: "No clear trend."}, {Text: ""}}
	e.builder.model = "builder-model"
	e.start(t)
	r := e.call(t, t.Context(), baseRequest(), secretKey)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.order != "td" {
		t.Fatalf("event order = %q, want td (empty deltas are skipped)", r.order)
	}
	if r.done.GetModel() != "builder-model" {
		t.Errorf("done.model = %q, want builder-model", r.done.GetModel())
	}
	if len(r.done.GetUnverifiedNumbers()) != 0 {
		t.Errorf("unverified = %q, want none", r.done.GetUnverifiedNumbers())
	}
}

func TestNarrateGridIsOptional(t *testing.T) {
	tests := []struct {
		name    string
		grid    *commonv1.BlobRef
		gridErr error
	}{
		{name: "no grid ref", grid: nil},
		{name: "grid missing in bucket", grid: &commonv1.BlobRef{Bucket: "analysis", Key: "jobs/j1/nope.json"}},
		{name: "grid read fails", grid: &commonv1.BlobRef{Bucket: "analysis", Key: gridKey}, gridErr: errors.New("decode: bad json")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.gridErr != nil {
				e.loader.errs[gridKey] = tc.gridErr
			}
			e.start(t)
			req := baseRequest()
			req.Result.Grid = tc.grid
			r := e.call(t, t.Context(), req, secretKey)
			if r.err != nil {
				t.Fatalf("Narrate: %v", r.err)
			}
			if r.done == nil || e.builder.got.Grid != nil || e.builder.got.Series == nil {
				t.Fatalf("want a done event, series only; done=%v input=%+v", r.done, e.builder.got)
			}
		})
	}
}

func TestNarrateErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*env, *narratorv1.NarrateRequest)
		key   string
		want  connect.Code
		// wantText is how many text deltas arrive before the error.
		wantText    int
		noLLMCall   bool
		wantInError string
	}{
		{
			name:  "no result",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Result = nil },
			key:   secretKey, want: connect.CodeInvalidArgument, noLLMCall: true, wantInError: "result.series",
		},
		{
			name:  "no series ref",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Result.Series = nil },
			key:   secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name:  "no llm config",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Llm = nil },
			key:   secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name:  "unspecified provider",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Llm.Provider = narratorv1.Provider_PROVIDER_UNSPECIFIED },
			key:   secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name:  "no model",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Llm.Model = "  " },
			key:   secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name:  "bad base url",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Llm.BaseUrl = "ftp://example.com" },
			key:   secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name: "bad history role",
			setup: func(_ *env, r *narratorv1.NarrateRequest) {
				r.History = []*narratorv1.ChatMessage{{Role: narratorv1.Role_ROLE_UNSPECIFIED, Text: "hi"}}
			},
			key: secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name:  "question too long",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Question = strings.Repeat("a", MaxQuestionChars+1) },
			key:   secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name: "history too long",
			setup: func(_ *env, r *narratorv1.NarrateRequest) {
				for range MaxHistory + 1 {
					r.History = append(r.History, &narratorv1.ChatMessage{Role: narratorv1.Role_ROLE_USER, Text: "q"})
				}
			},
			key: secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name:  "question without key for public endpoint",
			setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Question = "Why did it rise?" },
			key:   "", want: connect.CodeUnauthenticated, noLLMCall: true, wantInError: "Add an LLM key to ask questions",
		},
		{
			name: "question without key for public base url",
			setup: func(_ *env, r *narratorv1.NarrateRequest) {
				r.Question = "Why did it rise?"
				r.Llm.BaseUrl = "https://openrouter.ai/api/v1"
			},
			key: "", want: connect.CodeUnauthenticated, noLLMCall: true, wantInError: "Add an LLM key to ask questions",
		},
		{
			name: "question without key and without llm config",
			setup: func(_ *env, r *narratorv1.NarrateRequest) {
				r.Question = "Why did it rise?"
				r.Llm = nil
			},
			key: "", want: connect.CodeInvalidArgument, noLLMCall: true, wantInError: "llm is required",
		},
		{
			name:  "overview without key, series missing",
			setup: func(e *env, _ *narratorv1.NarrateRequest) { delete(e.loader.docs, seriesKey) },
			key:   "", want: connect.CodeNotFound, noLLMCall: true,
		},
		{
			name: "overview without key, object store down",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.loader.errs[seriesKey] = &StorageError{Err: errors.New("connection refused")}
			},
			key: "", want: connect.CodeUnavailable, noLLMCall: true,
		},
		{
			name:  "series missing",
			setup: func(e *env, _ *narratorv1.NarrateRequest) { delete(e.loader.docs, seriesKey) },
			key:   secretKey, want: connect.CodeNotFound, noLLMCall: true,
		},
		{
			name: "object store down",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.loader.errs[seriesKey] = &StorageError{Err: fmt.Errorf("get analysis/%s: connection refused", seriesKey)}
			},
			key: secretKey, want: connect.CodeUnavailable, noLLMCall: true,
		},
		{
			name: "series does not decode",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.loader.errs[seriesKey] = fmt.Errorf("decode analysis/%s: unexpected token", seriesKey)
			},
			key: secretKey, want: connect.CodeInternal, noLLMCall: true,
		},
		{
			name: "series in another bucket",
			setup: func(e *env, r *narratorv1.NarrateRequest) {
				e.loader.errs[seriesKey] = invalid(`results must be in the "analysis" bucket, got "raw"`)
			},
			key: secretKey, want: connect.CodeInvalidArgument, noLLMCall: true,
		},
		{
			name:  "builder fails",
			setup: func(e *env, _ *narratorv1.NarrateRequest) { e.builder.err = errors.New("no regression in series") },
			key:   secretKey, want: connect.CodeInternal, noLLMCall: true,
		},
		{
			name: "builder stub unimplemented",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.builder.err = connect.NewError(connect.CodeUnimplemented, "prompt builder is not wired yet")
			},
			key: secretKey, want: connect.CodeUnimplemented, noLLMCall: true,
		},
		{
			name: "factory refuses provider",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.factory.err = fmt.Errorf("%w: unsupported provider", llm.ErrBadRequest)
			},
			key: secretKey, want: connect.CodeInvalidArgument,
		},
		{
			name: "unauthorized",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.llm.streamErr = fmt.Errorf("%w: 401 Incorrect API key provided: %s", llm.ErrUnauthorized, secretKey)
			},
			key: secretKey, want: connect.CodeUnauthenticated,
		},
		{
			name: "rate limited",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.llm.streamErr = fmt.Errorf("%w: 429", llm.ErrRateLimited)
			},
			key: secretKey, want: connect.CodeResourceExhausted,
		},
		{
			name: "bad request",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.llm.streamErr = fmt.Errorf("%w: 404 model not found", llm.ErrBadRequest)
			},
			key: secretKey, want: connect.CodeInvalidArgument,
		},
		{
			name: "upstream down",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.llm.streamErr = fmt.Errorf("%w: dial tcp: connection refused", llm.ErrUpstream)
			},
			key: secretKey, want: connect.CodeUnavailable,
		},
		{
			name: "unknown llm error",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.llm.streamErr = fmt.Errorf("weird failure with key %s", secretKey)
			},
			key: secretKey, want: connect.CodeInternal,
		},
		{
			name: "error mid-stream",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.llm.deltas = []llm.Delta{
					{Text: "Rainfall rose "},
					{Err: fmt.Errorf("%w: stream reset, key=%s", llm.ErrUpstream, secretKey)},
					{Text: "never sent"},
				}
			},
			key: secretKey, want: connect.CodeUnavailable, wantText: 1,
		},
		{
			name: "rate limited mid-stream",
			setup: func(e *env, _ *narratorv1.NarrateRequest) {
				e.llm.deltas = []llm.Delta{{Text: "a"}, {Text: "b"}, {Err: llm.ErrRateLimited}}
			},
			key: secretKey, want: connect.CodeResourceExhausted, wantText: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			req := baseRequest()
			tc.setup(e, req)
			e.start(t)
			r := e.call(t, t.Context(), req, tc.key)
			if r.err == nil {
				t.Fatalf("want error %v, got events %q", tc.want, r.order)
			}
			if got := connect.CodeOf(r.err); got != tc.want {
				t.Fatalf("code = %v, want %v (%v)", got, tc.want, r.err)
			}
			if r.done != nil {
				t.Errorf("got a done event on a failed stream")
			}
			if len(r.texts) != tc.wantText {
				t.Errorf("got %d text deltas before the error, want %d", len(r.texts), tc.wantText)
			}
			if tc.noLLMCall && e.factory.calls != 0 {
				t.Errorf("the LLM factory was called %d times, want none", e.factory.calls)
			}
			if tc.wantInError != "" && !strings.Contains(r.err.Error(), tc.wantInError) {
				t.Errorf("error %q does not mention %q", r.err, tc.wantInError)
			}
			if strings.Contains(r.err.Error(), secretKey) {
				t.Errorf("the API key leaked into the error: %v", r.err)
			}
			if logs := e.logs.text(); strings.Contains(logs, secretKey) {
				t.Errorf("the API key leaked into the logs:\n%s", logs)
			}
		})
	}
}

func TestNarrateWithoutKeyStreamsTemplateOverview(t *testing.T) {
	series := &analysisv1.SeriesAnalysis{
		VariableId: "gpm_precip", Units: "mm/day", Alpha: 0.05, NValid: 240, NTotal: 240,
		Regression: &analysisv1.RegressionResult{
			SlopePerDecade: 0.42, SlopeCiLow: 0.1, SlopeCiHigh: 0.74, PValue: 0.003, Significant: true,
		},
		Warnings: []string{"Only 20 years of data"},
	}
	grid := &analysisv1.GridAnalysis{Layers: []*analysisv1.GridLayer{{
		SlopesPerDecade: []float64{0.1, 0.2}, Significant: []bool{true, false},
	}}}
	tests := []struct {
		name  string
		setup func(*env, *narratorv1.NarrateRequest)
		// noGrid means the grid is left out of the overview.
		noGrid bool
	}{
		{name: "provider default endpoint", setup: func(*env, *narratorv1.NarrateRequest) {}},
		{name: "no llm config at all", setup: func(_ *env, r *narratorv1.NarrateRequest) { r.Llm = nil }},
		{name: "unspecified provider and model", setup: func(_ *env, r *narratorv1.NarrateRequest) {
			r.Llm = &narratorv1.LlmConfig{}
		}},
		{name: "public base url", setup: func(_ *env, r *narratorv1.NarrateRequest) {
			r.Llm.BaseUrl = "https://openrouter.ai/api/v1"
		}},
		{name: "base url outside the allowlist calls nothing", setup: func(e *env, r *narratorv1.NarrateRequest) {
			e.allowlist = []string{"localhost"}
			r.Llm.BaseUrl = "https://evil.example.com/v1"
		}},
		{name: "blank question counts as overview", setup: func(_ *env, r *narratorv1.NarrateRequest) {
			r.Question = "  \n"
		}},
		{name: "grid unreadable", noGrid: true, setup: func(e *env, _ *narratorv1.NarrateRequest) {
			e.loader.errs[gridKey] = errors.New("decode: bad json")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.loader.docs[seriesKey] = series
			e.loader.docs[gridKey] = grid
			req := baseRequest()
			req.Dates = &commonv1.DateRange{Start: &commonv1.Date{Year: 2001, Month: 1}, End: &commonv1.Date{Year: 2020, Month: 12}}
			tc.setup(e, req)
			e.start(t)

			r := e.call(t, t.Context(), req, "")
			if r.err != nil {
				t.Fatalf("Narrate: %v", r.err)
			}
			wantGrid := grid
			if tc.noGrid {
				wantGrid = nil
			}
			want := overview.Build(series, wantGrid, req.GetVariable(), req.GetDates())
			if got := strings.Join(r.texts, ""); got != want {
				t.Fatalf("text = %q\nwant %q", got, want)
			}
			if wantOrder := strings.Repeat("t", len(overview.BuildSentences(series, wantGrid, req.GetVariable(), req.GetDates()))) + "d"; r.order != wantOrder || len(r.texts) < 3 {
				t.Errorf("event order = %q, want %q (one delta per sentence)", r.order, wantOrder)
			}
			if r.done.GetModel() != "template" || len(r.done.GetUnverifiedNumbers()) != 0 {
				t.Errorf("done = %v, want model template and no unverified numbers", r.done)
			}
			if e.factory.calls != 0 || e.builder.got != nil {
				t.Errorf("the LLM path ran: factory calls %d, builder input %v", e.factory.calls, e.builder.got)
			}
			if logs := e.logs.text(); !strings.Contains(logs, "narrated") || !strings.Contains(logs, "template") {
				t.Errorf("want a narrated log record with model template, got:\n%s", logs)
			}
		})
	}
}

func TestNarrateKeyOptionalForLocalServer(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	req := baseRequest()
	req.Llm.BaseUrl = "http://host.docker.internal:11434/v1"
	r := e.call(t, t.Context(), req, "")
	if r.err != nil {
		t.Fatalf("Narrate: %v", r.err)
	}
	if r.done == nil {
		t.Fatal("no done event")
	}
	if e.factory.cfg.APIKey != "" || e.factory.cfg.BaseURL != req.Llm.BaseUrl {
		t.Errorf("factory cfg base_url = %q, key empty = %v", e.factory.cfg.BaseURL, e.factory.cfg.APIKey == "")
	}
}

func TestNarrateNeverLogsKey(t *testing.T) {
	// A successful call, a grid failure that quotes the key, and an LLM error
	// that quotes it. None may put the key in a log record.
	e := newEnv(t)
	e.loader.errs[gridKey] = fmt.Errorf("odd error mentioning %s", secretKey)
	e.start(t)
	if r := e.call(t, t.Context(), baseRequest(), secretKey); r.err != nil {
		t.Fatal(r.err)
	}
	e.llm.streamErr = fmt.Errorf("%w: invalid key %s", llm.ErrUnauthorized, secretKey)
	if r := e.call(t, t.Context(), baseRequest(), secretKey); connect.CodeOf(r.err) != connect.CodeUnauthenticated {
		t.Fatalf("want Unauthenticated, got %v", r.err)
	}
	logs := e.logs.text()
	if !strings.Contains(logs, "narrated") || !strings.Contains(logs, "[redacted]") {
		t.Fatalf("expected a narrated record and a redacted error, got:\n%s", logs)
	}
	if strings.Contains(logs, secretKey) || strings.Contains(logs, "SECRET") {
		t.Fatalf("the API key leaked into the logs:\n%s", logs)
	}
}

func TestNarrateClientCancel(t *testing.T) {
	e := newEnv(t)
	e.llm.deltas = []llm.Delta{{Text: "first"}}
	e.llm.block = true
	e.llm.stopped = make(chan struct{})
	e.start(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("x-llm-api-key", secretKey)
	stream, err := e.client.Narrate(ctx, baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	msg, err := stream.Receive()
	if err != nil || msg.GetEvent().GetTextDelta() != "first" {
		t.Fatalf("first event = %v, %v", msg, err)
	}
	cancel()
	select {
	case <-e.llm.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the LLM stream was not canceled after the client went away")
	}
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New with no deps should fail")
	}
}

func TestIsLocalBaseURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"", false},
		{"https://api.openai.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"http://8.8.8.8:11434", false},
		{"http://localhost:11434/v1", true},
		{"http://LOCALHOST:1234", true},
		{"http://127.0.0.1:1234/v1", true},
		{"http://[::1]:11434", true},
		{"http://192.168.1.20:11434", true},
		{"http://10.0.0.5:8000/v1", true},
		{"http://host.docker.internal:11434/v1", true},
		{"http://ollama:11434/v1", true},
		{"http://lm.localhost:1234", true},
		{"http://169.254.169.254/latest", false},
		{"http://[fe80::1]:11434", false},
		{"http://[::ffff:127.0.0.1]:11434", true},
		{"http://localhost.:11434", false},
		{"://bad", false},
	}
	for _, tc := range tests {
		if got := isLocalBaseURL(tc.url); got != tc.want {
			t.Errorf("isLocalBaseURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// fakeBucket serves one object per key, or fails at Get or while reading.
type fakeBucket struct {
	objects map[string]string
	getErr  error
	readErr error
	gotKey  string
}

func (f *fakeBucket) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.gotKey = key
	if f.getErr != nil {
		return nil, f.getErr
	}
	body, ok := f.objects[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	var r io.Reader = strings.NewReader(body)
	if f.readErr != nil {
		r = io.MultiReader(r, &failingReader{err: f.readErr})
	}
	return io.NopCloser(r), nil
}

type failingReader struct{ err error }

func (f *failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestBucketLoader(t *testing.T) {
	const good = `{"variableId":"v","units":"mm/day","someFutureField":1}`
	tests := []struct {
		name    string
		bucket  string
		key     string
		fb      fakeBucket
		check   func(error) bool
		wantDoc bool
	}{
		{name: "same bucket", bucket: "analysis", key: "k.json", wantDoc: true},
		{name: "empty bucket means analysis", bucket: "", key: "k.json", wantDoc: true},
		{name: "raw bucket refused", bucket: "raw", key: "k.json",
			check: func(err error) bool { var inv *invalidError; return errors.As(err, &inv) }},
		{name: "uploads bucket refused", bucket: "uploads", key: "k.json",
			check: func(err error) bool { var inv *invalidError; return errors.As(err, &inv) }},
		{name: "missing object", bucket: "analysis", key: "nope.json",
			check: func(err error) bool {
				var se *StorageError
				return errors.Is(err, blob.ErrNotFound) && !errors.As(err, &se)
			}},
		{name: "store unreachable", bucket: "analysis", key: "k.json", fb: fakeBucket{getErr: errors.New("dial tcp: connection refused")},
			check: func(err error) bool { var se *StorageError; return errors.As(err, &se) }},
		{name: "read fails midway", bucket: "analysis", key: "k.json", fb: fakeBucket{readErr: errors.New("connection reset")},
			check: func(err error) bool { var se *StorageError; return errors.As(err, &se) }},
		{name: "bad json is not a storage error", bucket: "analysis", key: "bad.json",
			check: func(err error) bool { var se *StorageError; return err != nil && !errors.As(err, &se) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb := tc.fb
			fb.objects = map[string]string{"k.json": good, "bad.json": "{not json"}
			l := BucketLoader{Name: "analysis", Bucket: &fb}
			var got analysisv1.SeriesAnalysis
			err := l.Load(t.Context(), &commonv1.BlobRef{Bucket: tc.bucket, Key: tc.key}, &got)
			if tc.wantDoc {
				if err != nil || fb.gotKey != tc.key || got.GetVariableId() != "v" || got.GetUnits() != "mm/day" {
					t.Fatalf("Load = %v, key %q, doc %v", err, fb.gotKey, &got)
				}
				return
			}
			if !tc.check(err) {
				t.Fatalf("unexpected error %#v", err)
			}
			if strings.HasPrefix(tc.name, "raw") || strings.HasPrefix(tc.name, "uploads") {
				if fb.gotKey != "" {
					t.Errorf("read %q from a refused bucket", fb.gotKey)
				}
			}
		})
	}
}

func TestNarrateBaseURLAllowlist(t *testing.T) {
	allow := ParseAllowlist(" localhost, OpenRouter.ai ,*.example.org,,")
	tests := []struct {
		name    string
		allow   []string
		baseURL string
		key     string
		want    connect.Code // CodeOf(nil) is 0, meaning success
	}{
		{name: "empty allowlist allows any host", allow: nil, baseURL: "http://ollama:11434/v1", key: secretKey},
		{name: "empty allowlist still refuses metadata", allow: nil, baseURL: "http://169.254.169.254/latest", key: secretKey, want: connect.CodePermissionDenied},
		{name: "metadata as a mapped IPv6", allow: nil, baseURL: "http://[::ffff:169.254.169.254]/v1", key: secretKey, want: connect.CodePermissionDenied},
		{name: "IPv6 link-local refused", allow: nil, baseURL: "http://[fe80::1]:11434/v1", key: secretKey, want: connect.CodePermissionDenied},
		{name: "AWS IPv6 metadata refused", allow: []string{"fd00:ec2::254"}, baseURL: "http://[fd00:ec2::254]/v1", key: secretKey, want: connect.CodePermissionDenied},
		{name: "empty allowlist refuses compose services", allow: nil, baseURL: "http://seaweedfs:8888/v1", want: connect.CodePermissionDenied},
		{name: "compose service listed by name", allow: []string{"nats"}, baseURL: "http://nats:8222/v1"},
		{name: "compose service not matched by a wildcard", allow: []string{"*.example.org"}, baseURL: "http://redis:6379/v1", want: connect.CodePermissionDenied},
		{name: "key over plain http to a public host", allow: nil, baseURL: "http://api.openai.com/v1", key: secretKey, want: connect.CodeInvalidArgument},
		{name: "key over plain http to a local host", allow: nil, baseURL: "http://192.168.1.20:11434/v1", key: secretKey},
		{name: "key over https", allow: nil, baseURL: "https://api.openai.com/v1", key: secretKey},
		{name: "provider default endpoint", allow: allow, baseURL: "", key: secretKey},
		{name: "local host listed, no key", allow: allow, baseURL: "http://localhost:11434/v1"},
		{name: "listed host, case-insensitive", allow: allow, baseURL: "https://openrouter.ai/api/v1", key: secretKey},
		{name: "wildcard subdomain", allow: allow, baseURL: "https://llm.example.org/v1", key: secretKey},
		{name: "internal service refused", allow: allow, baseURL: "http://seaweedfs:8333", key: secretKey, want: connect.CodePermissionDenied},
		{name: "unlisted local host refused even without key", allow: allow, baseURL: "http://127.0.0.1:11434/v1", want: connect.CodePermissionDenied},
		{name: "lookalike suffix refused", allow: allow, baseURL: "https://example.org.evil.com/v1", key: secretKey, want: connect.CodePermissionDenied},
		{name: "userinfo trick refused", allow: allow, baseURL: "https://openrouter.ai@evil.com/v1", key: secretKey, want: connect.CodePermissionDenied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.allowlist = tc.allow
			e.start(t)
			req := baseRequest()
			req.Llm.BaseUrl = tc.baseURL
			r := e.call(t, t.Context(), req, tc.key)
			if tc.want == 0 {
				if r.err != nil || r.done == nil {
					t.Fatalf("want success, got %v (events %q)", r.err, r.order)
				}
				if e.factory.cfg.BaseURL != tc.baseURL {
					t.Errorf("factory base_url = %q, want %q", e.factory.cfg.BaseURL, tc.baseURL)
				}
				return
			}
			if got := connect.CodeOf(r.err); got != tc.want {
				t.Fatalf("code = %v, want %v (%v)", got, tc.want, r.err)
			}
			if e.factory.calls != 0 {
				t.Errorf("the LLM factory was called for a refused host")
			}
		})
	}
}

// A dial the egress guard refused maps to permission_denied, not to
// unavailable, though the error also wraps llm.ErrUpstream.
func TestNarrateDialRefused(t *testing.T) {
	e := newEnv(t)
	e.llm.streamErr = fmt.Errorf("%w: request failed: %w", llm.ErrUpstream, fmt.Errorf("dial: %w", egress.ErrForbidden))
	e.start(t)
	r := e.call(t, t.Context(), baseRequest(), secretKey)
	if got := connect.CodeOf(r.err); got != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want permission_denied (%v)", got, r.err)
	}
}

func TestHostListed(t *testing.T) {
	allow := []string{"localhost", "llm.home.lan:8080", "*.example.org"}
	for host, want := range map[string]bool{
		"localhost": true, "LLM.home.lan": true, "a.example.org": true,
		"example.org": false, "other.lan": false,
	} {
		if got := HostListed(host, allow); got != want {
			t.Errorf("HostListed(%q) = %v, want %v", host, got, want)
		}
	}
	if HostListed("localhost", nil) {
		t.Error("an empty allowlist lists nothing")
	}
}

func TestParseAllowlist(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{" , ,", nil},
		{"localhost", []string{"localhost"}},
		{" Ollama:11434 , *.Example.org,", []string{"ollama:11434", "*.example.org"}},
	}
	for _, tc := range tests {
		got := ParseAllowlist(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
			t.Errorf("ParseAllowlist(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	allow := []string{"localhost", "ollama:11434", "*.example.org", "::1"}
	tests := []struct {
		url  string
		want bool
	}{
		{"http://localhost:1234/v1", true},
		{"http://ollama:11434/v1", true},
		{"http://ollama:8080/v1", false},
		{"https://a.example.org", true},
		{"https://a.b.example.org", true},
		{"https://example.org", false},
		{"https://badexample.org", false},
		{"http://[::1]:11434", true},
		{"http://127.0.0.1", false},
		{"://bad", false},
		{"", true},
	}
	for _, tc := range tests {
		if got := hostAllowed(tc.url, allow); got != tc.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
	if !hostAllowed("http://anything:1", nil) {
		t.Error("an empty allowlist must allow every host")
	}
}

func TestNarrateProviderMessages(t *testing.T) {
	tests := []struct {
		name   string
		deltas []llm.Delta
		err    error
		want   connect.Code
		msg    string
	}{
		{
			name: "auth error never shows the provider text",
			err:  &llm.ProviderError{Kind: llm.ErrUnauthorized, Message: "Incorrect API key provided: sk-...abcd"},
			want: connect.CodeUnauthenticated, msg: msgUnauthorized,
		},
		{
			name: "rate limit with the provider's words",
			err:  &llm.ProviderError{Kind: llm.ErrRateLimited, Status: 429, Message: "Rate limit reached for requests"},
			want: connect.CodeResourceExhausted, msg: msgRateLimited + " Provider said: Rate limit reached for requests",
		},
		{
			name:   "quota mid-stream after text",
			deltas: []llm.Delta{{Text: "Air "}, {Err: &llm.ProviderError{Kind: llm.ErrQuotaExhausted, Message: "You have no credits remaining."}}},
			want:   connect.CodeResourceExhausted, msg: msgQuota + " Provider said: You have no credits remaining.",
		},
		{
			name: "the key is redacted again",
			err:  &llm.ProviderError{Kind: llm.ErrBadRequest, Status: 400, Message: "bad model for " + secretKey},
			want: connect.CodeInvalidArgument, msg: msgBadRequest + " Provider said: bad model for [redacted]",
		},
		{
			name: "a redirect asks for the final URL",
			err:  fmt.Errorf("%w: HTTP 301", llm.ErrRedirect),
			want: connect.CodeInvalidArgument, msg: msgRedirect,
		},
		{
			name: "no provider message, our text only",
			err:  fmt.Errorf("%w: HTTP 502: <html>gateway</html>", llm.ErrUpstream),
			want: connect.CodeUnavailable, msg: msgUpstream,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.llm.deltas = tc.deltas
			e.llm.streamErr = tc.err
			e.start(t)
			r := e.call(t, t.Context(), baseRequest(), secretKey)
			var ce *connect.Error
			if !errors.As(r.err, &ce) || ce.Code() != tc.want || ce.Message() != tc.msg {
				t.Fatalf("err = %v, want %v %q", r.err, tc.want, tc.msg)
			}
		})
	}
}

func TestNarrateSlowStart(t *testing.T) {
	e := newEnv(t)
	e.llm.streamErr = fmt.Errorf("%w: no response headers within 2m30s", llm.ErrSlowStart)
	e.start(t)
	r := e.call(t, t.Context(), baseRequest(), secretKey)
	var ce *connect.Error
	if !errors.As(r.err, &ce) || ce.Code() != connect.CodeDeadlineExceeded || ce.Message() != msgSlowStart {
		t.Fatalf("err = %v, want deadline_exceeded %q", r.err, msgSlowStart)
	}
}

// NARRATE_TIMEOUT ends a stream that is still running with deadline_exceeded:
// the text sent so far stays, and no done follows.
func TestNarrateTimeoutMidStream(t *testing.T) {
	e := newEnv(t)
	e.timeout = 150 * time.Millisecond
	e.llm.deltas = []llm.Delta{{Text: "first"}}
	e.llm.block = true
	e.llm.stopped = make(chan struct{})
	e.start(t)
	r := e.call(t, t.Context(), baseRequest(), secretKey)
	var ce *connect.Error
	if !errors.As(r.err, &ce) || ce.Code() != connect.CodeDeadlineExceeded || ce.Message() != "The answer took too long." {
		t.Fatalf("err = %v, want deadline_exceeded", r.err)
	}
	if r.order != "t" || r.done != nil {
		t.Fatalf("events %q, done %v: want the first delta and no done", r.order, r.done)
	}
	select {
	case <-e.llm.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the LLM stream kept running after the timeout")
	}
}

// The timeout also covers loading the results, on both paths.
func TestNarrateTimeoutWhileLoading(t *testing.T) {
	for _, key := range []string{secretKey, ""} {
		e := newEnv(t)
		e.timeout = 150 * time.Millisecond
		e.loader.block = true
		e.start(t)
		r := e.call(t, t.Context(), baseRequest(), key)
		if got := connect.CodeOf(r.err); got != connect.CodeDeadlineExceeded || r.order != "" {
			t.Fatalf("key=%t: code %v, events %q; want deadline_exceeded and no events", key != "", got, r.order)
		}
	}
}

// A client that leaves during the keyless overview stops the load.
func TestNarrateTemplateClientCancel(t *testing.T) {
	e := newEnv(t)
	e.loader.block = true
	e.loader.entered = make(chan struct{})
	e.loader.stopped = make(chan struct{})
	e.start(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan result, 1)
	go func() { done <- e.call(t, ctx, baseRequest(), "") }()
	select {
	case <-e.loader.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the template load never started")
	}
	cancel()
	select {
	case <-e.loader.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the template load kept running after the client left")
	}
	if r := <-done; r.err == nil || r.done != nil {
		t.Fatalf("want an error and no done, got %v, %v", r.err, r.done)
	}
}
