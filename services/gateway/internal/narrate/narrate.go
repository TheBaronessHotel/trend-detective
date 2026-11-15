// Package narrate proxies the narrator's stream to the browser. It loads the
// job, refuses until the analysis has finished, fills in what the narrator
// needs from the job spec and the catalog, forwards the user's LLM key and
// relays every event unchanged.
//
// The key lives in memory for the call only. It is never logged, and it is
// removed from any narrator error before that error is logged or shown.
package narrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1/narratorv1connect"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
)

// APIKeyHeader carries the user's LLM key, from the browser to the gateway
// and from the gateway to the narrator.
const APIKeyHeader = "X-Llm-Api-Key"

// ErrUnavailable means the narrator can't be reached. The stack runs without
// it, so this is expected when it isn't deployed.
var ErrUnavailable = errors.New("narrator unreachable")

// NotReadyError means the job has no analysis results to explain, yet or
// ever. Its message is shown to the user.
type NotReadyError struct{ msg string }

func (e *NotReadyError) Error() string { return e.msg }

// NarratorError is the narrator's own verdict on a call, like a rejected key
// or a rate limit. The gateway passes its code and message on as they are,
// with the key removed from the message.
type NarratorError struct {
	Code    connect.Code
	Message string
}

func (e *NarratorError) Error() string { return e.Code.String() + ": " + e.Message }

// Jobs reads jobs. *jobs.Orchestrator implements it.
type Jobs interface {
	GetJob(ctx context.Context, id string) (*gatewayv1.Job, error)
}

// Service relays Narrate calls to the narrator.
type Service struct {
	jobs    Jobs
	cat     *catalog.Catalog
	client  narratorv1connect.NarratorServiceClient
	timeout time.Duration
	log     logging.Logger
}

// New returns a Service. timeout bounds one call, stream included, and should
// be longer than the narrator's own NARRATE_TIMEOUT so its error reaches the
// user. Zero means no limit beyond the caller's.
func New(j Jobs, cat *catalog.Catalog, client narratorv1connect.NarratorServiceClient, timeout time.Duration, log logging.Logger) *Service {
	if log == nil {
		log = logging.Noop()
	}
	return &Service{jobs: j, cat: cat, client: client, timeout: timeout, log: log.With("component", "narrate")}
}

// NewClient returns a narrator client for baseURL, like http://narrator:8081.
// It speaks HTTP/2 without TLS (h2c), as the narrator serves it, and has no
// overall timeout, since answers stream for minutes. The caller's context
// bounds each call.
func NewClient(baseURL string) (narratorv1connect.NarratorServiceClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("NARRATOR_URL must be an http(s) URL like http://narrator:8081, got %q", baseURL)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if u.Scheme == "http" {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		tr.Protocols = protocols
	}
	// obs.Transport carries the trace on to the narrator.
	httpClient := &http.Client{Transport: obs.Transport(tr)}
	return narratorv1connect.NewNarratorServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, baseURL))), nil
}

// Narrate streams the narrator's answer for req's job to send. It returns
// jobs.ErrNotFound for an unknown job, *NotReadyError before the analysis
// has finished, *NarratorError for the narrator's own errors, and an error
// wrapping ErrUnavailable when the narrator can't be reached. An error after
// some events were sent ends the stream, as on the narrator.
func (s *Service) Narrate(ctx context.Context, req *gatewayv1.NarrateRequest, apiKey string, send func(*narratorv1.NarrateEvent) error) error {
	job, err := s.jobs.GetJob(ctx, req.GetJobId())
	if err != nil {
		return err
	}
	if err := ready(job); err != nil {
		return err
	}
	nreq := s.request(job, req)

	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	ctx, info := connect.NewClientContext(ctx)
	if apiKey != "" {
		info.RequestHeader().Set(APIKeyHeader, apiKey)
	}

	start := time.Now()
	stream, err := s.client.Narrate(ctx, nreq)
	if err != nil {
		return s.narratorErr(ctx, err, apiKey)
	}
	defer stream.Close()
	events, model := 0, ""
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return s.narratorErr(ctx, err, apiKey)
		}
		if d := msg.GetEvent().GetDone(); d != nil {
			model = d.GetModel()
		}
		if err := send(msg.GetEvent()); err != nil {
			return err
		}
		events++
	}
	s.log.Info(ctx, "narrated",
		"job_id", job.GetId(),
		"overview", strings.TrimSpace(req.GetQuestion()) == "",
		"with_key", apiKey != "",
		"model", model,
		"events", events,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return nil
}

// ready checks that the job has series analysis results.
func ready(job *gatewayv1.Job) error {
	if job.GetResults().GetAnalysis().GetSeries().GetKey() != "" {
		return nil
	}
	switch job.GetState() {
	case gatewayv1.JobState_JOB_STATE_FAILED:
		return &NotReadyError{msg: "This job failed before the trend analysis, so there is nothing to explain."}
	case gatewayv1.JobState_JOB_STATE_SUCCEEDED:
		return &NotReadyError{msg: "This job stopped before the trend analysis, so there is nothing to explain."}
	}
	return &NotReadyError{msg: "The trend analysis isn't finished yet. Ask again once the job is done."}
}

// request builds the narrator's request from the job and the catalog. The
// spec holds the dates the job actually used, after clamping.
func (s *Service) request(job *gatewayv1.Job, req *gatewayv1.NarrateRequest) *narratorv1.NarrateRequest {
	spec := job.GetSpec()
	variable := &narratorv1.VariableContext{Id: spec.GetVariableId()}
	if entry, ok := s.cat.Get(spec.GetVariableId()); ok {
		v := entry.Variable
		variable.Name = v.GetName()
		variable.Definition = v.GetDefinition()
		variable.Units = v.GetUnits()
		variable.LlmHints = entry.LLMHints
	}
	return &narratorv1.NarrateRequest{
		Result:   job.GetResults().GetAnalysis(),
		Variable: variable,
		Bbox:     spec.GetBbox(),
		Dates:    spec.GetDates(),
		Question: req.GetQuestion(),
		History:  req.GetHistory(),
		Llm:      req.GetLlm(),
	}
}

// narratorErr sorts a client error into the narrator's own verdict, the
// caller going away, or the narrator being unreachable.
func (s *Service) narratorErr(ctx context.Context, err error, apiKey string) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.IsRemote() {
		return &NarratorError{Code: ce.Code(), Message: redact(ce.Message(), apiKey)}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	s.log.Warn(ctx, "narrator unreachable", "error", redact(err.Error(), apiKey))
	return ErrUnavailable
}

// redact removes the key from s. The narrator never echoes it, but this
// holds even if a narrator or provider message one day does.
func redact(s, apiKey string) string {
	if apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, apiKey, "[redacted]")
}
