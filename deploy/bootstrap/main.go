// Command bootstrap creates the JetStream streams and SeaweedFS buckets the
// services expect. Compose runs it on every `make up`, before any service
// starts. Running it again is safe: it creates what's missing and applies
// stream settings that changed.
//
// It doesn't create consumers. Each service creates its own at startup, so
// retry settings live with the code that consumes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Jobs expire from Redis after 7 days, so no message needs to outlive that.
const retention = 7 * 24 * time.Hour

// buckets holds one bucket per stage output, plus user uploads.
var buckets = []string{"raw", "prep", "analysis", "uploads"}

var streams = []jetstream.StreamConfig{
	{
		// Work-queue retention deletes a task once its consumer acks it. Each
		// stage's consumer filters on its own subject. The subjects are listed
		// one by one, so a publish to a misspelled subject fails instead of
		// sitting in the stream forever.
		Name:        "TASKS",
		Description: "Stage tasks. One durable consumer per stage, filtered to its subject.",
		Subjects:    []string{"tasks.ingest", "tasks.preprocess", "tasks.analysis"},
		Retention:   jetstream.WorkQueuePolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      retention,
		Duplicates:  2 * time.Minute,
	},
	{
		// Limits retention keeps events after the gateway acks them, so a
		// debugging consumer can replay a job.
		Name:        "EVENTS",
		Description: "StageDone events for the gateway.",
		Subjects:    []string{"events.>"},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      retention,
		Duplicates:  2 * time.Minute,
	},
	{
		// NATS publishes this advisory when a TASKS message runs out of
		// deliveries. Storing it means the gateway still sees it if it was
		// down at that moment.
		Name:        "ADVISORIES",
		Description: "Max-deliveries advisories for TASKS consumers.",
		Subjects:    []string{"$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.TASKS.>"},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      retention,
	},
}

type config struct {
	natsURL     string
	s3Endpoint  string
	s3AccessKey string
	s3SecretKey string
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}
	log.Info("bootstrap done")
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := retry(ctx, log, "streams", func(ctx context.Context) error {
		return ensureStreams(ctx, log, cfg.natsURL)
	}); err != nil {
		return err
	}
	return retry(ctx, log, "buckets", func(ctx context.Context) error {
		return ensureBuckets(ctx, log, cfg)
	})
}

func loadConfig() (config, error) {
	cfg := config{
		natsURL:     os.Getenv("NATS_URL"),
		s3Endpoint:  os.Getenv("S3_ENDPOINT"),
		s3AccessKey: os.Getenv("S3_ACCESS_KEY"),
		s3SecretKey: os.Getenv("S3_SECRET_KEY"),
	}
	var missing []string
	for _, v := range []struct{ name, value string }{
		{"NATS_URL", cfg.natsURL},
		{"S3_ENDPOINT", cfg.s3Endpoint},
		{"S3_ACCESS_KEY", cfg.s3AccessKey},
		{"S3_SECRET_KEY", cfg.s3SecretKey},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func ensureStreams(ctx context.Context, log *slog.Logger, natsURL string) error {
	nc, err := nats.Connect(natsURL, nats.Name("bootstrap"))
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("JetStream: %w", err)
	}
	for _, sc := range streams {
		s, err := js.CreateOrUpdateStream(ctx, sc)
		if err != nil {
			return fmt.Errorf("stream %s: %w", sc.Name, err)
		}
		log.Info("stream ready", "stream", sc.Name, "subjects", sc.Subjects, "messages", s.CachedInfo().State.Msgs)
	}
	return nil
}

func ensureBuckets(ctx context.Context, log *slog.Logger, cfg config) error {
	u, err := url.Parse(cfg.s3Endpoint)
	if err != nil || u.Host == "" {
		return permanent(fmt.Errorf("S3_ENDPOINT must be a URL like http://seaweedfs:8333, got %q", cfg.s3Endpoint))
	}
	mc, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.s3AccessKey, cfg.s3SecretKey, ""),
		Secure: u.Scheme == "https",
	})
	if err != nil {
		return permanent(fmt.Errorf("S3 client: %w", err))
	}
	for _, b := range buckets {
		exists, err := mc.BucketExists(ctx, b)
		if err != nil {
			return fmt.Errorf("check bucket %s: %w", b, err)
		}
		if exists {
			log.Info("bucket ready", "bucket", b)
			continue
		}
		if err := mc.MakeBucket(ctx, b, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("create bucket %s: %w", b, err)
		}
		log.Info("bucket created", "bucket", b)
	}
	return nil
}

// permanentError marks an error that retrying can't fix, like a malformed URL.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return permanentError{err} }

// retry calls fn every two seconds until it succeeds, returns a permanent
// error, or ctx ends. Compose starts bootstrap only once NATS and SeaweedFS
// report healthy, so this mostly covers the few seconds before SeaweedFS's
// S3 gateway accepts requests.
func retry(ctx context.Context, log *slog.Logger, step string, fn func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		var perm permanentError
		if err == nil || errors.As(err, &perm) {
			return err
		}
		log.Warn("not ready, retrying", "step", step, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: gave up: %w", step, err)
		case <-time.After(2 * time.Second):
		}
	}
}
