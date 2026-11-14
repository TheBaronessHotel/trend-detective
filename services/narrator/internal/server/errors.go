package server

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/egress"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
)

// invalidError is a problem with the request that the caller can fix. Its
// message is safe to show.
type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }

func invalid(msg string) error { return &invalidError{msg: msg} }

// User-facing messages for LLM failures. When the provider explained the
// failure in a JSON error, its message follows ours (see withProvider);
// raw response bodies never reach the user.
const (
	msgUnauthorized = "The LLM provider rejected the API key. Check the key and the provider."
	msgRateLimited  = "The LLM provider is rate limiting this key. Try again in a minute."
	msgQuota        = "This LLM key has no credits left. Add credits with the provider or use another key."
	msgBadRequest   = "The LLM provider rejected the request. Check the model name and the base URL."
	msgRedirect     = "The llm.base_url redirects to another address. Use the final URL, usually the https one."
	msgUpstream     = "The LLM provider is unreachable or failing. Try again later."
	msgSlowStart    = "The LLM provider took too long to start answering. Thinking models can take minutes before the first word: try again, or pick a faster model."
	msgInternal     = "Something went wrong on our side."
	msgStorage      = "The analysis results can't be read right now. Try again in a moment."
)

// toConnect maps err to a Connect error. Unexpected errors get a generic
// message, and the detail goes to the log with the key redacted.
func (s *Server) toConnect(ctx context.Context, err error, apiKey string) error {
	var ce *connect.Error
	var inv *invalidError
	var storage *StorageError
	switch {
	case errors.As(err, &inv):
		return connect.NewError(connect.CodeInvalidArgument, inv.Error())
	case errors.Is(err, egress.ErrForbidden):
		// The dialer refused the resolved address. Checked before
		// ErrUpstream, which the same error also wraps.
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodePermissionDenied, msgForbiddenHost)
	case errors.Is(err, llm.ErrUnauthorized):
		// No provider message: an auth error can quote part of the key.
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeUnauthenticated, msgUnauthorized)
	case errors.Is(err, llm.ErrQuotaExhausted):
		// resource_exhausted, like a rate limit, since Narrate already uses
		// failed_precondition for "analysis not finished". The message says
		// that waiting won't help.
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeResourceExhausted, withProvider(msgQuota, err, apiKey))
	case errors.Is(err, llm.ErrRateLimited):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeResourceExhausted, withProvider(msgRateLimited, err, apiKey))
	case errors.Is(err, llm.ErrSlowStart):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeDeadlineExceeded, msgSlowStart)
	case errors.Is(err, llm.ErrRedirect):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeInvalidArgument, msgRedirect)
	case errors.Is(err, llm.ErrBadRequest):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeInvalidArgument, withProvider(msgBadRequest, err, apiKey))
	case errors.Is(err, llm.ErrUpstream):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeUnavailable, withProvider(msgUpstream, err, apiKey))
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, "The request was canceled.")
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, "The answer took too long.")
	case errors.As(err, &ce):
		// Already a Connect error, for example Unimplemented from a stub.
		return connect.NewError(ce.Code(), redact(ce.Message(), apiKey))
	case errors.Is(err, blob.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, "The analysis results are gone. Run the job again.")
	case errors.As(err, &storage):
		s.log.Warn(ctx, "object store failed", "error", redact(err.Error(), apiKey))
		return connect.NewError(connect.CodeUnavailable, msgStorage)
	}
	s.log.Error(ctx, "narrate failed", "error", redact(err.Error(), apiKey))
	return connect.NewError(connect.CodeInternal, msgInternal)
}

// withProvider appends the provider's own message, when it sent one in a
// JSON error, so a case like an empty account explains itself. The llm
// package already removed the key, control characters and length; the key
// is removed again here in case a fake or future adapter forgets.
func withProvider(msg string, err error, apiKey string) string {
	pm := strings.TrimSpace(redact(llm.ProviderMessage(err), apiKey))
	if pm == "" {
		return msg
	}
	return msg + " Provider said: " + pm
}

func (s *Server) logLLM(ctx context.Context, err error, apiKey string) {
	s.log.Warn(ctx, "llm call failed", "error", redact(err.Error(), apiKey))
}

// redact removes the API key from s. Adapters shouldn't put the key in an
// error, but a provider's response body might.
func redact(s, apiKey string) string {
	if apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, apiKey, "[redacted]")
}
