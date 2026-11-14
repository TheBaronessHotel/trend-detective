package core

import (
	"errors"
	"fmt"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
)

// Error is a failure with a reason the user can read. The worker turns it
// into a TaskError.
type Error struct {
	Code gatewayv1.ErrorCode
	// Transient means the same task might succeed in a moment, like after a
	// Harmony 5xx. The worker then lets JetStream redeliver it.
	Transient bool
	// Shown to the user as written.
	UserMessage string
	Err         error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code.String() + ": " + e.UserMessage
	}
	return e.Code.String() + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an Error. The format and args describe the technical detail.
func Errorf(code gatewayv1.ErrorCode, transient bool, userMessage, format string, args ...any) *Error {
	return &Error{Code: code, Transient: transient, UserMessage: userMessage, Err: fmt.Errorf(format, args...)}
}

// Transient wraps an unexpected error, like a network failure, as one worth
// retrying.
func Transient(err error) *Error {
	return &Error{
		Code:        gatewayv1.ErrorCode_ERROR_CODE_INTERNAL,
		Transient:   true,
		UserMessage: "Something went wrong while fetching the data.",
		Err:         err,
	}
}

// AsError returns err as an *Error. An error that isn't one is treated as
// transient, because ingest can't tell why it happened.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Transient(err)
}

// TaskError converts e for StageDone. retryable tells the user whether running
// the job again later might work.
func (e *Error) TaskError() *gatewayv1.TaskError {
	retryable := e.Transient ||
		e.Code == gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED ||
		e.Code == gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM
	detail := ""
	if e.Err != nil {
		detail = e.Err.Error()
	}
	return &gatewayv1.TaskError{
		Code:        e.Code,
		Retryable:   retryable,
		UserMessage: e.UserMessage,
		Detail:      detail,
	}
}
