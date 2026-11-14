package server

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
)

// invalidError is a problem with the request that the caller can fix. Its
// message is safe to show.
type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }

func invalid(msg string) error { return &invalidError{msg: msg} }

// User-facing messages for LLM failures. They never echo the provider's
// response, which can quote part of the key.
const (
	msgUnauthorized = "The LLM provider rejected the API key. Check the key and the provider."
	msgRateLimited  = "The LLM provider is rate limiting this key. Try again in a minute."
	msgBadRequest   = "The LLM provider rejected the request. Check the model name and the base URL."
	msgUpstream     = "The LLM provider is unreachable or failing. Try again later."
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
	case errors.Is(err, llm.ErrUnauthorized):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeUnauthenticated, msgUnauthorized)
	case errors.Is(err, llm.ErrRateLimited):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeResourceExhausted, msgRateLimited)
	case errors.Is(err, llm.ErrBadRequest):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeInvalidArgument, msgBadRequest)
	case errors.Is(err, llm.ErrUpstream):
		s.logLLM(ctx, err, apiKey)
		return connect.NewError(connect.CodeUnavailable, msgUpstream)
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
