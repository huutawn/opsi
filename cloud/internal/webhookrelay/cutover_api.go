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

	// POST /api/projects/{project}/applications/{application}/cutovers
	// POST /api/projects/{project}/services/{service}/cutovers
	if len(parts) == 5 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "application", parts[3], "owner", "admin", "developer") {
			return true
		}
		var request cutoverv1.ApplyRequest
		if !decodeResourceJSON(w, r, &request) {
			return true
		}
		value, reused, err := s.Cutovers.Apply(r.Context(), projectID, parts[3], request, principal.UserID, r.Header.Get("Idempotency-Key"))
		if err == nil && !reused {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_REQUESTED", "cutover", value.ID, "success", map[string]any{
				"application_id":    value.ApplicationID,
				"cutover_review_id": value.CutoverReviewID,
				"source_binding_id": value.SourceBindingID,
				"target_binding_id": value.TargetBindingID,
			})
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_APPLY_STARTED", "cutover", value.ID, "success", map[string]any{
				"application_id":                   value.ApplicationID,
				"pre_cutover_config_revision":      value.PreCutoverApplicationConfigRevision,
				"resulting_config_revision":        value.ResultingApplicationConfigRevision,
				"source_rollback_authority_intact": true,
			})
			if value.DeploymentJobID != "" {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_DEPLOYMENT_STARTED", "cutover", value.ID, "success", map[string]any{
					"application_id":    value.ApplicationID,
					"deployment_job_id": value.DeploymentJobID,
				})
			}
		}
		writeCutoverResult(w, r, map[string]any{"cutover": value, "application_cutover": value, "reused": reused}, err, http.StatusAccepted)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutovers
	// GET /api/projects/{project}/services/{service}/cutovers
	if len(parts) == 5 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListCutovers(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"cutovers": values, "application_cutovers": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutovers/{cutover}
	// GET /api/projects/{project}/services/{service}/cutovers/{cutover}
	if len(parts) == 6 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetCutover(r.Context(), projectID, parts[5])
		writeCutoverResult(w, r, map[string]any{"cutover": value, "application_cutover": value}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutovers
	// GET /api/projects/{project}/cutovers
	if len(parts) == 3 && (parts[2] == "application-cutovers" || parts[2] == "cutovers") && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListCutovers(r.Context(), projectID, r.URL.Query().Get("application_id"))
		writeCutoverResult(w, r, map[string]any{"cutovers": values, "application_cutovers": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutovers/{cutover}
	// GET /api/projects/{project}/cutovers/{cutover}
	if len(parts) == 4 && (parts[2] == "application-cutovers" || parts[2] == "cutovers") && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetCutover(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"cutover": value, "application_cutover": value}, err, http.StatusOK)
		return true
	}

	// POST /api/projects/{project}/applications/{application}/cutovers/{cutover}/rollbacks
	// POST /api/projects/{project}/services/{service}/cutovers/{cutover}/rollbacks
	if len(parts) == 7 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && (parts[6] == "rollbacks" || parts[6] == "cutover-rollbacks") && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "application", parts[3], "owner", "admin", "developer") {
			return true
		}
		value, reused, err := s.Cutovers.Rollback(r.Context(), projectID, parts[3], parts[5], principal.UserID, r.Header.Get("Idempotency-Key"))
		if err == nil && !reused {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_ROLLBACK_REQUESTED", "cutover_rollback", value.ID, "success", map[string]any{
				"application_id":    value.ApplicationID,
				"cutover_id":        value.CutoverID,
				"source_binding_id": value.SourceBindingID,
				"target_binding_id": value.TargetBindingID,
			})
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_ROLLBACK_APPLY_STARTED", "cutover_rollback", value.ID, "success", map[string]any{
				"application_id":            value.ApplicationID,
				"pre_rollback_revision":     value.CurrentApplicationConfigRevision,
				"resulting_config_revision": value.ResultingApplicationConfigRevision,
			})
			if value.DeploymentJobID != "" {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_ROLLBACK_DEPLOYMENT_STARTED", "cutover_rollback", value.ID, "success", map[string]any{
					"application_id":    value.ApplicationID,
					"deployment_job_id": value.DeploymentJobID,
				})
			}
		}
		writeCutoverResult(w, r, map[string]any{"rollback": value, "cutover_rollback": value, "reused": reused}, err, http.StatusAccepted)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutovers/{cutover}/rollbacks
	// GET /api/projects/{project}/services/{service}/cutovers/{cutover}/rollbacks
	if len(parts) == 7 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && (parts[6] == "rollbacks" || parts[6] == "cutover-rollbacks") && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListRollbacks(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"rollbacks": values, "cutover_rollbacks": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutovers/{cutover}/rollbacks/{rollback}
	// GET /api/projects/{project}/services/{service}/cutovers/{cutover}/rollbacks/{rollback}
	if len(parts) == 8 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && (parts[6] == "rollbacks" || parts[6] == "cutover-rollbacks") && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetRollback(r.Context(), projectID, parts[7])
		writeCutoverResult(w, r, map[string]any{"rollback": value, "cutover_rollback": value}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutover-rollbacks
	// GET /api/projects/{project}/cutover-rollbacks
	if len(parts) == 3 && (parts[2] == "application-cutover-rollbacks" || parts[2] == "cutover-rollbacks") && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListRollbacks(r.Context(), projectID, r.URL.Query().Get("application_id"))
		writeCutoverResult(w, r, map[string]any{"rollbacks": values, "cutover_rollbacks": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutover-rollbacks/{rollback}
	// GET /api/projects/{project}/cutover-rollbacks/{rollback}
	if len(parts) == 4 && (parts[2] == "application-cutover-rollbacks" || parts[2] == "cutover-rollbacks") && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetRollback(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"rollback": value, "cutover_rollback": value}, err, http.StatusOK)
		return true
	}

	// POST /api/projects/{project}/applications/{application}/cutovers/{cutover}/finalize
	// POST /api/projects/{project}/services/{service}/cutovers/{cutover}/finalize
	if len(parts) == 7 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && (parts[6] == "finalize" || parts[6] == "finalizations") && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "application", parts[3], "owner", "admin", "developer") {
			return true
		}
		value, reused, err := s.Cutovers.Finalize(r.Context(), projectID, parts[3], parts[5], principal.UserID, r.Header.Get("Idempotency-Key"))
		if err == nil && !reused {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_FINALIZE_REQUESTED", "cutover_finalization", value.ID, "success", map[string]any{
				"application_id":    value.ApplicationID,
				"cutover_id":        value.CutoverID,
				"source_binding_id": value.SourceBindingID,
				"target_binding_id": value.TargetBindingID,
			})
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_FINALIZE_VALIDATED", "cutover_finalization", value.ID, "success", map[string]any{
				"application_id":              value.ApplicationID,
				"cutover_id":                  value.CutoverID,
				"application_config_revision": value.ApplicationConfigRevision,
			})
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_SOURCE_BINDING_REVOKE_STARTED", "cutover_finalization", value.ID, "success", map[string]any{
				"application_id":    value.ApplicationID,
				"source_binding_id": value.SourceBindingID,
			})
			if value.Lifecycle == cutoverv1.FinalizationSucceeded {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "CUTOVER_FINALIZED", "cutover_finalization", value.ID, "success", map[string]any{
					"application_id":         value.ApplicationID,
					"cutover_id":             value.CutoverID,
					"source_binding_revoked": value.VerificationSummary.SourceBindingRevoked,
					"source_retained":        value.VerificationSummary.SourceResourceRetained,
					"evidence_hash":          value.EvidenceHash,
				})
			}
		}
		writeCutoverResult(w, r, map[string]any{"finalization": value, "application_cutover_finalization": value, "reused": reused}, err, http.StatusAccepted)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutovers/{cutover}/finalizations
	// GET /api/projects/{project}/services/{service}/cutovers/{cutover}/finalizations
	if len(parts) == 7 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && (parts[6] == "finalizations" || parts[6] == "finalize") && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListFinalizations(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"finalizations": values, "application_cutover_finalizations": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/applications/{application}/cutovers/{cutover}/finalizations/{finalization}
	// GET /api/projects/{project}/services/{service}/cutovers/{cutover}/finalizations/{finalization}
	if len(parts) == 8 && (parts[2] == "applications" || parts[2] == "services") && (parts[4] == "cutovers" || parts[4] == "application-cutovers") && (parts[6] == "finalizations" || parts[6] == "finalize") && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetFinalization(r.Context(), projectID, parts[7])
		writeCutoverResult(w, r, map[string]any{"finalization": value, "application_cutover_finalization": value}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutover-finalizations
	// GET /api/projects/{project}/cutover-finalizations
	if len(parts) == 3 && (parts[2] == "application-cutover-finalizations" || parts[2] == "cutover-finalizations") && r.Method == http.MethodGet {
		values, err := s.Cutovers.ListFinalizations(r.Context(), projectID, r.URL.Query().Get("application_id"))
		writeCutoverResult(w, r, map[string]any{"finalizations": values, "application_cutover_finalizations": values}, err, http.StatusOK)
		return true
	}

	// GET /api/projects/{project}/application-cutover-finalizations/{finalization}
	// GET /api/projects/{project}/cutover-finalizations/{finalization}
	if len(parts) == 4 && (parts[2] == "application-cutover-finalizations" || parts[2] == "cutover-finalizations") && r.Method == http.MethodGet {
		value, err := s.Cutovers.GetFinalization(r.Context(), projectID, parts[3])
		writeCutoverResult(w, r, map[string]any{"finalization": value, "application_cutover_finalization": value}, err, http.StatusOK)
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
		writeRegistryError(w, registry.APIError{Status: http.StatusNotFound, Code: "CUTOVER_NOT_FOUND", Message: "Cutover authority was not found.", RequestID: r.Header.Get("X-Request-ID")})
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

func (s *Server) handleAgentCutoverResult(w http.ResponseWriter, r *http.Request) {
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
	if len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	var result cutoverv1.CutoverApplyResult
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	cutoverID := parts[len(parts)-1]
	if cutoverID == "result" && len(parts) >= 6 {
		cutoverID = parts[len(parts)-2]
	}
	value, err := s.Cutovers.CompleteCutover(r.Context(), projectID, cutoverID, result)
	if err != nil {
		writeCutoverResult(w, r, value, err, http.StatusOK)
		return
	}
	action, outcome := "CUTOVER_FAILED", "failure"
	metadata := map[string]any{
		"application_id":    value.ApplicationID,
		"source_binding_id": value.SourceBindingID,
		"target_binding_id": value.TargetBindingID,
		"failure_code":      value.FailureCode,
	}
	if value.Lifecycle == cutoverv1.CutoverSucceeded {
		action, outcome = "CUTOVER_SUCCEEDED", "success"
		metadata = map[string]any{
			"application_id":                   value.ApplicationID,
			"source_binding_id":                value.SourceBindingID,
			"target_binding_id":                value.TargetBindingID,
			"source_resource_id":               value.SourceResourceID,
			"target_resource_id":               value.TargetResourceID,
			"deployment_job_id":                value.DeploymentJobID,
			"resulting_config_revision":        value.ResultingApplicationConfigRevision,
			"resulting_config_hash":            value.ResultingApplicationConfigHash,
			"evidence_hash":                    value.EvidenceHash,
			"workload_ready":                   value.VerificationSummary.WorkloadReady,
			"target_db_connected":              value.VerificationSummary.TargetDBConnected,
			"source_rollback_authority_intact": value.VerificationSummary.SourceRollbackPreserved,
		}
	}
	s.Registry.Audit(agent.OrgID, projectID, agent.ID, action, "cutover", value.ID, outcome, metadata)
	writeJSON(w, http.StatusOK, map[string]any{"cutover": value, "application_cutover": value})
}

func (s *Server) handleAgentCutoverRollbackResult(w http.ResponseWriter, r *http.Request) {
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
	if len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	var result cutoverv1.RollbackResult
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	rollbackID := parts[len(parts)-1]
	if rollbackID == "result" && len(parts) >= 6 {
		rollbackID = parts[len(parts)-2]
	}
	value, err := s.Cutovers.CompleteRollback(r.Context(), projectID, rollbackID, result)
	if err != nil {
		writeCutoverResult(w, r, value, err, http.StatusOK)
		return
	}
	action, outcome := "CUTOVER_ROLLBACK_FAILED", "failure"
	metadata := map[string]any{
		"application_id":    value.ApplicationID,
		"cutover_id":        value.CutoverID,
		"source_binding_id": value.SourceBindingID,
		"target_binding_id": value.TargetBindingID,
		"failure_code":      value.FailureCode,
	}
	if value.Lifecycle == cutoverv1.RollbackSucceeded {
		action, outcome = "CUTOVER_ROLLBACK_SUCCEEDED", "success"
		metadata = map[string]any{
			"application_id":            value.ApplicationID,
			"cutover_id":                value.CutoverID,
			"source_binding_id":         value.SourceBindingID,
			"target_binding_id":         value.TargetBindingID,
			"source_resource_id":        value.SourceResourceID,
			"target_resource_id":        value.TargetResourceID,
			"deployment_job_id":         value.DeploymentJobID,
			"resulting_config_revision": value.ResultingApplicationConfigRevision,
			"resulting_config_hash":     value.ResultingApplicationConfigHash,
			"evidence_hash":             value.EvidenceHash,
			"workload_ready":            value.VerificationSummary.WorkloadReady,
			"source_db_connected":       value.VerificationSummary.SourceDBConnected,
			"target_preserved":          value.VerificationSummary.TargetAuthorityPreserved,
		}
	}
	s.Registry.Audit(agent.OrgID, projectID, agent.ID, action, "cutover_rollback", value.ID, outcome, metadata)
	writeJSON(w, http.StatusOK, map[string]any{"rollback": value, "cutover_rollback": value})
}

func (s *Server) handleAgentCutoverFinalizationResult(w http.ResponseWriter, r *http.Request) {
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
	if len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	var result cutoverv1.FinalizeResult
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	finalizationID := parts[len(parts)-1]
	if finalizationID == "result" && len(parts) >= 6 {
		finalizationID = parts[len(parts)-2]
	}
	value, err := s.Cutovers.CompleteFinalization(r.Context(), projectID, finalizationID, result)
	if err != nil {
		writeCutoverResult(w, r, value, err, http.StatusOK)
		return
	}
	action, outcome := "CUTOVER_FINALIZE_FAILED", "failure"
	metadata := map[string]any{
		"application_id":    value.ApplicationID,
		"cutover_id":        value.CutoverID,
		"source_binding_id": value.SourceBindingID,
		"target_binding_id": value.TargetBindingID,
		"failure_code":      value.FailureCode,
	}
	if value.Lifecycle == cutoverv1.FinalizationSucceeded {
		action, outcome = "CUTOVER_FINALIZED", "success"
		metadata = map[string]any{
			"application_id":         value.ApplicationID,
			"cutover_id":             value.CutoverID,
			"source_binding_id":      value.SourceBindingID,
			"target_binding_id":      value.TargetBindingID,
			"source_resource_id":     value.SourceResourceID,
			"target_resource_id":     value.TargetResourceID,
			"source_binding_revoked": value.VerificationSummary.SourceBindingRevoked,
			"source_retained":        value.VerificationSummary.SourceResourceRetained,
			"evidence_hash":          value.EvidenceHash,
			"target_connected":       value.VerificationSummary.TargetDBConnected,
		}
	}
	s.Registry.Audit(agent.OrgID, projectID, agent.ID, action, "cutover_finalization", value.ID, outcome, metadata)
	writeJSON(w, http.StatusOK, map[string]any{"finalization": value, "application_cutover_finalization": value})
}
