// Package llm holds the provider-agnostic contract for streaming chat models.
// Implementations: OpenAI Chat Completions (also Ollama, OpenRouter, vLLM, LM Studio)
// and Anthropic Messages. The API key lives in memory only and is never logged.
package llm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Role of a chat message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one chat turn (system prompt is passed separately).
type Message struct {
	Role Role
	Text string
}

// Request is a single streaming completion call.
type Request struct {
	System      string
	Messages    []Message
	Model       string
	MaxTokens   int     // 0 means provider default (adapters use 1024 for Anthropic)
	Temperature float64 // 0 is allowed and means deterministic
}

// Config selects the provider endpoint. APIKey must never be logged or stored.
type Config struct {
	BaseURL string // empty means provider default
	APIKey  string // may be empty for local OpenAI-compatible servers (Ollama)
	// HeaderTimeout bounds the wait for the response headers, which a
	// thinking model may hold back while it reasons. Zero means only the
	// context bounds it. When it fires, Stream returns ErrSlowStart.
	HeaderTimeout time.Duration
}

// Delta is one streamed piece of output. The channel is closed after the
// final Delta; a non-nil Err on a Delta terminates the stream.
type Delta struct {
	Text      string
	Model     string // set when the provider reports the resolved model name
	Err       error
	Truncated bool // set on the final Delta when the provider's output token limit was reached
}

// Client streams a completion. Cancelling ctx must stop the HTTP request
// and close the channel.
type Client interface {
	Stream(ctx context.Context, req Request) (<-chan Delta, error)
}

// Sentinel errors so the handler can map them to Connect codes.
var (
	ErrUnauthorized = errors.New("llm: unauthorized")     // 401/403 -> Unauthenticated
	ErrRateLimited  = errors.New("llm: rate limited")     // 429 -> ResourceExhausted
	ErrBadRequest   = errors.New("llm: bad request")      // 400/404 -> InvalidArgument
	ErrUpstream     = errors.New("llm: upstream failure") // 5xx, network -> Unavailable
	// ErrQuotaExhausted means the key has no credits or quota left: OpenAI
	// insufficient_quota, Anthropic billing errors, OpenRouter 402. Unlike a
	// rate limit, waiting doesn't help.
	ErrQuotaExhausted = errors.New("llm: quota exhausted")
	// ErrRedirect means the base URL answered with a redirect, which the
	// client doesn't follow.
	ErrRedirect = errors.New("llm: base URL redirects")
	// ErrSlowStart means the provider sent no response headers within
	// Config.HeaderTimeout. It is not ErrUpstream: the provider was reached
	// and may just be slow.
	ErrSlowStart = errors.New("llm: provider slow to start answering")
)

// ProviderError is a failure the provider explained in its own JSON error
// body or stream event. Kind is one of the sentinels above. Message is the
// provider's message, with the key removed, control characters stripped
// and cut to about 300 runes, so it is safe to show the user. It is only
// set from a parsed provider error, never from a raw body.
type ProviderError struct {
	Kind    error
	Status  int // HTTP status, or 0 for an error inside the stream
	Message string
}

func (e *ProviderError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%v: stream: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("%v: HTTP %d: %s", e.Kind, e.Status, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.Kind }

// ProviderMessage returns the provider's own message from err, or "" when
// err carries none.
func ProviderMessage(err error) string {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Message
	}
	return ""
}
