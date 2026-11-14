// Package api implements the public Connect API. It translates between
// Connect and the jobs package, and turns errors into Connect codes the web
// app can show.
package api

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1/gatewayv1connect"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/catalog"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/results"
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EarthdataTokenHeader carries a user's own Earthdata token on CreateJob.
const EarthdataTokenHeader = "X-Earthdata-Token"

// Server implements GatewayService. Methods that belong to later tasks
// return Unimplemented.
type Server struct {
	gatewayv1connect.UnimplementedGatewayServiceHandler
	cat     *catalog.Catalog
	jobs    *jobs.Orchestrator
	results *results.Service
	log     logging.Logger
}

// New returns a Server.
func New(cat *catalog.Catalog, orch *jobs.Orchestrator, res *results.Service, log logging.Logger) *Server {
	return &Server{cat: cat, jobs: orch, results: res, log: log.With("component", "api")}
}

// ListVariables implements GatewayService.
func (s *Server) ListVariables(context.Context, *gatewayv1.ListVariablesRequest) (*gatewayv1.ListVariablesResponse, error) {
	return &gatewayv1.ListVariablesResponse{Variables: s.cat.Variables()}, nil
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
	return &gatewayv1.GetResultURLResponse{Url: link.URL, DataUrl: link.DataURL, ExpiresAt: timestamppb.New(link.ExpiresAt)}, nil
}

// CreateUploadURL implements GatewayService.
func (s *Server) CreateUploadURL(ctx context.Context, req *gatewayv1.CreateUploadURLRequest) (*gatewayv1.CreateUploadURLResponse, error) {
	up, err := s.results.UploadURL(ctx, req.GetFilename(), req.GetSizeBytes())
	if err != nil {
		return nil, s.toConnect(ctx, err)
	}
	return &gatewayv1.CreateUploadURLResponse{Url: up.URL, Upload: up.Ref, ExpiresAt: timestamppb.New(up.ExpiresAt)}, nil
}

// toConnect maps an error to a Connect error. Unexpected errors get a generic
// message, and the detail goes to the log only.
func (s *Server) toConnect(ctx context.Context, err error) error {
	var inv *jobs.InvalidError
	var badResult *results.InvalidError
	var notReady *results.NotReadyError
	switch {
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
	}
	s.log.Error(ctx, "request failed", logging.Err(err))
	return connect.NewError(connect.CodeInternal, "Something went wrong on our side.")
}
