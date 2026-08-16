package webhookrelay

import (
	"errors"
	"net/http"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	cutoverdomain "github.com/opsi-dev/opsi/cloud/internal/cutover"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
)

func (s *Server) handleCutoverAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	// POST /api/projects/{project}/applications/{application}/cutover-reviews
	// POST /api/projects/{project}/services/{service}/cutover-reviews
	if len(parts) == 5 && (parts[2] == "applications" || parts[2] == "services") && parts[4] == "cutover-reviews" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "application", parts[3], "owner", "admin", "developer") {
			return true
		}
		var request cutoverv1.ReviewRequest
		if !decodeResourceJSON(w, r, &request) {
			return true
		}
		value, reused, err := s.Cutovers.Review(r.Context(), projectID, parts[3], request, principal.UserID, r.Header.Get("Idempotency-Key"))
		if err == nil && !reused {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_REVIEW_REQUESTED", "cutover_review", value.ID, "success", map[string]any{
				"application_id":    value.ApplicationID,
				"source_binding_id": value.SourceBindingID,
				"target_binding_id": value.TargetBindingID,
				"backup_id":         value.BackupID,
				"target_restore_id": value.TargetRestoreID,
			})
		}
		writeCutoverResult(w, r, map[string]any{"cutover_review": value, "review": value, "reused": reused}, err, http.StatusAccepted)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutover-reviews
	// GET /api/projects/{project}/services/{service}/cutover-reviews
	if len(parts) == 5 && (parts[2] == "applications" || parts[2] == "services") && parts[4] == "cutover-reviews" && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListReviews(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"cutover_reviews": values, "reviews": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutover-reviews/{review}
	// GET /api/projects/{project}/services/{service}/cutover-reviews/{review}
	if len(parts) == 6 && (parts[2] == "applications" || parts[2] == "services") && parts[4] == "cutover-reviews" && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetReview(r.Context(), projectID, parts[5])
		writeCutoverResult(w, r, map[string]any{"cutover_review": value, "review": value}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutover-reviews
	// GET /api/projects/{project}/cutover-reviews
	if len(parts) == 3 && (parts[2] == "application-cutover-reviews" || parts[2] == "cutover-reviews") && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListReviews(r.Context(), projectID, r.URL.Query().Get("application_id"))
		writeCutoverResult(w, r, map[string]any{"cutover_reviews": values, "reviews": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutover-reviews/{review}
	// GET /api/projects/{project}/cutover-reviews/{review}
	if len(parts) == 4 && (parts[2] == "application-cutover-reviews" || parts[2] == "cutover-reviews") && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetReview(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"cutover_review": value, "review": value}, err, http.StatusOK)
		return true
	}

	return false
}

func writeCutoverResult[T any](w http.ResponseWriter, r *http.Request, value T, err error, status int) {
	if err == nil {
		writeJSON(w, status, value)
		return
	}
	var cutoverErr cutoverdomain.Error
	if errors.As(err, &cutoverErr) {
		writeRegistryError(w, registry.APIError{Status: cutoverErr.Status, Code: cutoverErr.Code, Message: cutoverErr.Message, RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if errors.Is(err, cutoverdomain.ErrNotFound) {
		writeRegistryError(w, registry.APIError{Status: http.StatusNotFound, Code: "CUTOVER_REVIEW_NOT_FOUND", Message: "Cutover review authority was not found.", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeResourceResult(w, r, value, err, status)
}

func (s *Server) handleAgentCutoverReviewResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID, nodeID := r.URL.Query().Get("project_id"), nodeIDFromAgentPath(r.URL.Path)
	agent, ok := s.authorizeAgent(w, r, projectID, nodeID)
	if !ok {
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 {
		http.NotFound(w, r)
		return
	}
	var result cutoverv1.ReviewResult
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	reviewID := parts[len(parts)-2]
	value, err := s.Cutovers.CompleteReview(r.Context(), projectID, reviewID, result)
	if err != nil {
		writeCutoverResult(w, r, value, err, http.StatusOK)
		return
	}
	action, outcome := "CUTOVER_REVIEW_FAILED", "failure"
	metadata := map[string]any{
		"application_id":    value.ApplicationID,
		"source_binding_id": value.SourceBindingID,
		"target_binding_id": value.TargetBindingID,
		"failure_code":      value.FailureCode,
	}
	if value.Lifecycle == cutoverv1.ReviewSucceeded {
		action, outcome = "CUTOVER_REVIEW_SUCCEEDED", "success"
		metadata = map[string]any{
			"application_id":    value.ApplicationID,
			"source_binding_id": value.SourceBindingID,
			"target_binding_id": value.TargetBindingID,
			"backup_id":         value.BackupID,
			"target_restore_id": value.TargetRestoreID,
			"source_preflight":  value.ValidationSummary.SourceSQLPreflight,
			"target_preflight":  value.ValidationSummary.TargetSQLPreflight,
			"target_role_attrs": value.ValidationSummary.TargetRoleAttributes,
		}
	}
	s.Registry.Audit(agent.OrgID, projectID, agent.ID, action, "cutover_review", value.ID, outcome, metadata)
	writeJSON(w, http.StatusOK, map[string]any{"cutover_review": value, "review": value})
}
