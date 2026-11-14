// Command narrator serves NarratorService over Connect. The gateway calls it
// once analysis has finished, and it streams an LLM explanation of the
// results using the user's own API key.
//
// "narrator healthcheck" exits 0 when a narrator on HTTP_ADDR answers
// /healthz. Compose uses it, since the image has no curl.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1/narratorv1connect"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/internal/envx"
	"github.com/TheBaronessHotel/trend-detective/internal/obs"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/egress"
	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/server"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
)

// maxRequestBytes bounds one NarrateRequest. History is capped in the server
// too, this just stops a huge body early.
const maxRequestBytes = 1 << 20

// shutdownGrace is how long streams in flight may finish after SIGTERM.
// compose.yaml gives the container a little longer (stop_grace_period).
const shutdownGrace = 10 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "narrator:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env, err := envx.Required("S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY")
	if err != nil {
		return err
	}
	timeout, err := envx.Duration("NARRATE_TIMEOUT", 3*time.Minute)
	if err != nil {
		return err
	}
	headerTimeout, err := llmHeaderTimeout(timeout)
	if err != nil {
		return err
	}
	bucket := envx.String("ANALYSIS_BUCKET", "analysis")
	allowlist := server.ParseAllowlist(envx.String("NARRATOR_LLM_BASE_URL_ALLOWLIST", ""))

	util, err := obs.Setup("narrator")
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), obs.ShutdownTimeout)
		defer cancel()
		_ = util.Shutdown(sctx)
	}()
	log := util.Logger

	store, err := blob.New(env["S3_ENDPOINT"], env["S3_ACCESS_KEY"], env["S3_SECRET_KEY"])
	if err != nil {
		return err
	}

	// newBuilder, newVerifier and newLLMClient live in wiring.go.
	svc, err := server.New(server.Deps{
		Loader:           server.BucketLoader{Name: bucket, Bucket: store.Bucket(bucket)},
		Builder:          newBuilder(),
		Verifier:         newVerifier(),
		NewClient:        newLLMClient(newLLMHTTPClient(egress.NewGuard(func(host string) bool { return server.HostListed(host, allowlist) })), headerTimeout),
		Log:              log,
		Timeout:          timeout,
		BaseURLAllowlist: allowlist,
	})
	if err != nil {
		return err
	}

	connectServer := connect.NewServer()
	narratorv1connect.RegisterNarratorServiceHandler(connectServer, svc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(maxRequestBytes))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	// h2c, so the gateway can stream over HTTP/2 without TLS.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Addr:              envx.String("HTTP_ADDR", ":8081"),
		Handler:           mux,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: answers stream for as long as NARRATE_TIMEOUT.
	}

	errc := make(chan error, 1)
	go func() {
		log.Info(ctx, "listening", "addr", srv.Addr, "bucket", bucket, "narrate_timeout", timeout.String(), "llm_header_timeout", headerTimeout.String(), "base_url_allowlist", allowlist)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		log.Error(context.Background(), "stopping", logging.Err(err))
		return err
	}
	// Let streams in flight finish, up to a bound, then cut the rest. A
	// stream can run for NARRATE_TIMEOUT, so running out of grace is normal
	// and not a failed exit.
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		log.Warn(context.Background(), "closing streams still running after the shutdown grace", "grace", shutdownGrace.String(), logging.Err(err))
		_ = srv.Close()
	}
	return nil
}

func healthcheck() int {
	return healthcheckAddr(envx.String("HTTP_ADDR", ":8081"))
}

// healthcheckAddr asks the narrator listening on addr for /healthz. A
// wildcard host means this machine.
func healthcheckAddr(addr string) int {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
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
