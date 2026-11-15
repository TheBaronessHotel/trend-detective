package obs

import (
	"context"
	"fmt"
	"os"

	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// tracingEnabled reports whether an OTLP endpoint is configured. Only then
// does Setup install an exporter.
func tracingEnabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// installTracer makes the global OpenTelemetry provider export spans over
// OTLP/HTTP, for example to Jaeger in the obs compose profile. It does nothing
// and returns nil when no endpoint is set, so spans stay no-ops. The exporter
// reads the standard OTEL_EXPORTER_OTLP_* variables itself and doesn't dial
// until the first batch, so startup never waits for the collector. Export
// errors go to log at warn level; a missing collector costs spans, not
// requests.
func installTracer(service string, log logging.Logger) (func(context.Context) error, error) {
	if !tracingEnabled() {
		return nil, nil
	}
	exp, err := otlptracehttp.New(context.Background())
	if err != nil {
		return nil, fmt.Errorf("OTLP exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(),
		resource.NewSchemaless(attribute.String("service.name", service)))
	if err != nil {
		return nil, fmt.Errorf("OTel resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn(context.Background(), "OpenTelemetry", logging.Err(err))
	}))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
