package obs

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/lein3000zzz/the-watchers/pkg/observability"
	"github.com/lein3000zzz/the-watchers/pkg/observability/metrics/redkit"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const httpTracer = "trend-detective/http"

// HTTP wraps a ServeMux so each request gets a server span and is counted in
// watchers_http_operations_total and watchers_http_operation_duration_seconds.
// The operation label is the mux pattern the request matched: one per Connect
// procedure, never a raw path, so the label set stays bounded. A traceparent
// header on the request becomes the span's parent. Requests to the paths in
// skip, such as health checks, pass through untouched. Responses with status
// 500 and up count as failures.
func HTTP(util *observability.Utility, mux *http.ServeMux, skip ...string) (http.Handler, error) {
	kit, err := redkit.Register(redkit.Params{
		Manager:   util.MetricsManager,
		Logger:    util.Logger,
		Subsystem: "http",
		Buckets:   redkit.MediumLongBuckets,
		Component: "http request",
	})
	if err != nil {
		return nil, fmt.Errorf("HTTP metrics: %w", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slices.Contains(skip, r.URL.Path) {
			mux.ServeHTTP(w, r)
			return
		}
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		_, pattern := mux.Handler(r)
		op := operation(pattern)
		done := kit.Start(ctx, op)
		sw := &statusWriter{ResponseWriter: w}
		name := op
		if !strings.Contains(op, " ") {
			name = r.Method + " " + op
		}
		_ = util.TracerManager.WithSpan(ctx, httpTracer, name, func(ctx context.Context, span trace.Span) error {
			mux.ServeHTTP(sw, r.WithContext(ctx))
			code := sw.code()
			span.SetAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", op),
				attribute.Int("http.response.status_code", code),
			)
			if code >= 500 {
				return fmt.Errorf("HTTP %d", code)
			}
			return nil
		}, trace.WithSpanKind(trace.SpanKindServer))
		done(sw.code() < 500)
	}), nil
}

func operation(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}

// statusWriter remembers the response status. It keeps http.Flusher, which
// Connect needs for server streams.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// Transport adds the context's traceparent to outgoing requests, so a call to
// another service continues the caller's trace.
func Transport(base http.RoundTripper) http.RoundTripper {
	return roundTripper(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		otel.GetTextMapPropagator().Inject(r.Context(), propagation.HeaderCarrier(r.Header))
		return base.RoundTrip(r)
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
