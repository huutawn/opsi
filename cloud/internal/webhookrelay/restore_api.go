package webhookrelay

import (
	"errors"
	"net/http"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	restoredomain "github.com/opsi-dev/opsi/cloud/internal/restore"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

func (s *Server) handleRestoreAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) == 5 && parts[2] == "backups" && parts[4] == "restore-review" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "backup", parts[3], "owner", "admin", "developer") {
			return true
		}
		var request restorev1.ReviewRequest
		if !decodeResourceJSON(w, r, &request) {
			return true
		}
		value, err := s.Restores.Review(r.Context(), projectID, parts[3], request.TargetResourceID, principal.UserID)
		writeRestoreResult(w, r, map[string]any{"review": value}, err, http.StatusAccepted)
		return true
	}
	if len(parts) == 5 && parts[2] == "backups" && parts[4] == "restores" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "backup", parts[3], "owner", "admin", "developer") {
			return true
		}
		var request restorev1.CreateRequest
		if !decodeResourceJSON(w, r, &request) {
			return true
		}
		value, reused, err := s.Restores.Create(r.Context(), projectID, parts[3], request.TargetResourceID, request.ReviewID, principal.UserID, r.Header.Get("Idempotency-Key"))
		if err == nil && !reused {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RESTORE_REQUESTED", "restore", value.ID, "success", map[string]any{"backup_id": value.BackupID, "target_resource_id": value.TargetResourceID, "artifact_sha256": value.ArtifactSHA256})
		}
		writeRestoreResult(w, r, map[string]any{"restore": value, "reused": reused}, err, http.StatusAccepted)
		return true
	}
	if len(parts) == 4 && parts[2] == "restore-reviews" && r.Method == http.MethodGet {
		value, err := s.Restores.GetReview(r.Context(), projectID, parts[3])
		writeRestoreResult(w, r, value, err, http.StatusOK)
		return true
	}
	if len(parts) == 3 && parts[2] == "restores" && r.Method == http.MethodGet {
		values, err := s.Restores.List(r.Context(), projectID, r.URL.Query().Get("backup_id"), r.URL.Query().Get("target_resource_id"))
		writeRestoreResult(w, r, map[string]any{"restores": values}, err, http.StatusOK)
		return true
	}
	if len(parts) == 4 && parts[2] == "restores" && r.Method == http.MethodGet {
		value, err := s.Restores.Get(r.Context(), projectID, parts[3])
		writeRestoreResult(w, r, value, err, http.StatusOK)
		return true
	}
	return false
}

func writeRestoreResult[T any](w http.ResponseWriter, r *http.Request, value T, err error, status int) {
	if err == nil {
		writeJSON(w, status, value)
		return
	}
	var restoreErr restoredomain.Error
	if errors.As(err, &restoreErr) {
		writeRegistryError(w, registry.APIError{Status: restoreErr.Status, Code: restoreErr.Code, Message: restoreErr.Message, RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if errors.Is(err, restoredomain.ErrNotFound) {
		writeRegistryError(w, registry.APIError{Status: http.StatusNotFound, Code: "RESTORE_NOT_FOUND", Message: "Restore authority was not found.", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeResourceResult(w, r, value, err, status)
}

func (s *Server) handleAgentRestoreReviewResult(w http.ResponseWriter, r *http.Request) {
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
	var result restorev1.ReviewResult
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	value, err := s.Restores.CompleteReview(r.Context(), projectID, parts[len(parts)-2], result)
	if err != nil {
		writeRestoreResult(w, r, value, err, http.StatusOK)
		return
	}
	if value.Lifecycle == restorev1.ReviewSucceeded {
		s.Registry.Audit(agent.OrgID, projectID, agent.ID, "RESTORE_REVIEWED", "restore_review", value.ID, "success", map[string]any{"backup_id": value.BackupID, "target_resource_id": value.TargetResourceID, "pristine": value.Pristine, "target_pvc_uid": value.TargetPVCUID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": value})
}

func (s *Server) handleAgentRestoreResult(w http.ResponseWriter, r *http.Request) {
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
	var result restorev1.Result
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	id := parts[len(parts)-2]
	current, _ := s.Restores.Get(r.Context(), projectID, id)
	value, err := s.Restores.Complete(r.Context(), projectID, id, result)
	if err != nil {
		writeRestoreResult(w, r, value, err, http.StatusOK)
		return
	}
	action, outcome := "RESTORE_FAILED", "failure"
	metadata := map[string]any{"backup_id": value.BackupID, "target_resource_id": value.TargetResourceID, "failure_code": value.FailureCode}
	if result.Status == restorev1.LifecycleRunning {
		if current.Lifecycle != restorev1.LifecycleLeased {
			writeJSON(w, http.StatusOK, map[string]any{"restore": value})
			return
		}
		action, outcome, metadata = "RESTORE_STARTED", "success", map[string]any{"backup_id": value.BackupID, "target_resource_id": value.TargetResourceID, "attempt_count": value.AttemptCount}
	} else if value.Lifecycle == restorev1.LifecycleSucceeded {
		action, outcome, metadata = "RESTORE_SUCCEEDED", "success", map[string]any{"backup_id": value.BackupID, "target_resource_id": value.TargetResourceID, "artifact_sha256": value.ArtifactSHA256, "objects": value.RestoredObjects}
	}
	s.Registry.Audit(agent.OrgID, projectID, agent.ID, action, "restore", id, outcome, metadata)
	writeJSON(w, http.StatusOK, map[string]any{"restore": value})
}
