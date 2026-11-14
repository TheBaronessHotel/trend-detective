// Package results hands the browser signed URLs. Downloads go straight from
// SeaweedFS to the browser, and so do CSV uploads, so neither passes through
// the gateway.
package results

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	preprocessv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/preprocess/v1"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// Buckets the gateway signs downloads for. Stage outputs only: raw files and
// other users' uploads are never handed out.
var downloadable = map[string]bool{"prep": true, "analysis": true}

const uploadBucket = "uploads"

// Signer signs URLs for the browser.
type Signer interface {
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)
	PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)
}

// ProtoReader reads protojson documents from the prep bucket.
type ProtoReader interface {
	GetProto(ctx context.Context, key string, msg proto.Message) error
}

// InvalidError is a request the gateway refuses. Its message is shown to the
// user.
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

// NotReadyError means the job hasn't produced the artifact, or never will.
// Its message is shown to the user.
type NotReadyError struct{ msg string }

func (e *NotReadyError) Error() string { return e.msg }

// Service signs result downloads and CSV uploads.
type Service struct {
	jobs     *jobs.Orchestrator
	prep     ProtoReader
	signer   Signer
	ttl      time.Duration
	maxBytes int64
	now      func() time.Time
	newID    func() string
}

// New returns a Service. URLs last ttl. Uploads are capped at maxBytes.
func New(orch *jobs.Orchestrator, prep ProtoReader, signer Signer, ttl time.Duration, maxBytes int64) *Service {
	return &Service{
		jobs:     orch,
		prep:     prep,
		signer:   signer,
		ttl:      ttl,
		maxBytes: maxBytes,
		now:      time.Now,
		newID:    func() string { return uuid.Must(uuid.NewV7()).String() },
	}
}

// Link is a signed download.
type Link struct {
	URL string
	// For cubes, the binary data the header points to. Empty otherwise.
	DataURL   string
	ExpiresAt time.Time
}

// Singular nouns, so "The %s isn't ready yet" reads right.
var artifactNames = map[gatewayv1.Artifact]string{
	gatewayv1.Artifact_ARTIFACT_SERIES:          "time series",
	gatewayv1.Artifact_ARTIFACT_SERIES_CSV:      "CSV file",
	gatewayv1.Artifact_ARTIFACT_VALUE_CUBE:      "map layer",
	gatewayv1.Artifact_ARTIFACT_ANOMALY_CUBE:    "anomaly map layer",
	gatewayv1.Artifact_ARTIFACT_SERIES_ANALYSIS: "trend analysis",
	gatewayv1.Artifact_ARTIFACT_GRID_ANALYSIS:   "heatmap",
}

// ResultURL signs a download for one of a job's outputs. It returns
// jobs.ErrNotFound for an unknown job, *InvalidError for an unknown artifact,
// and *NotReadyError when the job doesn't have the artifact yet.
func (s *Service) ResultURL(ctx context.Context, jobID string, a gatewayv1.Artifact) (*Link, error) {
	name, ok := artifactNames[a]
	if !ok {
		return nil, &InvalidError{msg: "Pick which result to download."}
	}
	job, err := s.jobs.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	ref := find(job.GetResults(), a)
	if ref.GetKey() == "" {
		if job.GetState() == gatewayv1.JobState_JOB_STATE_SUCCEEDED || job.GetState() == gatewayv1.JobState_JOB_STATE_FAILED {
			return nil, &NotReadyError{msg: fmt.Sprintf("This job has no %s.", name)}
		}
		return nil, &NotReadyError{msg: fmt.Sprintf("The %s isn't ready yet.", name)}
	}

	link := &Link{ExpiresAt: s.now().Add(s.ttl)}
	if link.URL, err = s.sign(ctx, ref); err != nil {
		return nil, err
	}
	if a == gatewayv1.Artifact_ARTIFACT_VALUE_CUBE || a == gatewayv1.Artifact_ARTIFACT_ANOMALY_CUBE {
		header := &preprocessv1.CubeHeader{}
		if err := s.prep.GetProto(ctx, ref.GetKey(), header); err != nil {
			return nil, fmt.Errorf("read cube header %s: %w", ref.GetKey(), err)
		}
		if link.DataURL, err = s.sign(ctx, header.GetData()); err != nil {
			return nil, err
		}
	}
	return link, nil
}

func (s *Service) sign(ctx context.Context, ref *commonv1.BlobRef) (string, error) {
	if !downloadable[ref.GetBucket()] || ref.GetKey() == "" {
		return "", fmt.Errorf("won't sign a download from %q/%q", ref.GetBucket(), ref.GetKey())
	}
	return s.signer.PresignGet(ctx, ref.GetBucket(), ref.GetKey(), s.ttl)
}

func find(r *gatewayv1.JobResults, a gatewayv1.Artifact) *commonv1.BlobRef {
	switch a {
	case gatewayv1.Artifact_ARTIFACT_SERIES:
		return r.GetPreprocess().GetSeries()
	case gatewayv1.Artifact_ARTIFACT_SERIES_CSV:
		return r.GetPreprocess().GetSeriesCsv()
	case gatewayv1.Artifact_ARTIFACT_VALUE_CUBE:
		return r.GetPreprocess().GetValueCube()
	case gatewayv1.Artifact_ARTIFACT_ANOMALY_CUBE:
		return r.GetPreprocess().GetAnomalyCube()
	case gatewayv1.Artifact_ARTIFACT_SERIES_ANALYSIS:
		return r.GetAnalysis().GetSeries()
	case gatewayv1.Artifact_ARTIFACT_GRID_ANALYSIS:
		return r.GetAnalysis().GetGrid()
	}
	return nil
}

// Upload is a signed CSV upload.
type Upload struct {
	URL string
	// Pass this as JobSpec.upload.
	Ref       *commonv1.BlobRef
	ExpiresAt time.Time
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// UploadURL signs a PUT for a user's CSV. It returns *InvalidError for a
// file that isn't a CSV or is too big.
func (s *Service) UploadURL(ctx context.Context, filename string, size int64) (*Upload, error) {
	name := unsafeChars.ReplaceAllString(path.Base(strings.ReplaceAll(filename, `\`, "/")), "_")
	if !strings.HasSuffix(strings.ToLower(name), ".csv") || len(name) <= len(".csv") {
		return nil, &InvalidError{msg: "Only .csv files can be uploaded."}
	}
	if len(name) > 120 {
		name = name[len(name)-120:]
	}
	if size <= 0 {
		return nil, &InvalidError{msg: "The file is empty."}
	}
	if size > s.maxBytes {
		return nil, &InvalidError{msg: fmt.Sprintf("The file is too big. The limit is %d MB.", s.maxBytes>>20)}
	}
	key := s.newID() + "/" + name
	u, err := s.signer.PresignPut(ctx, uploadBucket, key, s.ttl)
	if err != nil {
		return nil, err
	}
	return &Upload{
		URL:       u,
		Ref:       &commonv1.BlobRef{Bucket: uploadBucket, Key: key, SizeBytes: size, ContentType: "text/csv"},
		ExpiresAt: s.now().Add(s.ttl),
	}, nil
}
