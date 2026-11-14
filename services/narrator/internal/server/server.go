// Package server implements NarratorService. It loads finished analysis
// results, asks prompt.Builder for an LLM request, streams the model's answer
// and ends with a NarrateDone that lists numbers verify.Verifier couldn't
// trace back to the results.
//
// Without a key, an overview request (empty question) for a non-local
// base_url gets a fixed-text overview from package overview instead, so the
// app explains results for everyone. Questions still need a key.
//
// The user's API key arrives in the x-llm-api-key header. It lives in memory
// for the call only and is never logged or echoed in an error.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1/narratorv1connect"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/overview"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/verify"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
)

// APIKeyHeader carries the user's LLM API key.
const APIKeyHeader = "X-Llm-Api-Key"

// Request limits. The browser resends the whole chat each time, so these
// bound what one call can push into the prompt.
const (
	MaxQuestionChars = 4000
	MaxHistory       = 40
	MaxHistoryChars  = 64000
)

// ClientFactory returns an llm.Client for a provider. It's called once per
// Narrate with the user's key, so the client must not outlive the call.
type ClientFactory func(provider narratorv1.Provider, cfg llm.Config) (llm.Client, error)

// Deps are the parts the server is built from. All are required except Log.
type Deps struct {
	Loader    Loader
	Builder   prompt.Builder
	Verifier  verify.Verifier
	NewClient ClientFactory
	Log       logging.Logger
	// Timeout bounds one Narrate call, including the whole stream. Zero
	// means no limit beyond the caller's.
	Timeout time.Duration
	// BaseURLAllowlist lists the hosts a request's llm.base_url may point
	// at, as built by ParseAllowlist. Empty allows any host. It doesn't
	// apply when base_url is empty, since that's the provider's own endpoint.
	BaseURLAllowlist []string
}

// Server implements NarratorService.
type Server struct {
	loader    Loader
	builder   prompt.Builder
	verifier  verify.Verifier
	newClient ClientFactory
	log       logging.Logger
	timeout   time.Duration
	allowlist []string
}

var _ narratorv1connect.NarratorServiceHandler = (*Server)(nil)

// New returns a Server.
func New(d Deps) (*Server, error) {
	if d.Loader == nil || d.Builder == nil || d.Verifier == nil || d.NewClient == nil {
		return nil, errors.New("server: Loader, Builder, Verifier and NewClient are required")
	}
	log := d.Log
	if log == nil {
		log = logging.Noop()
	}
	return &Server{
		loader:    d.Loader,
		builder:   d.Builder,
		verifier:  d.Verifier,
		newClient: d.NewClient,
		log:       log.With("component", "narrate"),
		timeout:   d.Timeout,
		allowlist: d.BaseURLAllowlist,
	}, nil
}

// Narrate implements NarratorService. Text arrives as text_delta events in
// the order the model produced it, and a NarrateDone is always the last
// event of a successful stream. On an error after some text was sent, the
// stream ends with the error and no NarrateDone.
func (s *Server) Narrate(ctx context.Context, req *narratorv1.NarrateRequest, stream narratorv1connect.NarratorServiceNarrateServerStream) error {
	apiKey := ""
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		apiKey = strings.TrimSpace(info.RequestHeader().Get(APIKeyHeader))
	}
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	start := time.Now()
	res, err := s.narrate(ctx, req, apiKey, stream)
	if err != nil {
		return s.toConnect(ctx, err, apiKey)
	}
	s.log.Info(ctx, "narrated",
		"provider", req.GetLlm().GetProvider().String(),
		"model", res.model,
		"overview", req.GetQuestion() == "",
		"chars", res.chars,
		"unverified", res.unverified,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return nil
}

type outcome struct {
	model      string
	chars      int
	unverified int
}

// msgNeedKey answers a question sent without a key to a non-local endpoint.
const msgNeedKey = "Add an LLM key to ask questions. The overview works without one, and so does a local base_url like an Ollama server."

func (s *Server) narrate(ctx context.Context, req *narratorv1.NarrateRequest, apiKey string, stream narratorv1connect.NarratorServiceNarrateServerStream) (outcome, error) {
	if err := validate(req); err != nil {
		return outcome{}, err
	}
	cfg := req.GetLlm()
	if apiKey == "" && strings.TrimSpace(req.GetQuestion()) == "" && !isLocalBaseURL(cfg.GetBaseUrl()) {
		// No LLM is called, so the llm config doesn't matter here.
		return s.narrateTemplate(ctx, req, stream)
	}
	if err := validateLLM(cfg); err != nil {
		return outcome{}, err
	}
	if !hostAllowed(cfg.GetBaseUrl(), s.allowlist) {
		return outcome{}, connect.NewError(connect.CodePermissionDenied,
			"This server doesn't allow that llm.base_url host. Ask the operator to add it to NARRATOR_LLM_BASE_URL_ALLOWLIST.")
	}
	if apiKey == "" && !isLocalBaseURL(cfg.GetBaseUrl()) {
		return outcome{}, connect.NewError(connect.CodeUnauthenticated, msgNeedKey)
	}

	in, err := s.load(ctx, req, apiKey)
	if err != nil {
		return outcome{}, err
	}
	llmReq, facts, err := s.builder.Build(in)
	if err != nil {
		return outcome{}, fmt.Errorf("build prompt: %w", err)
	}
	if llmReq.Model == "" {
		llmReq.Model = cfg.GetModel()
	}

	client, err := s.newClient(cfg.GetProvider(), llm.Config{BaseURL: cfg.GetBaseUrl(), APIKey: apiKey})
	if err != nil {
		return outcome{}, err
	}
	deltas, err := client.Stream(ctx, llmReq)
	if err != nil {
		return outcome{}, err
	}

	model := llmReq.Model
	var answer strings.Builder
	for d := range deltas {
		if d.Err != nil {
			return outcome{}, d.Err
		}
		if d.Model != "" {
			model = d.Model
		}
		if d.Text == "" {
			continue
		}
		answer.WriteString(d.Text)
		if err := stream.Send(textDelta(d.Text)); err != nil {
			return outcome{}, err
		}
	}
	// A client that closes the channel because ctx ended reports no error
	// itself, so check here rather than send a Done for a cut-off answer.
	if err := ctx.Err(); err != nil {
		return outcome{}, err
	}

	unverified := s.verifier.Unverified(answer.String(), facts)
	if unverified == nil {
		unverified = []string{}
	}
	done := &narratorv1.NarrateResponse{Event: &narratorv1.NarrateEvent{
		Event: &narratorv1.NarrateEvent_Done{Done: &narratorv1.NarrateDone{UnverifiedNumbers: unverified, Model: model}},
	}}
	if err := stream.Send(done); err != nil {
		return outcome{}, err
	}
	return outcome{model: model, chars: answer.Len(), unverified: len(unverified)}, nil
}

// load reads the series results, and the grid results when there are any.
// A grid that can't be read is logged and left out.
func (s *Server) load(ctx context.Context, req *narratorv1.NarrateRequest, apiKey string) (prompt.Input, error) {
	in := prompt.Input{Req: req}
	in.Series = &analysisv1.SeriesAnalysis{}
	if err := s.loader.Load(ctx, req.GetResult().GetSeries(), in.Series); err != nil {
		return prompt.Input{}, fmt.Errorf("load series analysis: %w", err)
	}
	if ref := req.GetResult().GetGrid(); ref.GetKey() != "" {
		grid := &analysisv1.GridAnalysis{}
		if err := s.loader.Load(ctx, ref, grid); err != nil {
			// The grid only adds spatial context. Explain the series without it.
			s.log.Warn(ctx, "grid analysis unavailable, narrating the series only", "key", ref.GetKey(), "error", redact(err.Error(), apiKey))
		} else {
			in.Grid = grid
		}
	}
	return in, nil
}

// narrateTemplate streams the fixed-text overview one sentence per
// text_delta, then a NarrateDone with model "template". Every number in it
// comes from the results, so nothing is unverified.
func (s *Server) narrateTemplate(ctx context.Context, req *narratorv1.NarrateRequest, stream narratorv1connect.NarratorServiceNarrateServerStream) (outcome, error) {
	in, err := s.load(ctx, req, "")
	if err != nil {
		return outcome{}, err
	}
	text := overview.Build(in.Series, in.Grid, req.GetVariable(), req.GetDates())
	for _, sentence := range overview.Sentences(text) {
		if err := ctx.Err(); err != nil {
			return outcome{}, err
		}
		if err := stream.Send(textDelta(sentence)); err != nil {
			return outcome{}, err
		}
	}
	done := &narratorv1.NarrateResponse{Event: &narratorv1.NarrateEvent{
		Event: &narratorv1.NarrateEvent_Done{Done: &narratorv1.NarrateDone{UnverifiedNumbers: []string{}, Model: overview.Model}},
	}}
	if err := stream.Send(done); err != nil {
		return outcome{}, err
	}
	return outcome{model: overview.Model, chars: len(text)}, nil
}

func textDelta(text string) *narratorv1.NarrateResponse {
	return &narratorv1.NarrateResponse{Event: &narratorv1.NarrateEvent{
		Event: &narratorv1.NarrateEvent_TextDelta{TextDelta: text},
	}}
}

// validate checks everything that doesn't need I/O, except the llm config,
// which only matters when an LLM is called. See validateLLM.
func validate(req *narratorv1.NarrateRequest) error {
	if req.GetResult().GetSeries().GetKey() == "" {
		return invalid("result.series is required. Narrate only after the analysis has finished.")
	}
	if n := utf8.RuneCountInString(req.GetQuestion()); n > MaxQuestionChars {
		return invalid(fmt.Sprintf("question is %d characters, the limit is %d", n, MaxQuestionChars))
	}
	if len(req.GetHistory()) > MaxHistory {
		return invalid(fmt.Sprintf("history has %d messages, the limit is %d. Start a new chat.", len(req.GetHistory()), MaxHistory))
	}
	total := 0
	for i, m := range req.GetHistory() {
		if m.GetRole() != narratorv1.Role_ROLE_USER && m.GetRole() != narratorv1.Role_ROLE_ASSISTANT {
			return invalid(fmt.Sprintf("history[%d].role must be ROLE_USER or ROLE_ASSISTANT", i))
		}
		total += utf8.RuneCountInString(m.GetText())
	}
	if total > MaxHistoryChars {
		return invalid(fmt.Sprintf("history is %d characters, the limit is %d. Start a new chat.", total, MaxHistoryChars))
	}
	return nil
}

// validateLLM checks the llm config before an LLM call.
func validateLLM(cfg *narratorv1.LlmConfig) error {
	if cfg == nil {
		return invalid("llm is required")
	}
	switch cfg.GetProvider() {
	case narratorv1.Provider_PROVIDER_OPENAI_CHAT, narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES:
	default:
		return invalid("llm.provider must be PROVIDER_OPENAI_CHAT or PROVIDER_ANTHROPIC_MESSAGES")
	}
	if strings.TrimSpace(cfg.GetModel()) == "" {
		return invalid("llm.model is required, for example gpt-5.5 or claude-sonnet-5-5")
	}
	if b := cfg.GetBaseUrl(); b != "" {
		u, err := url.Parse(b)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return invalid("llm.base_url must be an http or https URL, like http://localhost:11434/v1")
		}
	}
	return nil
}

// isLocalBaseURL reports whether base points at a server on this machine or
// network, like Ollama or LM Studio, which usually needs no key: a loopback
// or private address, localhost, host.docker.internal, or a single-label
// host such as a compose service name.
func isLocalBaseURL(base string) bool {
	if base == "" {
		return false
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "host.docker.internal" {
		return true
	}
	// A bare name only resolves inside a local network, never on the internet.
	return !strings.Contains(host, ".")
}

// ParseAllowlist splits a comma-separated host list, like
// "localhost,ollama,*.openrouter.ai". Entries are lowercased, and blanks are
// dropped, so an empty string allows every host.
func ParseAllowlist(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// hostAllowed reports whether base may be called under allowlist. An entry
// matches the URL's hostname exactly, or host:port when it has a port, and
// "*.example.com" matches any subdomain of example.com but not example.com.
func hostAllowed(base string, allowlist []string) bool {
	if base == "" || len(allowlist) == 0 {
		return true
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	hostPort := strings.ToLower(u.Host)
	for _, entry := range allowlist {
		switch {
		case strings.HasPrefix(entry, "*."):
			if strings.HasSuffix(host, entry[1:]) {
				return true
			}
		case entry == host || entry == hostPort:
			return true
		}
	}
	return false
}
