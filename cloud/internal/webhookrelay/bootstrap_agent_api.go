package webhookrelay

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type bootstrapWorkerStateRequest struct {
	ProjectID   string `json:"project_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	FailureCode string `json:"failure_code"`
	Retryable   bool   `json:"retryable"`
}

const bootstrapLeaseDuration = 90 * time.Second

type bootstrapWorkerLeaseRequest struct {
	WorkerID string `json:"worker_id"`
}

func (s *Server) handleBootstrapWorker(w http.ResponseWriter, r *http.Request) {
	if s.Config.BootstrapWorkerToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Bootstrap-Worker-Token")), []byte(s.Config.BootstrapWorkerToken)) != 1 {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "BOOTSTRAP_WORKER_AUTH_REQUIRED", Message: "bootstrap worker token is required", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "internal" && parts[1] == "bootstrap" && parts[2] == "sessions" && parts[3] == "lease" {
		s.handleBootstrapWorkerLease(w, r)
		return
	}
	if r.Method == http.MethodGet && len(parts) == 5 && parts[0] == "internal" && parts[1] == "bootstrap" && parts[2] == "sessions" && parts[4] == "status" {
		s.handleBootstrapWorkerStatus(w, r, parts[3])
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(parts) == 5 && parts[0] == "internal" && parts[1] == "bootstrap" && parts[2] == "sessions" {
		if parts[4] == "lease-heartbeat" {
			s.handleBootstrapLeaseHeartbeat(w, r, parts[3])
			return
		}
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
}

func (s *Server) handleBootstrapWorkerLease(w http.ResponseWriter, r *http.Request) {
	var req bootstrapWorkerLeaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	if err := registry.ValidateBootstrapWorkerID(req.WorkerID); err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	now := s.clock()
	summary, err := s.Registry.RecoverExpiredBootstrapLeases(now)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	s.cleanupRecoveredBootstrapSecrets(summary)
	lease, found, err := s.Registry.LeaseNextBootstrapSession(req.WorkerID, now, bootstrapLeaseDuration)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	session := lease.Session
	credential, ok := s.credentials.GetForBootstrapLease(session.ID)
	if !ok {
		s.failLeasedBootstrap(w, r, lease, "BOOTSTRAP_CREDENTIAL_UNAVAILABLE", "bootstrap credential is unavailable", false)
		return
	}
	defer clearBootstrapCredential(&credential)
	registrationToken := newSecret("areg")
	ttl := session.ExpiresAt.Sub(now)
	if ttl <= 0 {
		ttl = time.Minute
	}
	s.registrations.Put(session.ID, session.OrgID, session.ProjectID, session.NodeID, registrationToken, ttl)
	reg, ok := s.registrations.GetForBootstrapLease(session.ID)
	if !ok {
		s.failLeasedBootstrap(w, r, lease, "AGENT_REGISTRATION_TOKEN_UNAVAILABLE", "agent registration token is unavailable", true)
		return
	}
	s.Registry.Audit(session.OrgID, session.ProjectID, "", "BOOTSTRAP_LEASE_ACQUIRED", "bootstrap_session", session.ID, "success", map[string]any{"worker_id": req.WorkerID, "node_id": session.NodeID, "lease_expires_at": lease.LeaseExpiresAt})
	writeJSON(w, http.StatusOK, map[string]any{
		"bundle": map[string]any{
			"session_id": session.ID, "project_id": session.ProjectID, "node_id": session.NodeID,
			"public_host": session.PublicHost, "ssh_port": session.SSHPort, "role": session.Role,
			"agent_registration_token": reg.Token, "agent_registration_expires": reg.ExpiresAt,
			"ssh": map[string]any{"auth_method": credential.AuthMethod, "username": credential.Username, "private_key": string(credential.PrivateKey), "password": string(credential.Password)},
		},
		"lease_token": lease.LeaseToken, "lease_expires_at": lease.LeaseExpiresAt,
	})
}

func (s *Server) failLeasedBootstrap(w http.ResponseWriter, r *http.Request, lease registry.BootstrapSessionLease, code, message string, retryable bool) {
	session := lease.Session
	updated, _ := s.Registry.FinishBootstrapSessionForLease(session.ProjectID, session.ID, session.LeaseOwner, lease.LeaseToken, registry.BootstrapFinishResult{Status: "failed", FailureCode: code, MessageRedacted: message, Retryable: retryable}, s.clock())
	if registryBootstrapTerminal(updated.Status) {
		s.credentials.Delete(session.ID)
		s.registrations.DeleteSession(session.ID)
	}
	if updated.ID != "" {
		action := "BOOTSTRAP_RETRY_SCHEDULED"
		if updated.Status == registry.BootstrapDeadLetter {
			action = "BOOTSTRAP_DEAD_LETTERED"
		}
		s.Registry.Audit(session.OrgID, session.ProjectID, "", action, "bootstrap_session", session.ID, updated.Status, map[string]any{"worker_id": session.LeaseOwner, "node_id": session.NodeID, "failure_code": code, "attempt_count": updated.AttemptCount, "max_attempts": updated.MaxAttempts, "next_attempt_at": updated.NextAttemptAt})
	}
	writeRegistryError(w, registry.APIError{Status: http.StatusServiceUnavailable, Code: code, Message: message, RequestID: r.Header.Get("X-Request-ID")})
}

func (s *Server) handleBootstrapLeaseHeartbeat(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req struct {
		ProjectID string `json:"project_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	workerID, leaseToken := bootstrapLeaseHeaders(r)
	session, err := s.Registry.RenewBootstrapLease(req.ProjectID, sessionID, workerID, leaseToken, s.clock(), bootstrapLeaseDuration)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": session.ID, "lease_expires_at": session.LeaseExpiresAt})
}

func (s *Server) handleBootstrapWorkerStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	workerID, leaseToken := bootstrapLeaseHeaders(r)
	session, err := s.Registry.GetBootstrapSessionForLease(r.URL.Query().Get("project_id"), sessionID, workerID, leaseToken, s.clock())
	writeRegistryResult(w, r, session, err, http.StatusOK)
}

func (s *Server) handleBootstrapWorkerProgress(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req bootstrapWorkerStateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !bootstrapStatusActive(req.Status) {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap progress requires an active status", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	s.applyBootstrapWorkerState(w, r, sessionID, req)
}

func (s *Server) applyBootstrapWorkerState(w http.ResponseWriter, r *http.Request, sessionID string, req bootstrapWorkerStateRequest) {
	workerID, leaseToken := bootstrapLeaseHeaders(r)
	session, err := s.Registry.UpdateBootstrapSessionForLease(req.ProjectID, sessionID, workerID, leaseToken, req.Status, req.Message, s.clock())
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
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
	s.Registry.Audit(session.OrgID, session.ProjectID, "", action, "bootstrap_session", session.ID, req.Status, map[string]any{"worker_id": workerID, "node_id": session.NodeID, "status": req.Status})
	writeJSON(w, http.StatusOK, session)
}

func bootstrapLeaseHeaders(r *http.Request) (string, string) {
	return strings.TrimSpace(r.Header.Get("X-Bootstrap-Worker-ID")), r.Header.Get("X-Bootstrap-Lease-Token")
}

func (s *Server) handleBootstrapWorkerFinish(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req bootstrapWorkerStateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status != "completed" && req.Status != "succeeded" && req.Status != "failed" && req.Status != "cancelled" {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap finish requires completed, succeeded, failed, or cancelled", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	workerID, leaseToken := bootstrapLeaseHeaders(r)
	session, err := s.Registry.FinishBootstrapSessionForLease(req.ProjectID, sessionID, workerID, leaseToken, registry.BootstrapFinishResult{Status: req.Status, FailureCode: req.FailureCode, MessageRedacted: req.Message, Retryable: req.Retryable}, s.clock())
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if registryBootstrapTerminal(session.Status) {
		s.credentials.Delete(sessionID)
		s.registrations.DeleteSession(sessionID)
	}
	action := "BOOTSTRAP_COMPLETED"
	if session.Status == registry.BootstrapRetryWait {
		action = "BOOTSTRAP_RETRY_SCHEDULED"
	} else if session.Status == registry.BootstrapDeadLetter {
		action = "BOOTSTRAP_DEAD_LETTERED"
	} else if session.Status == "cancelled" {
		action = "BOOTSTRAP_CANCELLED"
	}
	s.Registry.Audit(session.OrgID, session.ProjectID, "", action, "bootstrap_session", session.ID, session.Status, map[string]any{"worker_id": workerID, "attempt_count": session.AttemptCount, "max_attempts": session.MaxAttempts, "failure_code": session.LastFailureCode, "next_attempt_at": session.NextAttemptAt})
	writeJSON(w, http.StatusOK, session)
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
		AgentEndpoint        string         `json:"agent_endpoint"`
		AgentPort            int            `json:"agent_port"`
		AgentTLSServerName   string         `json:"agent_tls_server_name"`
		AgentCertSHA256      string         `json:"agent_cert_sha256"`
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
	bootstrapSession, err := s.Registry.GetBootstrapSession(reg.ProjectID, reg.SessionID)
	if err != nil || !bootstrapStatusActive(bootstrapSession.Status) || bootstrapSession.LeaseExpiresAt == nil || !bootstrapSession.LeaseExpiresAt.After(s.clock()) {
		writeRegistryError(w, registry.APIError{Status: http.StatusGone, Code: "AGENT_REGISTRATION_LEASE_EXPIRED", Message: "bootstrap lease is no longer active", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	agentToken := newSecret("agent")
	hash, err := auth.HashPAT(agentToken)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	agent, err := s.Registry.RegisterAgent(reg.ProjectID, reg.NodeID, req.PublicKeyFingerprint, hash, req.Version, "agent-register:"+reg.SessionID, req.Capabilities, registry.AgentEndpoint{
		Address: req.AgentEndpoint, Port: req.AgentPort, TLSServerName: req.AgentTLSServerName, CertSHA256: req.AgentCertSHA256,
	})
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if agent.CredentialHash != hash {
		writeRegistryError(w, registry.APIError{Status: http.StatusConflict, Code: "AGENT_ALREADY_REGISTERED", Message: "an Agent is already registered for this bootstrap session", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if _, err := s.Registry.UpdateBootstrapSession(reg.ProjectID, reg.SessionID, "waiting_agent", "agent registered; waiting for healthy heartbeat"); err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	s.Registry.Audit(agent.OrgID, agent.ProjectID, "agent", "AGENT_REGISTERED", "agent", agent.ID, "success", map[string]any{"node_id": agent.NodeID})
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent, "agent_token": agentToken})
}

func (s *Server) cleanupRecoveredBootstrapSecrets(summary registry.BootstrapRecoverySummary) {
	for _, sessions := range [][]registry.BootstrapSession{summary.DeadLettered, summary.Expired} {
		for _, session := range sessions {
			s.credentials.Delete(session.ID)
			s.registrations.DeleteSession(session.ID)
		}
	}
}

func registryBootstrapTerminal(status string) bool {
	switch status {
	case "completed", "succeeded", "cancelled", "expired", registry.BootstrapDeadLetter:
		return true
	default:
		return false
	}
}
