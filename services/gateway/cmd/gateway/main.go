// Command gateway serves the public Connect API and orchestrates jobs. It
// publishes stage tasks on NATS, reads StageDone events, and is the only
// writer of job state in Redis.
//
// "gateway healthcheck" exits 0 when a gateway on HTTP_ADDR answers /healthz.
// Compose uses it, since the image has no curl.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1/gatewayv1connect"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/internal/envx"
	"github.com/TheBaronessHotel/trend-detective/internal/natsx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/api"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/events"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/narrate"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/preflight"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/quota"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/results"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
	"github.com/rs/cors"
)

// Job state lives as long as the stream messages, 7 days.
const jobTTL = 7 * 24 * time.Hour

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
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
	limits, err := loadLimits()
	if err != nil {
		return err
	}
	cat, err := catalog.Parse(gateway.CatalogYAML)
	if err != nil {
		return err
	}

	util, err := obs.Setup("gateway")
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), obs.ShutdownTimeout)
		defer cancel()
		_ = util.Shutdown(sctx)
	}()
	log := util.Logger

	opt, err := redis.ParseURL(env["REDIS_URL"])
	if err != nil {
		return fmt.Errorf("REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to Redis: %w", err)
	}

	nc, err := nats.Connect(env["NATS_URL"], nats.Name("gateway"), nats.MaxReconnects(-1))
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("JetStream: %w", err)
	}

	orch := jobs.New(cat, jobs.NewStore(rdb, jobTTL), natsx.Publisher{JS: js}, limits, log)
	quotaCutoff, err := envx.Float01("QUOTA_CUTOFF", 0.9)
	if err != nil {
		return err
	}
	checker := preflight.New(preflight.Config{
		CMRURL:      envx.String("CMR_URL", "https://cmr.earthdata.nasa.gov"),
		HarmonyURL:  envx.String("HARMONY_URL", "https://harmony.earthdata.nasa.gov"),
		GiovanniURL: envx.String("GIOVANNI_URL", "https://api.giovanni.earthdata.nasa.gov"),
		TeamToken:   os.Getenv("EARTHDATA_TOKEN"),
		QuotaCutoff: quotaCutoff,
		Timeout:     5 * time.Second,
	}, log)
	orch.UsePreflight(checker)
	// Must match ingest's HARMONY_CONCURRENCY; compose sets both from one value.
	harmonyLimit, err := envx.Int("HARMONY_CONCURRENCY", 4)
	if err != nil {
		return err
	}
	quotas := quota.New(checker, rdb, os.Getenv("EARTHDATA_TOKEN"), int32(harmonyLimit), log)

	store, err := blob.New(env["S3_ENDPOINT"], env["S3_ACCESS_KEY"], env["S3_SECRET_KEY"])
	if err != nil {
		return err
	}
	signer, err := blob.NewPresigner(envx.String("S3_PUBLIC_ENDPOINT", "http://localhost:8333"), env["S3_ACCESS_KEY"], env["S3_SECRET_KEY"])
	if err != nil {
		return err
	}
	presignTTL, err := envx.Duration("PRESIGN_TTL", 15*time.Minute)
	if err != nil {
		return err
	}
	maxUploadMB, err := envx.Int("MAX_UPLOAD_MB", 20)
	if err != nil {
		return err
	}
	res := results.New(orch, store.Bucket("prep"), signer, presignTTL, int64(maxUploadMB)<<20)

	// The stack starts without the narrator. Narrate then fails with
	// Unavailable and everything else works.
	narratorClient, err := narrate.NewClient(envx.String("NARRATOR_URL", "http://narrator:8081"))
	if err != nil {
		return err
	}
	narratorTimeout, err := envx.Duration("NARRATOR_TIMEOUT", 4*time.Minute)
	if err != nil {
		return err
	}
	narr := narrate.New(orch, cat, narratorClient, narratorTimeout, log)

	connectServer := connect.NewServer()
	gatewayv1connect.RegisterGatewayServiceHandler(connectServer, api.New(cat, orch, res, narr, quotas, log))
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.Handle("GET /debug/jobs/{id}", api.DebugJobs(orch))

	handler, err := obs.HTTP(util, mux, "/healthz")
	if err != nil {
		return err
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Addr:              envx.String("HTTP_ADDR", ":8080"),
		Handler:           withCORS(handler, envx.String("CORS_ORIGINS", "*")),
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 4)
	go func() { errc <- events.Run(ctx, js, orch, log, 16) }()
	go func() { errc <- events.RunAdvisories(ctx, js, orch, log) }()
	go func() { errc <- events.RunProgress(ctx, js, orch, log) }()
	go func() {
		log.Info(ctx, "listening", "addr", srv.Addr, "variables", len(cat.Variables()))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			log.Error(context.Background(), "stopping", logging.Err(err))
			stop()
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

func loadLimits() (jobs.Limits, error) {
	subregionN, err := envx.Int("SUBREGION_N", 8)
	if err != nil {
		return jobs.Limits{}, err
	}
	maxGrid, err := envx.Int("MAX_GRID", 128)
	if err != nil {
		return jobs.Limits{}, err
	}
	maxGranules, err := envx.Int("MAX_GRANULES", 1000)
	if err != nil {
		return jobs.Limits{}, err
	}
	taskCap, err := envx.Int("TASK_CAP", 256)
	if err != nil {
		return jobs.Limits{}, err
	}
	return jobs.Limits{SubregionN: int32(subregionN), MaxGrid: int32(maxGrid), MaxGranules: int32(maxGranules), TaskCap: int32(taskCap)}, nil
}

// withCORS lets the web app call the API from its own dev server.
// CORS_ORIGINS is a comma-separated list, or * for any origin.
func withCORS(h http.Handler, origins string) http.Handler {
	return cors.New(cors.Options{
		AllowedOrigins: strings.Split(origins, ","),
		AllowedMethods: []string{http.MethodGet, http.MethodPost},
		AllowedHeaders: []string{
			"Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms",
			"Connect-Accept-Encoding", "Connect-Content-Encoding",
			"Grpc-Timeout", "X-Grpc-Web", "X-User-Agent", "Traceparent",
			api.EarthdataTokenHeader, api.LLMKeyHeader,
		},
		ExposedHeaders: []string{"Grpc-Status", "Grpc-Message", "Grpc-Status-Details-Bin", "Connect-Content-Encoding"},
		MaxAge:         7200,
	}).Handler(h)
}

func healthcheck() int {
	host, port, err := net.SplitHostPort(envx.String("HTTP_ADDR", ":8080"))
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
