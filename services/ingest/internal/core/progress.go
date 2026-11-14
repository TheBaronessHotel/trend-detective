package core

import "context"

// Unknown is the percent to report when an adapter can't tell how far along
// it is.
const Unknown = -1

// ProgressFunc receives how far a task has got: a percent from 0 to 100, or
// Unknown, and what it's doing, written for people.
type ProgressFunc func(percent int, activity string)

type progressKey struct{}

// WithProgress returns a context whose adapters report progress to fn.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// ReportProgress tells whoever runs the task how far it has got. It does
// nothing when nobody is listening, as in most tests.
func ReportProgress(ctx context.Context, percent int, activity string) {
	if fn, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && fn != nil {
		fn(percent, activity)
	}
}
