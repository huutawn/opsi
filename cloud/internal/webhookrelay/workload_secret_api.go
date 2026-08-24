package webhookrelay

import (
	"net/http"
	"net/url"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

// handleWorkloadSecretAPI exposes metadata and one-way plaintext upsert only.
// Secret material is accepted by the vault and is never included in a
// response, event, audit metadata, or error message.
func (s *Server) handleWorkloadSecretAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 5 || parts[2] != "applications" || parts[4] != "workload-secrets" {
		return false
	}
	applicationID := parts[3]
	services, err := s.Registry.ListServices(projectID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return true
	}
	found := false
	secretScope := ""
	for _, service := range services {
		if (service.ID == applicationID || service.Name == applicationID) && service.Status != "deleted" {
			found = true
			secretScope = service.ID
			break
		}
	}
	if !found {
		runs, listErr := s.DeploymentRuns.List(r.Context(), projectID, 100)
		if listErr != nil {
			writeRegistryFailure(w, r, listErr)
			return true
		}
		for _, run := range runs {
			if run.State != "awaiting_input" && run.State != "awaiting_approval" {
				continue
			}
			for _, application := range run.Plan.Applications {
				if application.Key == applicationID {
					found, secretScope = true, "planned:"+applicationID
					break
				}
			}
			if found {
				break
			}
		}
	}
	if !found {
		writeRegistryError(w, registryError(http.StatusNotFound, "WORKLOAD_SECRET_APPLICATION_NOT_FOUND", "Application was not found in this project.", r))
		return true
	}
	if s.Resources.Credentials == nil {
		writeRegistryError(w, registryError(http.StatusServiceUnavailable, "WORKLOAD_SECRET_AUTHORITY_UNAVAILABLE", "Workload-secret authority is unavailable.", r))
		return true
	}
	if len(parts) == 5 {
		switch r.Method {
		case http.MethodGet:
			if !s.requireRole(w, r, principal, projectID, "workload_secret", applicationID, "owner", "admin", "developer", "viewer", "support") {
				return true
			}
			metadata, listErr := s.Resources.Credentials.ListWorkloadSecrets(r.Context(), projectID, secretScope)
			writeRegistryResult(w, r, map[string]any{"workload_secrets": metadata}, listErr, http.StatusOK)
			return true
		case http.MethodPut:
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "workload_secret", applicationID, "owner", "admin", "developer") {
				return true
			}
			var request struct {
				LogicalName string `json:"logical_name"`
				Value       string `json:"value"`
			}
			if !decodeJSON(w, r, &request) {
				return true
			}
			secretID := workflowSecretID(projectID, secretScope, request.LogicalName)
			metadata, reused, upsertErr := s.Resources.Credentials.UpsertWorkloadSecret(r.Context(), resourcev1.WorkloadSecretUpsert{CredentialID: secretID, ProjectID: projectID, ServiceID: secretScope, LogicalName: request.LogicalName, Value: request.Value, IdempotencyKey: r.Header.Get("Idempotency-Key")})
			request.Value = ""
			writeRegistryResult(w, r, map[string]any{"workload_secret": metadata, "reused": reused}, upsertErr, http.StatusOK)
			return true
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
	}
	if len(parts) == 6 && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "workload_secret", applicationID, "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		logicalName, decodeErr := url.PathUnescape(parts[5])
		if decodeErr != nil {
			writeRegistryError(w, registryError(http.StatusBadRequest, "WORKLOAD_SECRET_NAME_INVALID", "Workload-secret logical name is invalid.", r))
			return true
		}
		metadata, getErr := s.Resources.Credentials.GetWorkloadSecret(r.Context(), projectID, secretScope, logicalName)
		if getErr != nil {
			writeRegistryError(w, registryError(http.StatusNotFound, "WORKLOAD_SECRET_NOT_FOUND", "Workload secret was not found.", r))
			return true
		}
		writeJSON(w, http.StatusOK, metadata)
		return true
	}
	return false
}

func registryError(status int, code, message string, r *http.Request) registry.APIError {
	return registry.APIError{Status: status, Code: code, Message: message, RequestID: r.Header.Get("X-Request-ID")}
}
