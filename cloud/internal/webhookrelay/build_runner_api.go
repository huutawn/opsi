package webhookrelay

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
)

func (s *Server) handleBuildJobDispatchAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	applicationID := r.PathValue("application_id")
	jobID := r.PathValue("build_job_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "build_job", jobID, "owner", "admin", "developer") || !decodeBuildJobIntent(w, r) {
		return
	}
	attempt, err := s.BuildJobs.Dispatch(r.Context(), projectID, applicationID, jobID)
	if err != nil {
		writeBuildJobFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, attempt)
}

func (s *Server) handleBuildRunnerClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		BuildJobID string `json:"build_job_id"`
		AttemptID  string `json:"attempt_id"`
		OIDCToken  string `json:"oidc_token"`
	}
	if !decodeStrictRunnerJSON(w, r, &request) {
		return
	}
	if request.OIDCToken == "" {
		writeBuildJobFailure(w, r, buildjob.Error{Code: "OIDC_MISSING", Status: 401, Message: "GitHub OIDC token is required.", Cause: "oidc"})
		return
	}
	if s.RunnerOIDC == nil || s.runnerOIDCInitError != nil {
		writeBuildJobFailure(w, r, buildjob.Error{Code: "OIDC_UNAVAILABLE", Status: 503, Message: "GitHub OIDC verification is unavailable.", Cause: "oidc"})
		return
	}
	identity, err := s.RunnerOIDC.Verify(r.Context(), request.OIDCToken)
	if err != nil {
		writeBuildJobFailure(w, r, runnerOIDCError(err))
		return
	}
	lease, err := s.BuildJobs.Claim(r.Context(), request.BuildJobID, request.AttemptID, runnerIdentity(identity))
	if err != nil {
		writeBuildJobFailure(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) handleBuildRunnerBuildSpec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(r.URL.Query()) != 1 || len(r.URL.Query()["build_job_id"]) != 1 {
		writeBuildJobFailure(w, r, buildjob.Error{Code: "BUILD_SPEC_REQUEST_INVALID", Status: 400, Message: "Build Spec request is invalid.", Cause: "request"})
		return
	}
	spec, err := s.BuildJobs.BuildSpec(r.Context(), r.URL.Query().Get("build_job_id"), bearerToken(r))
	if err != nil {
		writeBuildJobFailure(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, spec)
}

func decodeStrictRunnerJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeBuildJobFailure(w, r, buildjob.Error{Code: "RUNNER_CLAIM_INVALID", Status: 400, Message: "Runner claim is invalid.", Cause: "request"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeBuildJobFailure(w, r, buildjob.Error{Code: "RUNNER_CLAIM_INVALID", Status: 400, Message: "Runner claim must contain one JSON object.", Cause: "request"})
		return false
	}
	return true
}

func runnerIdentity(identity githuboidc.VerifiedIdentity) buildjob.RunnerIdentity {
	return buildjob.RunnerIdentity{Repository: identity.Repository, WorkflowRef: identity.WorkflowRef, Ref: identity.Ref, EventName: identity.EventName, RunID: identity.RunID, RunAttempt: identity.RunAttempt}
}

func runnerOIDCError(err error) error {
	code := err.Error()
	switch code {
	case "OIDC_SIGNATURE_INVALID", "OIDC_ISSUER_INVALID", "OIDC_AUDIENCE_INVALID", "OIDC_EXP_INVALID", "OIDC_NBF_INVALID", "OIDC_IAT_INVALID", "OIDC_TIME_INVALID", "OIDC_CLAIMS_INVALID", "OIDC_TOKEN_INVALID":
		return buildjob.Error{Code: code, Status: 401, Message: "GitHub OIDC token is invalid.", Cause: "oidc"}
	default:
		return buildjob.Error{Code: "OIDC_INVALID", Status: 401, Message: "GitHub OIDC token is invalid.", Cause: "oidc"}
	}
}
