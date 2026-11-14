// Package llm holds the provider-agnostic contract for streaming chat models.
// Implementations: OpenAI Chat Completions (also Ollama, OpenRouter, vLLM, LM Studio)
// and Anthropic Messages. The API key lives in memory only and is never logged.
package llm

import (
	"context"
	"errors"
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
}

// Delta is one streamed piece of output. The channel is closed after the
// final Delta; a non-nil Err on a Delta terminates the stream.
type Delta struct {
	Text  string
	Model string // set when the provider reports the resolved model name
	Err   error
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
)
