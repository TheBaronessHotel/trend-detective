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
	"strings"

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
	c := transport{endpoint: u.String(), key: cfg.APIKey, http: httpClient}
	if provider == narratorv1.Provider_PROVIDER_OPENAI_CHAT {
		return &openAIClient{transport: c}, nil
	}
	return &anthropicClient{transport: c}, nil
}

type transport struct {
	endpoint string
	key      string
	http     *http.Client
}

// start keeps HTTP status failures synchronous. Once headers are accepted, one
// goroutine owns the body and channel. Every send is cancellable, including errors.
func (c *transport) start(ctx context.Context, body any, headers http.Header, decode func(sseEvent) (Delta, bool, error)) (<-chan Delta, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode completion request", ErrBadRequest)
	}
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: cannot build completion request", ErrBadRequest)
	}
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		ctxErr := ctx.Err()
		cancel()
		if ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, ctxErr)
		}
		return nil, fmt.Errorf("%w: request failed: %s", ErrUpstream, c.safe(err.Error()))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer cancel()
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, ctx.Err())
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
		err := readSSE(resp.Body, func(event sseEvent) (bool, error) {
			d, done, err := decode(event)
			if err != nil {
				return false, err
			}
			if (d.Text != "" || d.Model != "") && !send(d) {
				return true, nil
			}
			return done, nil
		})
		if err != nil && ctx.Err() == nil {
			send(Delta{Err: fmt.Errorf("%w: stream: %s", streamErrorKind(err), c.safe(err.Error()))})
		}
	}()
	return out, nil
}

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

func (e apiError) kind() error {
	switch strings.Trim(string(e.Code), `"`) {
	case "401", "403", "invalid_api_key", "authentication_error", "permission_error":
		return ErrUnauthorized
	case "429", "rate_limit_exceeded", "rate_limit_error", "insufficient_quota":
		return ErrRateLimited
	case "400", "404", "invalid_request_error", "not_found_error", "model_not_found":
		return ErrBadRequest
	}
	switch e.Type {
	case "authentication_error", "permission_error":
		return ErrUnauthorized
	case "rate_limit_error", "insufficient_quota":
		return ErrRateLimited
	case "invalid_request_error", "not_found_error":
		return ErrBadRequest
	}
	return ErrUpstream
}

func (c *transport) statusError(status int, data []byte) error {
	kind := ErrUpstream
	switch status {
	case 401, 403:
		kind = ErrUnauthorized
	case 429:
		kind = ErrRateLimited
	case 400, 404:
		kind = ErrBadRequest
	}
	var envelope struct {
		Error apiError `json:"error"`
	}
	message := string(data)
	if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
		message = envelope.Error.Message
	}
	return fmt.Errorf("%w: HTTP %d: %s", kind, status, c.safe(strings.TrimSpace(message)))
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
