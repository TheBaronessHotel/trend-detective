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
	"github.com/lein3000zzz/the-watchers/pkg/observability/logging"
)

// EarthdataTokenHeader carries a user's own Earthdata token on CreateJob.
const EarthdataTokenHeader = "X-Earthdata-Token"

// Server implements GatewayService. Methods that belong to later tasks
// return Unimplemented.
type Server struct {
	gatewayv1connect.UnimplementedGatewayServiceHandler
	cat  *catalog.Catalog
	jobs *jobs.Orchestrator
	log  logging.Logger
}

// New returns a Server.
func New(cat *catalog.Catalog, orch *jobs.Orchestrator, log logging.Logger) *Server {
	return &Server{cat: cat, jobs: orch, log: log.With("component", "api")}
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

// toConnect maps an error to a Connect error. Unexpected errors get a generic
// message, and the detail goes to the log only.
func (s *Server) toConnect(ctx context.Context, err error) error {
	var inv *jobs.InvalidError
	switch {
	case errors.As(err, &inv):
		return connect.NewError(connect.CodeInvalidArgument, inv.Error())
	case errors.Is(err, jobs.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, "No job with that ID. Jobs are kept for 7 days.")
	case errors.Is(err, jobs.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err.Error())
	}
	s.log.Error(ctx, "request failed", logging.Err(err))
	return connect.NewError(connect.CodeInternal, "Something went wrong on our side.")
}
