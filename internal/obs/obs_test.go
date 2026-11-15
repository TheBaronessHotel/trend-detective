package obs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lein3000zzz/the-watchers/pkg/observability"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/lein3000zzz/the-watchers/pkg/observability/metrics/prommetrics"
	"github.com/lein3000zzz/the-watchers/pkg/observability/tracing/oteltracing"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// keepGlobals restores the global OTel provider and error handler after a
// test that changes them.
func keepGlobals(t *testing.T) {
	t.Helper()
	provider := otel.GetTracerProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(provider)
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	})
}

func TestInstallTracerIsOffWithoutEndpoint(t *testing.T) {
	keepGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	before := otel.GetTracerProvider()

	shutdown, err := installTracer("test", logging.Noop())
	if err != nil {
		t.Fatal(err)
	}
	if shutdown != nil {
		t.Error("installTracer returned a shutdown func without an endpoint")
	}
	if otel.GetTracerProvider() != before {
		t.Error("installTracer replaced the global provider without an endpoint")
	}
}

func TestInstallTracerExportsOverOTLP(t *testing.T) {
	keepGlobals(t)
	var mu sync.Mutex
	var paths []string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	shutdown, err := installTracer("test", logging.Noop())
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("installTracer returned no shutdown func with an endpoint set")
	}
	_, span := otel.Tracer("test").Start(context.Background(), "work")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 || paths[0] != "/v1/traces" {
		t.Fatalf("collector got %v, want a POST to /v1/traces", paths)
	}
}

// recordSpans installs an SDK provider that keeps finished spans in memory.
func recordSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	keepGlobals(t)
	exp := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	oteltracing.InstallPropagator()
	return exp
}

func TestHTTPRecordsSpansAndMetrics(t *testing.T) {
	exp := recordSpans(t)
	reg := prometheus.NewRegistry()
	util := observability.NewUtility(logging.Noop(), prommetrics.NewManager(reg, reg), oteltracing.DefaultOtelManager())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok/{id}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/pkg.Service/Method", func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("the handler's ResponseWriter lost http.Flusher")
		}
		w.(http.Flusher).Flush()
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	h, err := HTTP(util, mux, "/healthz")
	if err != nil {
		t.Fatal(err)
	}

	const parent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	serve := func(method, path string, header http.Header) int {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range header {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := serve("GET", "/ok/42", http.Header{"Traceparent": {parent}}); code != http.StatusOK {
		t.Fatalf("GET /ok/42 = %d", code)
	}
	serve("GET", "/boom", nil)
	serve("POST", "/pkg.Service/Method", nil)
	serve("GET", "/healthz", nil)
	serve("GET", "/nope", nil)

	spans := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range exp.GetSpans().Snapshots() {
		spans[s.Name()] = s
	}
	if _, ok := spans["GET /healthz"]; ok {
		t.Error("/healthz got a span, want it skipped")
	}
	for _, name := range []string{"GET /ok/{id}", "GET /boom", "POST /pkg.Service/Method", "GET unmatched"} {
		if _, ok := spans[name]; !ok {
			t.Errorf("no span %q; got %v", name, keys(spans))
		}
	}
	ok := spans["GET /ok/{id}"]
	if ok != nil {
		if got := ok.Parent().TraceID().String(); got != "0af7651916cd43dd8448eb211c80319c" {
			t.Errorf("GET /ok span trace %s, want the incoming traceparent's", got)
		}
		if ok.SpanKind() != trace.SpanKindServer {
			t.Errorf("span kind %v, want server", ok.SpanKind())
		}
	}
	if boom := spans["GET /boom"]; boom != nil && boom.Status().Code != codes.Error {
		t.Errorf("GET /boom span status %v, want error", boom.Status().Code)
	}

	body := scrape(t, util)
	for _, want := range []string{
		`watchers_http_operations_total{operation="GET /ok/{id}",status="success"} 1`,
		`watchers_http_operations_total{operation="GET /boom",status="error"} 1`,
		`watchers_http_operations_total{operation="/pkg.Service/Method",status="success"} 1`,
		`watchers_http_operations_total{operation="unmatched",status="success"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	if strings.Contains(body, "healthz") {
		t.Error("metrics count /healthz, want it skipped")
	}
}

func TestTransportInjectsTraceparent(t *testing.T) {
	recordSpans(t)
	var got string
	base := roundTripper(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get("traceparent")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
	})
	ctx, span := otel.Tracer("test").Start(context.Background(), "caller")
	defer span.End()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://narrator/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Transport(base).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q doesn't carry trace %s", got, span.SpanContext().TraceID())
	}
	if req.Header.Get("traceparent") != "" {
		t.Error("Transport changed the caller's request headers")
	}
}

func scrape(t *testing.T, util *observability.Utility) string {
	t.Helper()
	rec := httptest.NewRecorder()
	util.MetricsManager.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	b, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func keys(m map[string]sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
