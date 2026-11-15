// Command ingest fetches raw data from NASA sources into the raw bucket. It
// reads tasks from tasks.ingest and reports each one on events.stage.done.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/internal/envx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/appeears"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/bundle"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/csvupload"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/earthdata"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/giovanni"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/harmony"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/power"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/worker"
	"github.com/lein3000zzz/the-watchers/pkg/observability/metrics/redkit"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env, err := envx.Required("NATS_URL", "REDIS_URL", "S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY")
	if err != nil {
		return err
	}
	// Most tasks wait on NASA rather than work, and an AppEEARS task can wait
	// for hours, so this is high. Harmony has its own limit, HARMONY_CONCURRENCY.
	concurrency, err := envx.Int("INGEST_CONCURRENCY", 16)
	if err != nil {
		return err
	}
	ackWait, err := envx.Duration("INGEST_ACK_WAIT", 2*time.Minute)
	if err != nil {
		return err
	}
	harmonyConcurrency, err := envx.Int("HARMONY_CONCURRENCY", 4)
	if err != nil {
		return err
	}
	// The same limit the gateway signs uploads for.
	maxUploadMB, err := envx.Int("MAX_UPLOAD_MB", 20)
	if err != nil {
		return err
	}

	util, err := obs.Setup("ingest")
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), obs.ShutdownTimeout)
		defer cancel()
		_ = util.Shutdown(sctx)
	}()

	store, err := blob.New(env["S3_ENDPOINT"], env["S3_ACCESS_KEY"], env["S3_SECRET_KEY"])
	if err != nil {
		return err
	}
	bundles, err := bundle.New(envx.String("BUNDLE_DIR", "/bundle"))
	if err != nil {
		return err
	}
	opt, err := redis.ParseURL(env["REDIS_URL"])
	if err != nil {
		return fmt.Errorf("REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to Redis: %w", err)
	}
	harmonyMaxWait, err := envx.Duration("HARMONY_MAX_WAIT", 40*time.Minute)
	if err != nil {
		return err
	}
	harmonyCfg := harmony.Config{
		HarmonyURL:  envx.String("HARMONY_URL", "https://harmony.earthdata.nasa.gov"),
		CMRURL:      envx.String("CMR_URL", "https://cmr.earthdata.nasa.gov"),
		TeamToken:   os.Getenv("EARTHDATA_TOKEN"),
		Concurrency: harmonyConcurrency,
		Downloads:   4,
		MaxWait:     harmonyMaxWait,
	}
	harmonyAdapter := harmony.New(harmonyCfg, rdb)
	giovanniArea := harmony.NewAreaAverage(harmonyCfg, rdb)
	giovanniPoint := giovanni.New(envx.String("GIOVANNI_URL", "https://api.giovanni.earthdata.nasa.gov"),
		earthdata.NewTokens(rdb, os.Getenv("EARTHDATA_TOKEN")))
	if os.Getenv("EARTHDATA_TOKEN") == "" {
		util.Logger.Warn(ctx, "EARTHDATA_TOKEN is empty, so Harmony and Giovanni tasks fail unless the user gives a token")
	}
	powerAdapter := power.New(envx.String("POWER_URL", "https://power.larc.nasa.gov"))
	appeearsMaxWait, err := envx.Duration("APPEEARS_MAX_WAIT", 2*time.Hour)
	if err != nil {
		return err
	}
	appeearsAdapter := appeears.New(appeears.Config{
		URL:      envx.String("APPEEARS_URL", "https://appeears.earthdatacloud.nasa.gov/api"),
		Username: os.Getenv("EARTHDATA_USERNAME"),
		Password: os.Getenv("EARTHDATA_PASSWORD"),
		MaxWait:  appeearsMaxWait,
	}, rdb)
	if os.Getenv("EARTHDATA_USERNAME") == "" || os.Getenv("EARTHDATA_PASSWORD") == "" {
		util.Logger.Warn(ctx, "EARTHDATA_USERNAME or EARTHDATA_PASSWORD is empty, so AppEEARS tasks fail")
	}
	csvUpload := csvupload.New(store.Bucket("uploads"), int64(maxUploadMB)<<20)
	svc := core.New(store.Bucket("raw"), bundles, harmonyAdapter, giovanniArea, giovanniPoint, powerAdapter, csvUpload, appeearsAdapter)

	nc, err := nats.Connect(env["NATS_URL"], nats.Name("ingest"), nats.MaxReconnects(-1))
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("JetStream: %w", err)
	}

	kit, err := redkit.Register(redkit.Params{
		Manager:   util.MetricsManager,
		Logger:    util.Logger,
		Subsystem: "ingest",
		Buckets:   redkit.LongBuckets,
		Component: "ingest task",
	})
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}

	w := worker.New(js, svc, worker.NewRedisAttempts(rdb, 24*time.Hour), worker.NewRedisCancels(rdb), util, kit, worker.Config{
		Concurrency:  concurrency,
		MaxAttempts:  3,
		MaxDeliver:   2000,
		AckWait:      ackWait,
		Backoff:      []time.Duration{10 * time.Second, time.Minute},
		DrainTimeout: 8 * time.Second,
	})
	return w.Run(ctx)
}
