package webhookrelay

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func (s *Server) handleBootstrapWorker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.Config.BootstrapWorkerToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Bootstrap-Worker-Token")), []byte(s.Config.BootstrapWorkerToken)) != 1 {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "BOOTSTRAP_WORKER_AUTH_REQUIRED", Message: "bootstrap worker token is required", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "internal" || parts[1] != "bootstrap" || parts[2] != "sessions" || parts[4] != "take" {
		if len(parts) == 5 && parts[0] == "internal" && parts[1] == "bootstrap" && parts[2] == "sessions" && parts[4] == "finish" {
			s.handleBootstrapWorkerFinish(w, r, parts[3])
			return
		}
		http.NotFound(w, r)
		return
	}
	sessionID := parts[3]
	credential, ok := s.credentials.Take(sessionID)
	if !ok {
		writeRegistryError(w, registry.APIError{Status: http.StatusGone, Code: "BOOTSTRAP_CREDENTIAL_UNAVAILABLE", Message: "bootstrap credential is expired or already taken", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	reg, ok := s.registrations.TakeForWorker(sessionID)
	if !ok {
		writeRegistryError(w, registry.APIError{Status: http.StatusGone, Code: "AGENT_REGISTRATION_TOKEN_UNAVAILABLE", Message: "agent registration token is expired or already taken", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if _, err := s.Registry.UpdateBootstrapSession(reg.ProjectID, sessionID, "preflight", "bootstrap worker took credential"); err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	s.Registry.Audit(reg.OrgID, reg.ProjectID, "bootstrap-worker", "BOOTSTRAP_PREFLIGHT_STARTED", "bootstrap_session", sessionID, "success", map[string]any{"node_id": reg.NodeID})
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":                 sessionID,
		"project_id":                 reg.ProjectID,
		"node_id":                    reg.NodeID,
		"agent_registration_token":   reg.Token,
		"agent_registration_expires": reg.ExpiresAt,
		"ssh": map[string]any{
			"auth_method": credential.AuthMethod,
			"username":    credential.Username,
			"private_key": string(credential.PrivateKey),
			"password":    string(credential.Password),
		},
	})
}

func (s *Server) handleBootstrapWorkerFinish(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req struct {
		ProjectID string `json:"project_id"`
		Status    string `json:"status"`
		Message   string `json:"message"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status != "succeeded" && req.Status != "failed" && req.Status != "cancelled" {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap finish requires succeeded, failed, or cancelled", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	session, err := s.Registry.UpdateBootstrapSession(req.ProjectID, sessionID, req.Status, req.Message)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if req.Status == "succeeded" || req.Status == "failed" || req.Status == "cancelled" {
		s.credentials.Delete(sessionID)
		s.registrations.DeleteSession(sessionID)
	}
	action := "BOOTSTRAP_SUCCEEDED"
	if req.Status != "succeeded" {
		action = "BOOTSTRAP_FAILED"
	}
	s.Registry.Audit(session.OrgID, session.ProjectID, "bootstrap-worker", action, "bootstrap_session", session.ID, req.Status, map[string]any{"message": req.Message})
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RegistrationToken    string         `json:"registration_token"`
		PublicKeyFingerprint string         `json:"public_key_fingerprint"`
		Version              string         `json:"version"`
		Capabilities         map[string]any `json:"capabilities"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.PublicKeyFingerprint == "" {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "AGENT_FINGERPRINT_REQUIRED", Message: "agent public key fingerprint is required", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	reg, ok := s.registrations.Exchange(req.RegistrationToken)
	if !ok {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "AGENT_REGISTRATION_INVALID", Message: "agent registration token is invalid or expired", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	agentToken := newSecret("agent")
	hash, err := auth.HashPAT(agentToken)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	agent, err := s.Registry.RegisterAgent(reg.ProjectID, reg.NodeID, req.PublicKeyFingerprint, hash, req.Version, "agent-register:"+reg.SessionID, req.Capabilities)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	s.Registry.Audit(agent.OrgID, agent.ProjectID, "agent", "AGENT_REGISTERED", "agent", agent.ID, "success", map[string]any{"node_id": agent.NodeID})
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent, "agent_token": agentToken})
}
