package webhookrelay

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type bootstrapWorkerStateRequest struct {
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

func (s *Server) handleBootstrapWorker(w http.ResponseWriter, r *http.Request) {
	if s.Config.BootstrapWorkerToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Bootstrap-Worker-Token")), []byte(s.Config.BootstrapWorkerToken)) != 1 {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "BOOTSTRAP_WORKER_AUTH_REQUIRED", Message: "bootstrap worker token is required", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method == http.MethodGet && len(parts) == 5 && parts[0] == "internal" && parts[1] == "bootstrap" && parts[2] == "sessions" && parts[4] == "status" {
		session, err := s.Registry.GetBootstrapSession(r.URL.Query().Get("project_id"), parts[3])
		writeRegistryResult(w, r, session, err, http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(parts) != 5 || parts[0] != "internal" || parts[1] != "bootstrap" || parts[2] != "sessions" || parts[4] != "take" {
		if len(parts) == 5 && parts[0] == "internal" && parts[1] == "bootstrap" && parts[2] == "sessions" {
			if parts[4] == "progress" {
				s.handleBootstrapWorkerProgress(w, r, parts[3])
				return
			}
			if parts[4] == "finish" {
				s.handleBootstrapWorkerFinish(w, r, parts[3])
				return
			}
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
	session, err := s.Registry.UpdateBootstrapSession(reg.ProjectID, sessionID, "validating", "bootstrap worker took credential")
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	s.Registry.Audit(reg.OrgID, reg.ProjectID, "bootstrap-worker", "BOOTSTRAP_STATE_VALIDATING", "bootstrap_session", sessionID, "success", map[string]any{"node_id": reg.NodeID})
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":                 sessionID,
		"project_id":                 reg.ProjectID,
		"node_id":                    reg.NodeID,
		"public_host":                session.PublicHost,
		"ssh_port":                   session.SSHPort,
		"role":                       session.Role,
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

func (s *Server) handleBootstrapWorkerProgress(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req bootstrapWorkerStateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyBootstrapWorkerState(w, r, sessionID, req)
}

func (s *Server) applyBootstrapWorkerState(w http.ResponseWriter, r *http.Request, sessionID string, req bootstrapWorkerStateRequest) {
	session, err := s.Registry.UpdateBootstrapSession(req.ProjectID, sessionID, req.Status, req.Message)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if !bootstrapStatusActive(req.Status) {
		s.credentials.Delete(sessionID)
		s.registrations.DeleteSession(sessionID)
	}
	action := "BOOTSTRAP_STATE_" + strings.ToUpper(req.Status)
	if req.Status == "succeeded" {
		action = "BOOTSTRAP_SUCCEEDED"
	}
	if req.Status == "completed" {
		action = "BOOTSTRAP_COMPLETED"
	}
	if req.Status == "failed" {
		action = "BOOTSTRAP_FAILED"
	}
	s.Registry.Audit(session.OrgID, session.ProjectID, "bootstrap-worker", action, "bootstrap_session", session.ID, req.Status, map[string]any{"message": registry.RedactString(req.Message)})
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handleBootstrapWorkerFinish(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req bootstrapWorkerStateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status != "completed" && req.Status != "succeeded" && req.Status != "failed" && req.Status != "cancelled" {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap finish requires completed, failed, or cancelled", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	s.applyBootstrapWorkerState(w, r, sessionID, req)
}

func bootstrapStatusActive(status string) bool {
	switch status {
	case "created", "pending", "preflight", "validating", "connecting", "installing", "installing_k3s", "installing_agent", "registering_agent", "waiting_agent", "verifying_agent", "verifying":
		return true
	default:
		return false
	}
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
	if _, err := s.Registry.UpdateBootstrapSession(reg.ProjectID, reg.SessionID, "waiting_agent", "agent registered; waiting for healthy heartbeat"); err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	s.Registry.Audit(agent.OrgID, agent.ProjectID, "agent", "AGENT_REGISTERED", "agent", agent.ID, "success", map[string]any{"node_id": agent.NodeID})
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent, "agent_token": agentToken})
}
