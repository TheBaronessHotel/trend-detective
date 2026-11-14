// Package obs wires the-watchers the same way for every Go service. Logs go to
// stderr as JSON, and every record carries trace_id and span_id inside a
// span, plus job_id and task_id inside a task. Metrics and pprof are served
// on the admin listener. Spans go to the global OpenTelemetry provider, which
// drops them until the observability profile (PLAT-5) installs an exporter.
package obs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lein3000zzz/the-watchers/pkg/observability"
	"github.com/lein3000zzz/the-watchers/pkg/observability/admin"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging/slogging"
	"github.com/lein3000zzz/the-watchers/pkg/observability/metrics/prommetrics"
	"github.com/lein3000zzz/the-watchers/pkg/observability/profiling"
	"github.com/lein3000zzz/the-watchers/pkg/observability/tracing/oteltracing"
)

// Setup builds the Utility for a service and starts its admin listener on
// ADMIN_ADDR (default :9090). Call Shutdown on the result before exiting.
func Setup(service string) (*observability.Utility, error) {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return nil, err
	}
	log, err := slogging.NewSlogLoggerWithConfig(slogging.Config{
		Level:    level,
		Format:   logging.FormatJSON,
		ToStdout: true,
	}, oteltracing.CorrelateCore, func(next logging.Core) logging.Core {
		return logging.WithContextAttrs(next, taskAttrs)
	})
	if err != nil {
		return nil, fmt.Errorf("logger: %w", err)
	}
	oteltracing.InstallPropagator()

	mgr := prommetrics.DefaultManager()
	util := observability.NewUtility(log.With("service", service), mgr, oteltracing.DefaultOtelManager(),
		func(context.Context) error { return log.Close() },
	)

	profiling.Apply(profiling.Config{Mode: profiling.ModePull})
	addr := envOr("ADMIN_ADDR", ":9090")
	server := admin.New(admin.Config{Addr: addr},
		admin.Route{Path: "/metrics", Handler: mgr.Handler()},
		admin.Route{Path: "/debug/pprof/", Handler: profiling.Handler()},
	)
	go func() {
		if err := server.ListenAndServe(); err != nil {
			util.Logger.Error(context.Background(), "admin listener stopped", logging.Err(err))
		}
	}()
	util.OnShutdown(server.Shutdown)
	return util, nil
}

// ShutdownTimeout bounds how long a service waits for flushes when it stops.
const ShutdownTimeout = 10 * time.Second

type taskKey struct{}

type taskIDs struct{ job, task string }

// WithTask returns a context whose log records carry job_id and task_id.
func WithTask(ctx context.Context, jobID, taskID string) context.Context {
	return context.WithValue(ctx, taskKey{}, taskIDs{job: jobID, task: taskID})
}

// WithJob returns a context whose log records carry job_id.
func WithJob(ctx context.Context, jobID string) context.Context {
	return WithTask(ctx, jobID, "")
}

func taskAttrs(ctx context.Context) []logging.Attr {
	ids, ok := ctx.Value(taskKey{}).(taskIDs)
	if !ok {
		return nil
	}
	attrs := []logging.Attr{{Key: "job_id", Value: ids.job}}
	if ids.task != "" {
		attrs = append(attrs, logging.Attr{Key: "task_id", Value: ids.task})
	}
	return attrs
}

func parseLevel(s string) (logging.Level, error) {
	switch strings.ToLower(s) {
	case "", "info":
		return logging.LevelInfo, nil
	case "trace":
		return logging.LevelTrace, nil
	case "debug":
		return logging.LevelDebug, nil
	case "warn":
		return logging.LevelWarn, nil
	case "error":
		return logging.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL must be trace, debug, info, warn or error, got %q", s)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
