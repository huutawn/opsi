package webhookrelay

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/actiondevice"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func (s *Server) ensureActionDeviceStore() {
	s.actionDeviceMu.Lock()
	defer s.actionDeviceMu.Unlock()
	if s.actionDevices != nil {
		return
	}
	if postgresRegistry, ok := s.Registry.(registry.PostgresService); ok && postgresRegistry.DB != nil {
		s.actionDevices = actiondevice.NewPostgresStore(postgresRegistry.DB)
		return
	}
	s.actionDevices = actiondevice.NewMemoryStore()
}

func (s *Server) actionDeviceService() actiondevice.Service {
	s.ensureActionDeviceStore()
	return actiondevice.Service{Store: s.actionDevices, Now: s.clock}
}

func (s *Server) handleActionDevices(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("project_id"))
	principal, ok := s.authorizeProject(w, r, projectID)
	if !ok || principal.UserID == "" {
		if ok {
			writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "AUTH_REQUIRED", Message: "authenticated principal is required", RequestID: r.Header.Get("X-Request-ID")})
		}
		return
	}
	trusted := actiondevice.Principal{ProjectID: principal.ProjectID, UserID: principal.UserID, Role: principal.Role}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			DisplayName    string `json:"display_name"`
			PublicKey      string `json:"public_key"`
			IdempotencyKey string `json:"idempotency_key"`
			OwnerPrincipal string `json:"owner_principal,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid action device request")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid action device request")
			return
		}
		publicKey, err := base64.StdEncoding.DecodeString(body.PublicKey)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			writeError(w, http.StatusBadRequest, "invalid Ed25519 public key")
			return
		}
		device, replay, err := s.actionDeviceService().Register(r.Context(), trusted, actiondevice.RegisterRequest{DisplayName: body.DisplayName, PublicKey: publicKey, IdempotencyKey: body.IdempotencyKey})
		if err != nil {
			writeActionDeviceError(w, r, err)
			return
		}
		device.PublicKey = nil
		device.IdempotencyKey = ""
		status := http.StatusCreated
		if replay {
			status = http.StatusOK
		}
		writeJSON(w, status, device)
	case http.MethodGet:
		devices, err := s.actionDeviceService().List(r.Context(), trusted)
		if err != nil {
			writeActionDeviceError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleActionDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := strings.TrimSpace(r.PathValue("project_id"))
	principal, ok := s.authorizeProject(w, r, projectID)
	if !ok || principal.UserID == "" {
		if ok {
			writeError(w, http.StatusUnauthorized, "authenticated principal is required")
		}
		return
	}
	device, changed, err := s.actionDeviceService().Revoke(r.Context(), actiondevice.Principal{ProjectID: principal.ProjectID, UserID: principal.UserID, Role: principal.Role}, r.PathValue("device_id"))
	if err != nil {
		writeActionDeviceError(w, r, err)
		return
	}
	device.PublicKey = nil
	device.IdempotencyKey = ""
	writeJSON(w, http.StatusOK, map[string]any{"device": device, "revoked": true, "changed": changed})
}

func (s *Server) handleAgentActionDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := strings.TrimSpace(r.PathValue("project_id"))
	nodeID := strings.TrimSpace(r.Header.Get("X-Opsi-Node-ID"))
	if projectID == "" || nodeID == "" || s.Registry == nil {
		writeError(w, http.StatusUnauthorized, "authenticated Agent is required")
		return
	}
	token := bearerToken(r)
	if _, err := s.Registry.VerifyAgent(projectID, nodeID, token); err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if s.Config.RequireAgentSignatures && !validAgentSignature(r, token) {
		writeError(w, http.StatusUnauthorized, "Agent signature is invalid")
		return
	}
	s.ensureActionDeviceStore()
	device, err := s.actionDevices.Get(r.Context(), projectID, r.PathValue("device_id"))
	if err != nil || device.Status != actiondevice.DeviceActive || len(device.PublicKey) != ed25519.PublicKeySize {
		writeError(w, http.StatusNotFound, "active action device not found")
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func writeActionDeviceError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusServiceUnavailable, "ACTION_DEVICE_STORAGE_UNAVAILABLE"
	switch {
	case errors.Is(err, actiondevice.ErrPrincipalRequired):
		status, code = http.StatusUnauthorized, "AUTH_REQUIRED"
	case errors.Is(err, actiondevice.ErrPermissionDenied):
		status, code = http.StatusForbidden, "PERMISSION_DENIED"
	case errors.Is(err, actiondevice.ErrInvalidDevice):
		status, code = http.StatusBadRequest, "ACTION_DEVICE_INVALID"
	case errors.Is(err, actiondevice.ErrDeviceNotFound):
		status, code = http.StatusNotFound, "ACTION_DEVICE_NOT_FOUND"
	case errors.Is(err, actiondevice.ErrReplayConflict):
		status, code = http.StatusConflict, "ACTION_DEVICE_REPLAY_CONFLICT"
	}
	writeRegistryError(w, registry.APIError{Status: status, Code: code, Message: err.Error(), RequestID: r.Header.Get("X-Request-ID")})
}
