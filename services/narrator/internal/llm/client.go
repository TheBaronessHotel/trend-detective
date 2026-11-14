package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
)

// New selects a streaming API adapter. A nil httpClient uses http.DefaultClient;
// callers should bound each stream with a context deadline and cancel it if they
// stop reading. BaseURL is the API root, including /v1 for OpenAI-compatible APIs.
func New(provider narratorv1.Provider, cfg Config, httpClient *http.Client) (Client, error) {
	var base, path string
	switch provider {
	case narratorv1.Provider_PROVIDER_OPENAI_CHAT:
		base, path = "https://api.openai.com/v1", "/chat/completions"
	case narratorv1.Provider_PROVIDER_ANTHROPIC_MESSAGES:
		base, path = "https://api.anthropic.com", "/v1/messages"
	default:
		return nil, fmt.Errorf("%w: unsupported provider %d", ErrBadRequest, provider)
	}
	if cfg.BaseURL != "" {
		base = cfg.BaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("%w: BaseURL must be an HTTP(S) API root without credentials or fragment", ErrBadRequest)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	c := transport{endpoint: u.String(), key: cfg.APIKey, http: httpClient, headerTimeout: cfg.HeaderTimeout}
	if provider == narratorv1.Provider_PROVIDER_OPENAI_CHAT {
		return &openAIClient{transport: c}, nil
	}
	return &anthropicClient{transport: c}, nil
}

type transport struct {
	endpoint      string
	key           string
	http          *http.Client
	headerTimeout time.Duration
}

// errHeaderTimeout is the cancel cause when the response headers are late.
var errHeaderTimeout = errors.New("llm: header timeout")

// start keeps HTTP status failures synchronous. Once headers are accepted, one
// goroutine owns the body and channel. Every send is cancellable, including errors.
func (c *transport) start(ctx context.Context, body any, headers http.Header, decode func(sseEvent) (Delta, bool, error)) (<-chan Delta, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode completion request", ErrBadRequest)
	}
	parent := ctx
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(nil) }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: cannot build completion request", ErrBadRequest)
	}
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// The header timeout covers everything up to the response headers:
	// connecting, the proxy, and a model thinking before its first byte.
	// The body then streams for as long as the caller's context allows.
	var timer *time.Timer
	if c.headerTimeout > 0 {
		timer = time.AfterFunc(c.headerTimeout, func() { cancelCause(errHeaderTimeout) })
	}
	resp, err := c.http.Do(req)
	if timer != nil && !timer.Stop() && parent.Err() == nil {
		// The timer fired, even if Do still returned a response.
		if resp != nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, fmt.Errorf("%w: no response headers within %s", ErrSlowStart, c.headerTimeout)
	}
	if err != nil {
		ctxErr := parent.Err()
		if ctxErr == nil {
			ctxErr = ctx.Err()
		}
		cancel()
		if ctxErr != nil {
			return nil, fmt.Errorf("llm: request failed: %w", ctxErr)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("llm: request failed: %w", context.DeadlineExceeded)
		}
		// Keep err in the chain, so a caller can tell a refused address
		// (egress.ErrForbidden) from a network failure. Only the redacted
		// text is ever printed.
		return nil, &chainError{
			msg:  fmt.Sprintf("%v: request failed: %s", ErrUpstream, c.safe(err.Error())),
			errs: []error{ErrUpstream, err},
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer cancel()
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, c.statusError(resp.StatusCode, nil)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if ctx.Err() != nil {
			return nil, fmt.Errorf("llm: request failed: %w", ctx.Err())
		}
		return nil, c.statusError(resp.StatusCode, data)
	}
	out := make(chan Delta)
	go func() {
		defer close(out)
		defer cancel()
		defer resp.Body.Close()
		send := func(d Delta) bool {
			select {
			case out <- d:
				return true
			case <-ctx.Done():
				return false
			}
		}
		var truncated bool
		err := readSSE(resp.Body, func(event sseEvent) (bool, error) {
			// Decoders return a *ProviderError for a provider error event.
			d, done, err := decode(event)
			if err != nil {
				return false, err
			}
			// Providers report token limits before the terminal SSE event and may
			// send usage or model metadata afterward. Keep the flag for the end.
			truncated = truncated || d.Truncated
			d.Truncated = done && truncated
			if (d.Text != "" || d.Model != "" || d.Truncated) && !send(d) {
				return true, nil
			}
			return done, nil
		})
		if err != nil && ctx.Err() == nil {
			var pe *ProviderError
			if errors.As(err, &pe) {
				send(Delta{Err: &ProviderError{Kind: pe.Kind, Message: c.clean(pe.Message)}, Truncated: truncated})
				return
			}
			send(Delta{Err: fmt.Errorf("%w: stream: %s", streamErrorKind(err), c.safe(err.Error())), Truncated: truncated})
		}
	}()
	return out, nil
}

// chainError prints msg and unwraps to every error in errs.
type chainError struct {
	msg  string
	errs []error
}

func (e *chainError) Error() string   { return e.msg }
func (e *chainError) Unwrap() []error { return e.errs }

func (c *transport) safe(s string) string {
	if c.key != "" {
		for _, key := range []string{c.key, url.QueryEscape(c.key), url.PathEscape(c.key)} {
			s = strings.ReplaceAll(s, key, "[redacted]")
		}
	}
	return s
}

type apiError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

// quotaCodes are error codes and types that mean the key has no credits or
// quota left.
var quotaCodes = map[string]bool{
	"402": true, "insufficient_quota": true, "billing_error": true, "insufficient_credits": true,
	"payment_required": true, "billing_hard_limit_reached": true, "credit_balance_too_low": true,
}

// quotaPhrases spot the same in a message, for providers that send a
// generic code: Anthropic answers 400 invalid_request_error with "Your
// credit balance is too low". "Exceeded your current quota" is left out on
// purpose: Gemini sends it for per-minute rate limits too.
var quotaPhrases = []string{
	"credit balance is too low", "no credits", "insufficient credits", "insufficient_quota",
	"out of credits", "requires more credits", "billing hard limit",
}

// quota reports whether e says the key has run out of credits or quota.
func (e apiError) quota() bool {
	if quotaCodes[strings.Trim(string(e.Code), `"`)] || quotaCodes[e.Type] {
		return true
	}
	msg := strings.ToLower(e.Message)
	for _, p := range quotaPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func (e apiError) kind() error {
	if e.quota() {
		return ErrQuotaExhausted
	}
	switch strings.Trim(string(e.Code), `"`) {
	case "401", "403", "invalid_api_key", "authentication_error", "permission_error":
		return ErrUnauthorized
	case "429", "rate_limit_exceeded", "rate_limit_error":
		return ErrRateLimited
	case "400", "404", "invalid_request_error", "not_found_error", "model_not_found":
		return ErrBadRequest
	}
	switch e.Type {
	case "authentication_error", "permission_error":
		return ErrUnauthorized
	case "rate_limit_error":
		return ErrRateLimited
	case "invalid_request_error", "not_found_error":
		return ErrBadRequest
	}
	return ErrUpstream
}

// statusError turns a non-2xx answer into an error. Only a body that parses
// as a provider JSON error yields a *ProviderError, whose message may be
// shown to the user. Any other body, which could come from an internal
// service behind a crafted base_url, is only logged. Authentication
// failures never carry the body, since it can quote part of the key.
func (c *transport) statusError(status int, data []byte) error {
	if status >= 300 && status < 400 {
		return fmt.Errorf("%w: HTTP %d", ErrRedirect, status)
	}
	kind := ErrUpstream
	switch status {
	case 401, 403:
		return fmt.Errorf("%w: HTTP %d: provider authentication failed", ErrUnauthorized, status)
	case 402:
		kind = ErrQuotaExhausted
	case 429:
		kind = ErrRateLimited
	case 400, 404:
		kind = ErrBadRequest
	}
	if e, ok := parseProviderError(data); ok {
		if e.quota() {
			kind = ErrQuotaExhausted
		}
		return &ProviderError{Kind: kind, Status: status, Message: c.clean(e.Message)}
	}
	return fmt.Errorf("%w: HTTP %d: %s", kind, status, c.clean(string(data)))
}

// parseProviderError reads the error bodies providers send:
// {"error":{"message":...}} from OpenAI, OpenRouter and Gemini's
// OpenAI-compatible API, the same wrapped in an array as Gemini sometimes
// sends it, and {"type":"error","error":{...}} from Anthropic. The error
// must be an object with a message.
func parseProviderError(data []byte) (apiError, bool) {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		var list []json.RawMessage
		if json.Unmarshal(data, &list) != nil || len(list) == 0 {
			return apiError{}, false
		}
		data = list[0]
	}
	var envelope struct {
		Error *apiError `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Error == nil || strings.TrimSpace(envelope.Error.Message) == "" {
		return apiError{}, false
	}
	return *envelope.Error, true
}

// maxShownRunes caps a provider message shown to the user.
const maxShownRunes = 300

// keyLikeRE finds tokens that look like API keys (sk-..., Google's AIza...),
// so a message never shows one even when it isn't the caller's own key.
var keyLikeRE = regexp.MustCompile(`\b((sk|pk|rk)-[A-Za-z0-9_\-*.]{6,}|AIza[0-9A-Za-z_\-]{20,})`)

// clean makes provider text safe to log and show: the key and key-like
// tokens are removed, control characters become spaces, runs of spaces
// collapse, and the text is cut to maxShownRunes.
func (c *transport) clean(s string) string {
	s = keyLikeRE.ReplaceAllString(c.safe(s), "[redacted]")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxShownRunes {
		s = string([]rune(s)[:maxShownRunes]) + "…"
	}
	return s
}

func streamErrorKind(err error) error {
	for _, kind := range []error{ErrUnauthorized, ErrRateLimited, ErrBadRequest} {
		if errors.Is(err, kind) {
			return kind
		}
	}
	return ErrUpstream
}

// sseEvent contains one complete event, independent of HTTP chunk boundaries.
type sseEvent struct {
	name string
	data string
}

const maxEventBytes = 1 << 20

func readSSE(r io.Reader, consume func(sseEvent) (bool, error)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var event sseEvent
	var data strings.Builder
	dispatch := func() (bool, error) {
		if data.Len() == 0 {
			event = sseEvent{}
			return false, nil
		}
		event.data = strings.TrimSuffix(data.String(), "\n")
		done, err := consume(event)
		event = sseEvent{}
		data.Reset()
		return done, err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if done, err := dispatch(); done || err != nil {
				return err
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event.name = value
		case "data":
			if data.Len()+len(value)+1 > maxEventBytes {
				return fmt.Errorf("SSE event exceeds %d bytes", maxEventBytes)
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if done, err := dispatch(); done || err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
