package webhookrelay

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func (s *Server) handleBuildJobsAPI(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	applicationID := r.PathValue("application_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPost:
		if !s.requireRole(w, r, principal, projectID, "build_job", applicationID, "owner", "admin", "developer") || !decodeBuildJobIntent(w, r) {
			return
		}
		job, reused, err := s.BuildJobs.Create(r.Context(), projectID, applicationID, principal.UserID, r.Header.Get("Idempotency-Key"))
		if err != nil {
			writeBuildJobFailure(w, r, err)
			return
		}
		status := http.StatusCreated
		if reused {
			status = http.StatusOK
		}
		writeJSON(w, status, job)
	case http.MethodGet:
		if !s.requireRole(w, r, principal, projectID, "build_job", applicationID, "owner", "admin", "developer", "viewer", "support") {
			return
		}
		limit, err := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("limit"), "50"))
		if err != nil {
			writeBuildJobFailure(w, r, buildjob.Error{Code: "BUILD_JOB_LIST_INVALID", Status: 400, Message: "status or limit is invalid", Cause: "request"})
			return
		}
		jobs, err := s.BuildJobs.List(r.Context(), projectID, applicationID, r.URL.Query().Get("status"), limit)
		if err != nil {
			writeBuildJobFailure(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"build_jobs": jobs})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBuildJobAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	applicationID := r.PathValue("application_id")
	jobID := r.PathValue("build_job_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "build_job", jobID, "owner", "admin", "developer", "viewer", "support") {
		return
	}
	job, err := s.BuildJobs.Get(r.Context(), projectID, applicationID, jobID)
	if err != nil {
		writeBuildJobFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func decodeBuildJobIntent(w http.ResponseWriter, r *http.Request) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	var intent map[string]json.RawMessage
	if err := decoder.Decode(&intent); err != nil || intent == nil || len(intent) != 0 {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "BUILD_JOB_INTENT_INVALID", Message: "BuildJob intent must be an empty JSON object.", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "BUILD_JOB_INTENT_INVALID", Message: "BuildJob intent must contain one JSON object.", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	return true
}

func writeBuildJobFailure(w http.ResponseWriter, r *http.Request, err error) {
	var typed buildjob.Error
	if !errors.As(err, &typed) {
		typed = buildjob.Error{Code: "BUILD_JOB_INTERNAL", Status: 500, Message: "Internal server error.", Cause: "internal"}
	}
	writeRegistryError(w, registry.APIError{Status: typed.Status, Code: typed.Code, Message: typed.Message, RequestID: r.Header.Get("X-Request-ID")})
}
