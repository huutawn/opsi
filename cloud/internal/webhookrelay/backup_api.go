package webhookrelay

import (
	"errors"
	"net/http"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	backupdomain "github.com/opsi-dev/opsi/cloud/internal/backup"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
)

func (s *Server) handleBackupAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) >= 3 && parts[2] == "backups" {
		if len(parts) == 3 && r.Method == http.MethodGet {
			values, err := s.Backups.List(r.Context(), projectID, r.URL.Query().Get("resource_id"))
			writeBackupResult(w, r, map[string]any{"backups": values}, err, http.StatusOK)
			return true
		}
		if len(parts) == 4 && r.Method == http.MethodGet {
			value, err := s.Backups.Get(r.Context(), projectID, parts[3])
			writeBackupResult(w, r, value, err, http.StatusOK)
			return true
		}
		return false
	}
	if len(parts) != 5 || parts[2] != "resources" || parts[4] != "backups" {
		return false
	}
	resourceID := parts[3]
	if r.Method == http.MethodGet {
		values, err := s.Backups.List(r.Context(), projectID, resourceID)
		writeBackupResult(w, r, map[string]any{"backups": values}, err, http.StatusOK)
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "resource", resourceID, "owner", "admin", "developer") {
		return true
	}
	var request backupv1.CreateRequest
	if !decodeResourceJSON(w, r, &request) {
		return true
	}
	value, reused, err := s.Backups.Create(r.Context(), projectID, resourceID, principal.UserID, r.Header.Get("Idempotency-Key"))
	if err == nil && !reused {
		s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "BACKUP_REQUESTED", "backup", value.ID, "success", map[string]any{"resource_id": value.SourceResourceID, "backup_type": value.BackupType, "lifecycle": value.Lifecycle})
	}
	writeBackupResult(w, r, map[string]any{"backup": value, "reused": reused}, err, http.StatusAccepted)
	return true
}

func writeBackupResult[T any](w http.ResponseWriter, r *http.Request, value T, err error, status int) {
	if err == nil {
		writeJSON(w, status, value)
		return
	}
	var backupErr backupdomain.Error
	if errors.As(err, &backupErr) {
		writeRegistryError(w, registry.APIError{Status: backupErr.Status, Code: backupErr.Code, Message: backupErr.Message, RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if errors.Is(err, backupdomain.ErrNotFound) {
		writeRegistryError(w, registry.APIError{Status: http.StatusNotFound, Code: "BACKUP_NOT_FOUND", Message: "Backup was not found.", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeResourceResult(w, r, value, err, status)
}
