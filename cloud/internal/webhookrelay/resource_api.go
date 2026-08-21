package webhookrelay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type resourceTopologySource interface {
	TopologyResources(context.Context, string) ([]resourcev1.Resource, error)
}

func resourceIdentities(ctx context.Context, source resourceTopologySource, projectID string) ([]topologyv1.ResourceIdentity, error) {
	values, err := source.TopologyResources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]topologyv1.ResourceIdentity, 0, len(values))
	for _, value := range values {
		runtimeID := ""
		if value.Runtime != nil {
			runtimeID = value.Runtime.Spec.Assignment.RuntimeID
		}
		identity := topologyv1.ResourceIdentity{ID: value.ID, ProjectID: value.ProjectID, EnvironmentID: value.EnvironmentID, Name: value.Name, Kind: string(value.Kind), Type: string(value.Type), Lifecycle: string(value.Lifecycle), RuntimeID: runtimeID}
		if value.Managed != nil {
			identity.Version, identity.Replicas, identity.CPUMillicores, identity.MemoryBytes = value.Managed.Version, value.Managed.Replicas, value.Managed.CPUMillicores, value.Managed.MemoryBytes
		}
		out = append(out, identity)
	}
	return out, nil
}

func (s *Server) handleResourceAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 3 {
		return false
	}
	if s.handleBackupAPI(w, r, projectID, parts, principal) {
		return true
	}
	if s.handleRestoreAPI(w, r, projectID, parts, principal) {
		return true
	}
	if s.handleCutoverAPI(w, r, projectID, parts, principal) {
		return true
	}
	if parts[2] == "resource-types" && len(parts) == 3 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"resource_types": s.Resources.Definitions()})
		return true
	}
	if parts[2] == "resource-bindings" {
		if len(parts) == 3 && r.Method == http.MethodGet {
			value, err := s.Resources.ListBindings(r.Context(), projectID, r.URL.Query().Get("environment_id"))
			writeResourceResult(w, r, map[string]any{"bindings": value}, err, http.StatusOK)
			return true
		}
		if len(parts) == 3 && r.Method == http.MethodPost {
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "resource_binding", projectID, "owner", "admin", "developer") {
				return true
			}
			var request resourcev1.CreateBindingRequest
			if !decodeResourceJSON(w, r, &request) {
				return true
			}
			value, reused, err := s.Resources.CreateBinding(r.Context(), projectID, r.Header.Get("Idempotency-Key"), request)
			if err == nil && !reused {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RESOURCE_BINDING_CREATED", "resource_binding", value.ID, "success", map[string]any{"protocol": value.Protocol, "target_id": value.Target.ID})
			}
			writeResourceResult(w, r, map[string]any{"binding": value, "reused": reused}, err, http.StatusCreated)
			return true
		}
		if len(parts) == 4 && r.Method == http.MethodDelete {
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "resource_binding", parts[3], "owner", "admin", "developer") {
				return true
			}
			value, err := s.Resources.DeleteBinding(r.Context(), projectID, parts[3])
			if err == nil {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RESOURCE_BINDING_DELETE_REQUESTED", "resource_binding", parts[3], "success", nil)
			}
			writeResourceResult(w, r, value, err, http.StatusAccepted)
			return true
		}
		return false
	}
	if parts[2] == "retained-storages" {
		if len(parts) == 3 && r.Method == http.MethodGet {
			value, err := s.Resources.ListRetainedStorage(r.Context(), projectID, r.URL.Query().Get("environment_id"))
			writeResourceResult(w, r, map[string]any{"retained_storages": value}, err, http.StatusOK)
			return true
		}
		if len(parts) == 4 && r.Method == http.MethodGet {
			value, err := s.Resources.GetRetainedStorage(r.Context(), projectID, parts[3])
			writeResourceResult(w, r, value, err, http.StatusOK)
			return true
		}
		if len(parts) == 5 && parts[4] == "review" && r.Method == http.MethodPost {
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "retained_storage", parts[3], "owner", "admin") {
				return true
			}
			value, err := s.Resources.ReviewRetainedStorageDestroy(r.Context(), projectID, parts[3], principal.UserID)
			if err == nil {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RETAINED_STORAGE_DESTROY_REVIEWED", "retained_storage", parts[3], "success", map[string]any{"review_token": value.ReviewToken, "revision": value.Revision})
			}
			writeResourceResult(w, r, map[string]any{"review": value}, err, http.StatusOK)
			return true
		}
		if len(parts) == 5 && parts[4] == "destroy" && r.Method == http.MethodPost {
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "retained_storage", parts[3], "owner", "admin") {
				return true
			}
			var request resourcev1.DestroyRetainedStorageRequest
			if !decodeResourceJSON(w, r, &request) {
				return true
			}
			value, reused, err := s.Resources.RequestRetainedStorageDestroy(r.Context(), projectID, parts[3], principal.UserID, r.Header.Get("Idempotency-Key"), request)
			if err == nil && !reused {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RETAINED_STORAGE_DESTROY_REQUESTED", "retained_storage", parts[3], "success", map[string]any{"review_token": request.ReviewToken, "lifecycle": value.Lifecycle})
			}
			writeResourceResult(w, r, map[string]any{"retained_storage": value, "reused": reused}, err, http.StatusAccepted)
			return true
		}
		return false
	}
	if parts[2] != "resources" {
		return false
	}
	if len(parts) == 3 && r.Method == http.MethodGet {
		value, err := s.Resources.List(r.Context(), projectID, r.URL.Query().Get("environment_id"))
		writeResourceResult(w, r, map[string]any{"resources": value}, err, http.StatusOK)
		return true
	}
	if len(parts) == 3 && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "resource", projectID, "owner", "admin", "developer") {
			return true
		}
		var request resourcev1.CreateRequest
		if !decodeResourceJSON(w, r, &request) {
			return true
		}
		value, reused, err := s.Resources.Create(r.Context(), projectID, principal.UserID, r.Header.Get("Idempotency-Key"), request)
		if err == nil && !reused {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RESOURCE_CREATED", "resource", value.ID, "success", map[string]any{"kind": value.Kind, "type": value.Type, "lifecycle": value.Lifecycle})
		}
		writeResourceResult(w, r, map[string]any{"resource": value, "reused": reused}, err, http.StatusCreated)
		return true
	}
	if len(parts) == 4 && r.Method == http.MethodGet {
		value, err := s.Resources.Get(r.Context(), projectID, parts[3])
		writeResourceResult(w, r, value, err, http.StatusOK)
		return true
	}
	if len(parts) == 4 && r.Method == http.MethodPut {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "resource", parts[3], "owner", "admin", "developer") {
			return true
		}
		var request resourcev1.UpdateRequest
		if !decodeResourceJSON(w, r, &request) {
			return true
		}
		value, err := s.Resources.Update(r.Context(), projectID, parts[3], request)
		writeResourceResult(w, r, value, err, http.StatusOK)
		return true
	}
	if len(parts) == 4 && r.Method == http.MethodDelete {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "resource", parts[3], "owner", "admin") {
			return true
		}
		value, err := s.Resources.DeleteIntent(r.Context(), projectID, parts[3], principal.UserID)
		if err == nil {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RESOURCE_DELETE_REQUESTED", "resource", parts[3], "success", map[string]any{"result": "runtime deletion requested; persistent storage will be retained"})
		}
		writeResourceResult(w, r, value, err, http.StatusAccepted)
		return true
	}
	return false
}

func decodeResourceJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_RESOURCE_JSON", Message: "Resource request is not valid strict JSON.", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_RESOURCE_JSON", Message: "Resource request must contain exactly one JSON document.", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	return true
}

func writeResourceResult[T any](w http.ResponseWriter, r *http.Request, value T, err error, status int) {
	if err == nil {
		writeJSON(w, status, value)
		return
	}
	var resourceErr resource.Error
	if errors.As(err, &resourceErr) {
		writeRegistryError(w, registry.APIError{Status: resourceErr.Status, Code: resourceErr.Code, Message: resourceErr.Message, RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if errors.Is(err, resource.ErrNotFound) {
		writeRegistryError(w, registry.APIError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "Resource was not found.", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeRegistryFailure(w, r, err)
}
