// Package api implements the public Connect API. It translates between
// Connect and the jobs package, and turns errors into Connect codes the web
// app can show.
package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1/gatewayv1connect"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/narrate"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/preflight"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/quota"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/results"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EarthdataTokenHeader carries a user's own Earthdata token on CreateJob.
const EarthdataTokenHeader = "X-Earthdata-Token"

// LLMKeyHeader carries a user's own LLM key on Narrate.
const LLMKeyHeader = narrate.APIKeyHeader

// msgNarratorDown is shown when the narrator can't be reached.
const msgNarratorDown = "The explanation service isn't reachable right now. The charts and numbers still work. Try again in a moment."

// Server implements GatewayService.
type Server struct {
	gatewayv1connect.UnimplementedGatewayServiceHandler
	cat     *catalog.Catalog
	jobs    *jobs.Orchestrator
	results *results.Service
	narrate *narrate.Service
	quota   *quota.Service
	log     logging.Logger
}

// New returns a Server.
func New(cat *catalog.Catalog, orch *jobs.Orchestrator, res *results.Service, narr *narrate.Service, q *quota.Service, log logging.Logger) *Server {
	return &Server{cat: cat, jobs: orch, results: res, narrate: narr, quota: q, log: log.With("component", "api")}
}

// CancelJob implements GatewayService.
func (s *Server) CancelJob(ctx context.Context, req *gatewayv1.CancelJobRequest) (*gatewayv1.CancelJobResponse, error) {
	if req.GetJobId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "job_id is required")
	}
	job, err := s.jobs.CancelJob(ctx, req.GetJobId())
	if err != nil {
		return nil, s.toConnect(ctx, err)
	}
	return &gatewayv1.CancelJobResponse{Job: job}, nil
}

// GetQuota implements GatewayService.
func (s *Server) GetQuota(ctx context.Context, _ *gatewayv1.GetQuotaRequest) (*gatewayv1.GetQuotaResponse, error) {
	if s.quota == nil {
		return nil, connect.NewError(connect.CodeUnavailable, "The quota isn't available.")
	}
	return s.quota.Get(ctx), nil
}

// ListVariables implements GatewayService.
func (s *Server) ListVariables(context.Context, *gatewayv1.ListVariablesRequest) (*gatewayv1.ListVariablesResponse, error) {
	return &gatewayv1.ListVariablesResponse{Variables: s.cat.Variables(), Topics: s.cat.TopicsInUse()}, nil
}

// CreateJob implements GatewayService.
func (s *Server) CreateJob(ctx context.Context, req *gatewayv1.CreateJobRequest) (*gatewayv1.CreateJobResponse, error) {
	token := ""
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		token = info.RequestHeader().Get(EarthdataTokenHeader)
	}
	job, err := s.jobs.CreateJob(ctx, req.GetSpec(), token)
	if err != nil {
		return nil, s.toConnect(ctx, err)
	}
	return &gatewayv1.CreateJobResponse{Job: job}, nil
}

// GetJob implements GatewayService.
func (s *Server) GetJob(ctx context.Context, req *gatewayv1.GetJobRequest) (*gatewayv1.GetJobResponse, error) {
	if req.GetJobId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "job_id is required")
	}
	job, err := s.jobs.GetJob(ctx, req.GetJobId())
	if err != nil {
		return nil, s.toConnect(ctx, err)
	}
	return &gatewayv1.GetJobResponse{Job: job}, nil
}

// WatchJob implements GatewayService. It sends the job now and after every
// change, and ends the stream once the job succeeds or fails.
func (s *Server) WatchJob(ctx context.Context, req *gatewayv1.WatchJobRequest, stream gatewayv1connect.GatewayServiceWatchJobServerStream) error {
	if req.GetJobId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, "job_id is required")
	}
	err := s.jobs.Watch(ctx, req.GetJobId(), func(job *gatewayv1.Job) error {
		return stream.Send(&gatewayv1.WatchJobResponse{Job: job})
	})
	if err != nil {
		return s.toConnect(ctx, err)
	}
	return nil
}

// GetResultURL implements GatewayService.
func (s *Server) GetResultURL(ctx context.Context, req *gatewayv1.GetResultURLRequest) (*gatewayv1.GetResultURLResponse, error) {
	if req.GetJobId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "job_id is required")
	}
	link, err := s.results.ResultURL(ctx, req.GetJobId(), req.GetArtifact())
	if err != nil {
		return nil, s.toConnect(ctx, err)
	}
	return &gatewayv1.GetResultURLResponse{
		Url:          link.URL,
		DataUrl:      link.DataURL,
		AnomaliesUrl: link.AnomaliesURL,
		ExpiresAt:    timestamppb.New(link.ExpiresAt),
	}, nil
}

// CreateUploadURL implements GatewayService.
func (s *Server) CreateUploadURL(ctx context.Context, req *gatewayv1.CreateUploadURLRequest) (*gatewayv1.CreateUploadURLResponse, error) {
	up, err := s.results.UploadURL(ctx, req.GetFilename(), req.GetSizeBytes())
	if err != nil {
		return nil, s.toConnect(ctx, err)
	}
	return &gatewayv1.CreateUploadURLResponse{Url: up.URL, Upload: up.Ref, ExpiresAt: timestamppb.New(up.ExpiresAt)}, nil
}

// Narrate implements GatewayService. It relays the narrator's stream for a
// job whose analysis has finished, and fails with FailedPrecondition before
// that. The X-Llm-Api-Key header goes to the narrator and nowhere else.
func (s *Server) Narrate(ctx context.Context, req *gatewayv1.NarrateRequest, stream gatewayv1connect.GatewayServiceNarrateServerStream) error {
	if req.GetJobId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, "job_id is required")
	}
	if s.narrate == nil {
		return connect.NewError(connect.CodeUnavailable, msgNarratorDown)
	}
	key := ""
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		key = strings.TrimSpace(info.RequestHeader().Get(LLMKeyHeader))
	}
	err := s.narrate.Narrate(ctx, req, key, func(ev *narratorv1.NarrateEvent) error {
		return stream.Send(&gatewayv1.NarrateResponse{Event: ev})
	})
	if err != nil {
		return s.toConnect(ctx, err)
	}
	return nil
}

// toConnect maps an error to a Connect error. Unexpected errors get a generic
// message, and the detail goes to the log only.
func (s *Server) toConnect(ctx context.Context, err error) error {
	var inv *jobs.InvalidError
	var badResult *results.InvalidError
	var notReady *results.NotReadyError
	var refusal *preflight.Refusal
	var narrNotReady *narrate.NotReadyError
	var narrErr *narrate.NarratorError
	switch {
	case errors.As(err, &refusal) && refusal.Quota:
		return connect.NewError(connect.CodeResourceExhausted, refusal.Error())
	case errors.As(err, &refusal):
		return connect.NewError(connect.CodeInvalidArgument, refusal.Error())
	case errors.As(err, &inv):
		return connect.NewError(connect.CodeInvalidArgument, inv.Error())
	case errors.As(err, &badResult):
		return connect.NewError(connect.CodeInvalidArgument, badResult.Error())
	case errors.As(err, &notReady):
		return connect.NewError(connect.CodeFailedPrecondition, notReady.Error())
	case errors.Is(err, jobs.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, "No job with that ID. Jobs are kept for 7 days.")
	case errors.Is(err, jobs.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err.Error())
	case errors.As(err, &narrNotReady):
		return connect.NewError(connect.CodeFailedPrecondition, narrNotReady.Error())
	case errors.As(err, &narrErr):
		return connect.NewError(narrErr.Code, narrErr.Message)
	case errors.Is(err, narrate.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, msgNarratorDown)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, "The request was canceled.")
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, "That took too long. Try again.")
	}
	s.log.Error(ctx, "request failed", logging.Err(err))
	return connect.NewError(connect.CodeInternal, "Something went wrong on our side.")
}
