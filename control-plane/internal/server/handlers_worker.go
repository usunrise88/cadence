package server

// The worker protocol (tag worker, docs/review/2026-09-30-phase-2-plan.md "Worker protocol"). Wave 0 fixes the
// contract only; stream W replaces these 501 answers with the implementation.

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/api"
)

func workerNotYet(opID string) api.Problem {
	detail := opID + " arrives with the worker protocol (phase 2, stream W)"
	return api.Problem{Type: "https://cadence.local/help/errors/not-implemented", Title: "Not implemented", Status: 501, Detail: &detail}
}

// WorkerArtifactsSet answers 501 until stream W lands.
func (s *Server) WorkerArtifactsSet(_ context.Context, _ api.WorkerArtifactsSetRequestObject) (api.WorkerArtifactsSetResponseObject, error) {
	return api.WorkerArtifactsSetdefaultApplicationProblemPlusJSONResponse{Body: workerNotYet("workerArtifacts.set"), StatusCode: 501}, nil
}

// WorkerLogsNew answers 501 until stream W lands.
func (s *Server) WorkerLogsNew(_ context.Context, _ api.WorkerLogsNewRequestObject) (api.WorkerLogsNewResponseObject, error) {
	return api.WorkerLogsNewdefaultApplicationProblemPlusJSONResponse{Body: workerNotYet("workerLogs.new"), StatusCode: 501}, nil
}

// WorkerMetricsNew answers 501 until stream W lands.
func (s *Server) WorkerMetricsNew(_ context.Context, _ api.WorkerMetricsNewRequestObject) (api.WorkerMetricsNewResponseObject, error) {
	return api.WorkerMetricsNewdefaultApplicationProblemPlusJSONResponse{Body: workerNotYet("workerMetrics.new"), StatusCode: 501}, nil
}

// WorkerLeasesRelease answers 501 until stream W lands.
func (s *Server) WorkerLeasesRelease(_ context.Context, _ api.WorkerLeasesReleaseRequestObject) (api.WorkerLeasesReleaseResponseObject, error) {
	return api.WorkerLeasesReleasedefaultApplicationProblemPlusJSONResponse{Body: workerNotYet("workerLeases.release"), StatusCode: 501}, nil
}

// WorkerLeasesReport answers 501 until stream W lands.
func (s *Server) WorkerLeasesReport(_ context.Context, _ api.WorkerLeasesReportRequestObject) (api.WorkerLeasesReportResponseObject, error) {
	return api.WorkerLeasesReportdefaultApplicationProblemPlusJSONResponse{Body: workerNotYet("workerLeases.report"), StatusCode: 501}, nil
}

// WorkerLeasesClaim answers 501 until stream W lands.
func (s *Server) WorkerLeasesClaim(_ context.Context, _ api.WorkerLeasesClaimRequestObject) (api.WorkerLeasesClaimResponseObject, error) {
	return api.WorkerLeasesClaimdefaultApplicationProblemPlusJSONResponse{Body: workerNotYet("workerLeases.claim"), StatusCode: 501}, nil
}

// WorkerRegistrationsNew answers 501 until stream W lands.
func (s *Server) WorkerRegistrationsNew(_ context.Context, _ api.WorkerRegistrationsNewRequestObject) (api.WorkerRegistrationsNewResponseObject, error) {
	return api.WorkerRegistrationsNewdefaultApplicationProblemPlusJSONResponse{Body: workerNotYet("workerRegistrations.new"), StatusCode: 501}, nil
}
