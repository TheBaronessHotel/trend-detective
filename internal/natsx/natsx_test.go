package natsx

import (
	"context"
	"testing"

	"github.com/lein3000zzz/the-watchers/pkg/observability/tracing/oteltracing"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceHeaderStaysLowercaseAndRoundTrips(t *testing.T) {
	oteltracing.InstallPropagator()
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	h := nats.Header{}
	InjectTrace(ctx, h)

	if _, ok := h["traceparent"]; !ok {
		t.Fatalf("want a lowercase traceparent key, got %v", h)
	}
	if _, ok := h["Traceparent"]; ok {
		t.Fatalf("traceparent was canonicalized: %v", h)
	}

	got := trace.SpanContextFromContext(ExtractTrace(context.Background(), h))
	if got.TraceID() != sc.TraceID() || got.SpanID() != sc.SpanID() {
		t.Fatalf("round trip lost the span: got %v/%v, want %v/%v", got.TraceID(), got.SpanID(), sc.TraceID(), sc.SpanID())
	}
}
