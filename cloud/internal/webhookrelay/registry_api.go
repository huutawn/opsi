package webhookrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func (s *Server) handleRegistryAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	if len(parts) == 3 && parts[0] == "orgs" && parts[2] == "projects" {
		principal, ok := s.authorizeOrg(w, r, parts[1])
		if !ok {
			return
		}
		s.handleOrgProjects(w, r, parts[1], principal)
		return
	}
	if len(parts) >= 2 && parts[0] == "projects" {
		principal, ok := s.authorizeProject(w, r, parts[1])
		if !ok {
			return
		}
		s.handleProjectAPI(w, r, parts, principal)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleGitHubInstallationsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "github_installation", projectID, "owner", "admin", "developer", "viewer", "support") {
		return
	}
	installations, err := s.Registry.ListGitHubInstallations(projectID)
	writeRegistryResult(w, r, map[string]any{"installations": installations}, err, http.StatusOK)
}

func (s *Server) handleGitHubRepositoriesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "github_repository", projectID, "owner", "admin", "developer", "viewer", "support") {
		return
	}
	repositories, err := s.Registry.ListGitHubRepositories(projectID)
	writeRegistryResult(w, r, map[string]any{"repositories": repositories}, err, http.StatusOK)
}

func (s *Server) handleGitHubRepositoryClaimAPI(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "github_repository", r.PathValue("repository_id"), "owner", "admin") {
		return
	}
	repositoryID, err := strconv.ParseInt(r.PathValue("repository_id"), 10, 64)
	if err != nil || repositoryID <= 0 {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "GITHUB_REPOSITORY_ID_INVALID", Message: "repository_id must be a positive integer", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	switch r.Method {
	case http.MethodPost:
		claim, err := s.Registry.ClaimGitHubRepository(projectID, repositoryID, principal.UserID)
		writeRegistryResult(w, r, claim, err, http.StatusOK)
	case http.MethodDelete:
		if err := s.Registry.ReleaseGitHubRepository(projectID, repositoryID, principal.UserID); err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"released": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGitHubBindingsAPI(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !s.requireRole(w, r, principal, projectID, "github_service_binding", projectID, "owner", "admin", "developer", "viewer", "support") {
			return
		}
		bindings, err := s.Registry.ListGitHubServiceBindings(projectID)
		writeRegistryResult(w, r, map[string]any{"bindings": bindings}, err, http.StatusOK)
	case http.MethodPost:
		if !s.requireRole(w, r, principal, projectID, "github_service_binding", projectID, "owner", "admin") {
			return
		}
		var draft registry.GitHubServiceBindingDraft
		if !decodeJSON(w, r, &draft) {
			return
		}
		draft.CreatedBy = principal.UserID
		binding, err := s.Registry.CreateGitHubServiceBinding(projectID, draft)
		writeRegistryResult(w, r, binding, err, http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGitHubBindingAPI(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok {
		return
	}
	bindingID := r.PathValue("binding_id")
	switch r.Method {
	case http.MethodGet:
		if !s.requireRole(w, r, principal, projectID, "github_service_binding", bindingID, "owner", "admin", "developer", "viewer", "support") {
			return
		}
		binding, err := s.Registry.GetGitHubServiceBinding(projectID, bindingID)
		writeRegistryResult(w, r, binding, err, http.StatusOK)
	case http.MethodPut:
		if !s.requireRole(w, r, principal, projectID, "github_service_binding", bindingID, "owner", "admin") {
			return
		}
		var source registry.GitHubSource
		if !decodeJSON(w, r, &source) {
			return
		}
		binding, err := s.Registry.UpdateGitHubServiceBinding(projectID, bindingID, principal.UserID, source)
		writeRegistryResult(w, r, binding, err, http.StatusOK)
	case http.MethodDelete:
		if !s.requireRole(w, r, principal, projectID, "github_service_binding", bindingID, "owner", "admin") {
			return
		}
		if err := s.Registry.RemoveGitHubServiceBinding(projectID, bindingID, principal.UserID); err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) authorizeGitHubProject(w http.ResponseWriter, r *http.Request, projectID string) (auth.VerifyResult, bool) {
	if s.Auth == nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusServiceUnavailable, Code: "AUTH_UNAVAILABLE", Message: "PAT authentication is not configured", RequestID: r.Header.Get("X-Request-ID")})
		return auth.VerifyResult{}, false
	}
	token := bearerToken(r)
	if token == "" {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "AUTH_REQUIRED", Message: "Authorization bearer token is required", RequestID: r.Header.Get("X-Request-ID")})
		return auth.VerifyResult{}, false
	}
	principal, err := s.Auth.VerifyPAT(r.Context(), auth.VerifyRequest{Token: token, ProjectID: projectID})
	if err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "AUTH_INVALID", Message: "Authorization bearer token is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return auth.VerifyResult{}, false
	}
	return principal, true
}

func (s *Server) handleOrgProjects(w http.ResponseWriter, r *http.Request, orgID string, principal auth.VerifyResult) {
	switch r.Method {
	case http.MethodGet:
		projects, err := s.Registry.ListProjects(orgID)
		writeRegistryResult(w, r, map[string]any{"projects": projects}, err, http.StatusOK)
	case http.MethodPost:
		if !requireWriteHeaders(w, r) {
			return
		}
		if s.Auth != nil && principal.UserID == "" {
			writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "PERMISSION_DENIED", Message: "authenticated principal user ID is required", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		if !s.requireRole(w, r, principal, "", "project", orgID, "owner", "admin") {
			return
		}
		var req struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		project, err := s.Registry.CreateProject(orgID, req.Name, req.Slug, principal.UserID, r.Header.Get("Idempotency-Key"))
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		s.auditOnce(orgID, project.ID, principal.UserID, "PROJECT_CREATED", "project", project.ID, "success", nil)
		writeJSON(w, http.StatusCreated, project)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProjectAPI(w http.ResponseWriter, r *http.Request, parts []string, principal auth.VerifyResult) {
	projectID := parts[1]
	if s.handleResourceAPI(w, r, projectID, parts, principal) {
		return
	}
	if s.handleServiceConfigurationAPI(w, r, projectID, parts, principal) {
		return
	}
	if s.handleServiceDependenciesAPI(w, r, projectID, parts, principal) {
		return
	}
	if s.handleExposureAPI(w, r, projectID, parts, principal) {
		return
	}
	if s.handleDeploymentAPI(w, r, projectID, parts, principal) {
		return
	}
	if s.handlePlacementAPI(w, r, projectID, parts, principal) {
		return
	}
	if s.handleBuildRecordRead(w, r, projectID, parts, principal) {
		return
	}
	if len(parts) == 3 && parts[2] == "readiness" && r.Method == http.MethodGet {
		value, err := s.Registry.ProjectReadiness(projectID)
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 3 && parts[2] == "nodes" {
		if r.Method == http.MethodGet {
			value, err := s.Registry.ListNodes(projectID)
			if value == nil {
				value = []registry.Node{}
			}
			writeRegistryResult(w, r, map[string]any{"nodes": value}, err, http.StatusOK)
			return
		}
		if r.Method == http.MethodPost {
			if !requireWriteHeaders(w, r) {
				return
			}
			if !s.requireRole(w, r, principal, projectID, "node", projectID, "owner", "admin") {
				return
			}
			var req struct {
				Name       string `json:"name"`
				Role       string `json:"role"`
				Status     string `json:"status"`
				PublicHost string `json:"public_host"`
				AgentID    string `json:"agent_id"`
			}
			if !decodeJSON(w, r, &req) {
				return
			}
			value, err := s.Registry.UpsertNode(projectID, req.Name, req.Role, req.Status, req.PublicHost, req.AgentID, r.Header.Get("Idempotency-Key"))
			if err == nil {
				s.Registry.Audit(value.OrgID, projectID, principal.UserID, "NODE_REGISTERED", "node", value.ID, "success", map[string]any{"status": value.Status})
			}
			writeRegistryResult(w, r, value, err, http.StatusCreated)
			return
		}
	}
	if len(parts) == 3 && parts[2] == "services" && r.Method == http.MethodGet {
		value, err := s.Registry.ListServices(projectID)
		writeRegistryResult(w, r, map[string]any{"services": value}, err, http.StatusOK)
		return
	}
	if len(parts) == 3 && parts[2] == "deployments" && r.Method == http.MethodGet {
		value, err := s.Registry.ListDeployments(projectID)
		writeRegistryResult(w, r, map[string]any{"deployments": value}, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[2] == "deployments" && parts[4] == "events" && r.Method == http.MethodGet {
		value, err := s.Registry.DeploymentEvents(projectID, parts[3])
		writeRegistryResult(w, r, map[string]any{"events": value}, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[2] == "deployments" && parts[4] == "rollback" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if !s.requireRole(w, r, principal, projectID, "deployment_job", parts[3], "owner", "admin", "developer") {
			return
		}
		var req struct {
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		value, err := s.Registry.RollbackDeployment(projectID, parts[3], principal.UserID, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
		if err == nil {
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, "DEPLOYMENT_ROLLBACK_STARTED", "deployment_job", value.ID, "success", map[string]any{"source_deployment_id": parts[3], "service_id": value.ServiceID})
		}
		writeRegistryResult(w, r, value, err, http.StatusAccepted)
		return
	}
	if len(parts) == 3 && parts[2] == "audit" && r.Method == http.MethodGet {
		value, err := s.Registry.ListAudit(projectID)
		writeRegistryResult(w, r, map[string]any{"events": value}, err, http.StatusOK)
		return
	}
	if len(parts) == 3 && parts[2] == "support" && r.Method == http.MethodGet {
		value, err := s.supportSummary(projectID)
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 4 && parts[2] == "nodes" && r.Method == http.MethodGet {
		value, err := s.Registry.NodeDiagnostics(projectID, parts[3])
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[2] == "nodes" && parts[4] == "offline" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if !validDeploymentIdempotencyKey(key) {
			writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "IDEMPOTENCY_KEY_INVALID", Message: "Idempotency-Key must be 1-128 printable characters without whitespace", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		if !s.requireRole(w, r, principal, projectID, "node", parts[3], "owner", "admin") {
			return
		}
		if principal.UserID == "" {
			writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "PERMISSION_DENIED", Message: "authenticated principal user ID is required", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		var req struct {
			ConfirmTargetReset bool `json:"confirm_target_reset"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if !req.ConfirmTargetReset {
			writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "TARGET_RESET_CONFIRMATION_REQUIRED", Message: "confirm_target_reset=true is required", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		value, _, err := s.Registry.MarkNodeOffline(projectID, parts[3], principal.UserID, key, r.Header.Get("X-Request-ID"))
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[2] == "nodes" && (parts[4] == "drain" || parts[4] == "remove") && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if s.Auth != nil && principal.UserID == "" {
			writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "PERMISSION_DENIED", Message: "authenticated principal user ID is required", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		if !s.requireRole(w, r, principal, projectID, "node", parts[3], "owner", "admin") {
			return
		}
		var req struct {
			ConfirmRemove bool `json:"confirm_remove"`
		}
		if r.ContentLength != 0 {
			if !decodeJSON(w, r, &req) {
				return
			}
		}
		job, err := s.Registry.RequestNodeLifecycle(projectID, parts[3], parts[4], principal.UserID, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"), req.ConfirmRemove, r.URL.Query().Get("force") == "true")
		if err == nil {
			s.auditOnce(job.OrgID, projectID, principal.UserID, "NODE_LIFECYCLE_REQUESTED", "node_lifecycle_job", job.ID, "success", map[string]any{"action": job.Action, "target_node_id": job.TargetNodeID, "status": job.Status})
		} else if nodes, listErr := s.Registry.ListNodes(projectID); listErr == nil {
			if node, ok := nodeByID(nodes, parts[3]); ok {
				code := "NODE_LIFECYCLE_REQUEST_FAILED"
				var apiErr registry.APIError
				if errors.As(err, &apiErr) {
					code = apiErr.Code
				}
				s.Registry.Audit(node.OrgID, projectID, principal.UserID, "NODE_LIFECYCLE_REQUEST_REJECTED", "node", node.ID, "failure", map[string]any{"action": parts[4], "error_code": code})
			}
		}
		writeRegistryResult(w, r, job, err, http.StatusAccepted)
		return
	}
	if len(parts) == 3 && parts[2] == "agents" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if !s.requireRole(w, r, principal, projectID, "agent", projectID, "owner", "admin") {
			return
		}
		var req struct {
			NodeID               string         `json:"node_id"`
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
		if !s.limits.Allow("agent:"+projectID+":"+req.NodeID, 5, time.Hour) {
			s.observer.Inc("rate_limited_total")
			writeRegistryError(w, registry.APIError{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: "agent registration rate limit exceeded", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		agentToken := newSecret("agent")
		hash, err := auth.HashPAT(agentToken)
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		value, err := s.Registry.RegisterAgent(projectID, req.NodeID, req.PublicKeyFingerprint, hash, req.Version, r.Header.Get("Idempotency-Key"), req.Capabilities, registry.AgentEndpoint{
			Address: req.AgentEndpoint, Port: req.AgentPort, TLSServerName: req.AgentTLSServerName, CertSHA256: req.AgentCertSHA256,
		})
		if err == nil {
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, "AGENT_REGISTERED", "agent", value.ID, "success", map[string]any{"node_id": value.NodeID})
		}
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		resp := map[string]any{"agent": value}
		if value.CredentialHash == hash {
			resp["agent_token"] = agentToken
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}
	if len(parts) == 5 && parts[2] == "agents" && (parts[4] == "rotate" || parts[4] == "revoke") && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if !s.requireRole(w, r, principal, projectID, "agent", parts[3], "owner", "admin") {
			return
		}
		var value registry.Agent
		var err error
		action := "AGENT_CREDENTIAL_ROTATED"
		agentToken := ""
		if parts[4] == "revoke" {
			value, err = s.Registry.RevokeAgent(projectID, parts[3])
			action = "AGENT_REVOKED"
		} else {
			agentToken = newSecret("agent")
			hash, hashErr := auth.HashPAT(agentToken)
			if hashErr != nil {
				writeRegistryFailure(w, r, hashErr)
				return
			}
			value, err = s.Registry.RotateAgent(projectID, parts[3], hash)
		}
		if err == nil {
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, action, "agent", value.ID, "success", nil)
		}
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		resp := map[string]any{"agent": value}
		if agentToken != "" {
			resp["agent_token"] = agentToken
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if len(parts) == 3 && parts[2] == "bootstrap-sessions" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if !s.requireRole(w, r, principal, projectID, "bootstrap_session", projectID, "owner", "admin") {
			return
		}
		if !s.limits.Allow("bootstrap:"+firstNonEmpty(principal.UserID, principal.OrgID, projectID), 10, time.Hour) {
			writeRegistryError(w, registry.APIError{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: "bootstrap session rate limit exceeded", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		var req struct {
			Role          string `json:"role"`
			PublicHost    string `json:"public_host"`
			SSHPort       int    `json:"ssh_port"`
			SSHUsername   string `json:"ssh_username"`
			AuthMethod    string `json:"auth_method"`
			SSHPrivateKey string `json:"ssh_private_key"`
			SSHPassword   string `json:"ssh_password"`
			K3SToken      string `json:"k3s_token"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		credential, err := bootstrapCredential(req.AuthMethod, req.SSHUsername, req.SSHPrivateKey, req.SSHPassword, req.K3SToken)
		if err != nil {
			writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_BOOTSTRAP_CREDENTIAL", Message: err.Error(), RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		defer clearBootstrapCredential(&credential)
		if credential.AuthMethod == "command" {
			if _, err := s.bootstrapBaseURL(r); err != nil || s.bootstrapRunnerPath == "" {
				writeRegistryError(w, registry.APIError{Status: http.StatusServiceUnavailable, Code: "BOOTSTRAP_COMMAND_UNAVAILABLE", Message: "bootstrap command runner is unavailable", RequestID: r.Header.Get("X-Request-ID")})
				return
			}
		}
		value, err := s.Registry.CreateBootstrapSession(projectID, req.Role, req.PublicHost, credential.Username, credential.AuthMethod, principal.UserID, r.Header.Get("Idempotency-Key"), req.SSHPort)
		if err == nil {
			s.observer.Inc("bootstrap_sessions_total")
			ttl := time.Until(value.ExpiresAt)
			if ttl <= 0 {
				ttl = 30 * time.Minute
			}
			if credential.AuthMethod == "command" {
				stored, ok := s.credentials.GetForBootstrapLease(value.ID)
				if ok && stored.AuthMethod == "command" && len(stored.Token) != 0 {
					credential.Token = append(credential.Token[:0], stored.Token...)
				} else {
					credential.Token = []byte(value.ID + "." + newSecret("btok"))
				}
				clearBootstrapCredential(&stored)
				value.BootstrapCommand, err = s.bootstrapCommand(r, string(credential.Token))
			}
			registrationToken := newSecret("areg")
			s.credentials.Put(value.ID, credential, ttl)
			s.registrations.Put(value.ID, value.OrgID, projectID, value.NodeID, registrationToken, ttl)
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, "BOOTSTRAP_SESSION_CREATED", "bootstrap_session", value.ID, "success", map[string]any{"role": value.Role, "auth_method": value.AuthMethod})
		}
		writeRegistryResult(w, r, value, err, http.StatusCreated)
		return
	}
	if len(parts) == 3 && parts[2] == "bootstrap-sessions" && r.Method == http.MethodGet {
		value, err := s.Registry.ListBootstrapSessions(projectID)
		writeRegistryResult(w, r, map[string]any{"sessions": value}, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[2] == "bootstrap-sessions" && parts[4] == "retry" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if principal.Role != "owner" && principal.Role != "admin" {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RBAC_DENIED", "bootstrap_session", parts[3], "denied", map[string]any{"role": principal.Role, "error_code": "BOOTSTRAP_RETRY_FORBIDDEN"})
			s.observer.Inc("rbac_denied_total")
			writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "BOOTSTRAP_RETRY_FORBIDDEN", Message: "only Owner or Admin can retry a bootstrap session", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		current, err := s.Registry.GetBootstrapSession(projectID, parts[3])
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		if current.Status != registry.BootstrapDeadLetter {
			result, retryErr := s.Registry.ManualRetryBootstrapSession(projectID, parts[3], r.Header.Get("Idempotency-Key"), s.clock())
			if retryErr == nil && !result.Applied {
				if current.AuthMethod == "command" {
					credential, ok := s.credentials.GetForBootstrapLease(result.Session.ID)
					if !ok || credential.AuthMethod != "command" || len(credential.Token) == 0 {
						clearBootstrapCredential(&credential)
						writeRegistryError(w, registry.APIError{Status: http.StatusConflict, Code: "BOOTSTRAP_RETRY_CREDENTIAL_UNAVAILABLE", Message: "bootstrap command token is unavailable", RequestID: r.Header.Get("X-Request-ID")})
						return
					}
					result.Session.BootstrapCommand, retryErr = s.bootstrapCommand(r, string(credential.Token))
					clearBootstrapCredential(&credential)
					if retryErr != nil {
						writeRegistryFailure(w, r, retryErr)
						return
					}
				}
				writeJSON(w, http.StatusAccepted, result.Session)
				return
			}
			if retryErr != nil {
				writeRegistryFailure(w, r, retryErr)
				return
			}
			writeRegistryError(w, registry.APIError{Status: http.StatusConflict, Code: "BOOTSTRAP_NOT_DEAD_LETTER", Message: "bootstrap session is not dead-lettered", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		if current.AuthMethod != "command" {
			credential, ok := s.credentials.GetForBootstrapLease(parts[3])
			clearBootstrapCredential(&credential)
			if !ok {
				writeRegistryError(w, registry.APIError{Status: http.StatusConflict, Code: "BOOTSTRAP_RETRY_CREDENTIAL_UNAVAILABLE", Message: "bootstrap credential is unavailable", RequestID: r.Header.Get("X-Request-ID")})
				return
			}
		}
		result, err := s.Registry.ManualRetryBootstrapSession(projectID, parts[3], r.Header.Get("Idempotency-Key"), s.clock())
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		if result.Applied {
			s.Registry.Audit(result.Session.OrgID, projectID, principal.UserID, "BOOTSTRAP_MANUAL_RETRY_REQUESTED", "bootstrap_session", result.Session.ID, "success", map[string]any{"previous_attempt_count": current.AttemptCount, "attempt_count": 0, "manual_retry_generation": r.Header.Get("Idempotency-Key")})
		}
		if current.AuthMethod == "command" {
			credential, ok := s.credentials.GetForBootstrapLease(result.Session.ID)
			if result.Applied || !ok || credential.AuthMethod != "command" || len(credential.Token) == 0 {
				clearBootstrapCredential(&credential)
				credential = BootstrapCredential{AuthMethod: "command", Username: "root", Token: []byte(result.Session.ID + "." + newSecret("btok"))}
				ttl := result.Session.ExpiresAt.Sub(s.clock())
				if ttl <= 0 {
					writeRegistryError(w, registry.APIError{Status: http.StatusConflict, Code: "BOOTSTRAP_SESSION_EXPIRED", Message: "expired bootstrap session cannot be retried", RequestID: r.Header.Get("X-Request-ID")})
					return
				}
				s.credentials.Put(result.Session.ID, credential, ttl)
			}
			result.Session.BootstrapCommand, err = s.bootstrapCommand(r, string(credential.Token))
			clearBootstrapCredential(&credential)
			if err != nil {
				writeRegistryFailure(w, r, err)
				return
			}
		}
		writeJSON(w, http.StatusAccepted, result.Session)
		return
	}
	if len(parts) == 4 && parts[2] == "bootstrap-sessions" && r.Method == http.MethodGet {
		value, err := s.Registry.GetBootstrapSession(projectID, parts[3])
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[2] == "bootstrap-sessions" && parts[4] == "events" && r.Method == http.MethodGet {
		value, err := s.Registry.BootstrapEvents(projectID, parts[3])
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 3 && parts[2] == "services" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if !s.requireRole(w, r, principal, projectID, "service", projectID, "owner", "admin", "developer") {
			return
		}
		var req registry.ServiceDraft
		if !decodeJSON(w, r, &req) {
			return
		}
		value, err := s.Registry.CreateService(projectID, req, r.Header.Get("Idempotency-Key"))
		if err == nil {
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, "SERVICE_CREATED", "service", value.ID, "success", map[string]any{"type": value.Type})
		}
		writeRegistryResult(w, r, value, err, http.StatusCreated)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleServiceConfigurationAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 5 || parts[2] != "services" || parts[4] != "configuration" {
		return false
	}
	serviceID := parts[3]
	if len(parts) == 5 && r.Method == http.MethodGet {
		value, err := s.Registry.GetServiceConfiguration(projectID, serviceID)
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return true
	}
	if len(parts) != 6 || r.Method != http.MethodPost {
		return false
	}
	switch parts[5] {
	case "preview", "validate", "diff":
		var draft registry.ServiceConfigurationDraft
		if !decodeJSON(w, r, &draft) {
			return true
		}
		var value any
		var err error
		switch parts[5] {
		case "preview":
			value, err = s.Registry.PreviewServiceConfiguration(projectID, serviceID, draft)
		case "validate":
			value, err = s.Registry.ValidateServiceConfiguration(projectID, serviceID, draft)
		case "diff":
			value, err = s.Registry.DiffServiceConfiguration(projectID, serviceID, draft)
		}
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return true
	case "apply":
		if !requireWriteHeaders(w, r) {
			return true
		}
		if !s.requireRole(w, r, principal, projectID, "service", serviceID, "owner", "admin", "developer") {
			return true
		}
		var request registry.ServiceConfigurationApplyRequest
		if !decodeJSON(w, r, &request) {
			return true
		}
		value, err := s.Registry.ApplyServiceConfiguration(projectID, serviceID, principal.UserID, r.Header.Get("Idempotency-Key"), request)
		if err == nil && !value.Reused {
			orgID := ""
			if services, listErr := s.Registry.ListServices(projectID); listErr == nil {
				for _, service := range services {
					if service.ID == serviceID {
						orgID = service.OrgID
						break
					}
				}
			}
			s.Registry.Audit(orgID, projectID, principal.UserID, "SERVICE_CONFIGURATION_APPLIED", "service", serviceID, "success", map[string]any{"revision": value.Configuration.Revision, "state_hash": value.Configuration.StateHash})
		}
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return true
	}
	return false
}

func (s *Server) handleServiceDependenciesAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 5 || parts[2] != "services" {
		return false
	}
	serviceID := parts[3]
	action := ""
	if parts[4] == "dependencies" && len(parts) == 6 {
		action = parts[5]
	} else if parts[4] == "configuration" && len(parts) == 7 && parts[5] == "dependencies" {
		action = parts[6]
	} else {
		return false
	}

	if r.Method != http.MethodPost {
		return false
	}

	switch action {
	case "review":
		s.reviewDependencies(w, r, projectID, serviceID, principal)
		return true
	case "apply":
		s.applyDependencies(w, r, projectID, serviceID, principal)
		return true
	}
	return false
}

func (s *Server) reviewDependencies(w http.ResponseWriter, r *http.Request, projectID, serviceID string, principal auth.VerifyResult) {
	services, err := s.Registry.ListServices(projectID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	var source registry.ServiceRecord
	for _, candidate := range services {
		if candidate.ID == serviceID {
			source = candidate
			break
		}
	}
	if source.ID == "" {
		writeRegistryFailure(w, r, registry.ErrNotFound)
		return
	}

	config, err := s.Registry.GetServiceConfiguration(projectID, serviceID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}

	bindings, err := s.Resources.ListBindings(r.Context(), projectID, source.EnvironmentID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}

	plan, err := registry.PlanDependencyRealization(r.Context(), config, bindings, func(ctx context.Context, targetID string) (resourcev1.Resource, error) {
		return s.Resources.Get(ctx, projectID, targetID)
	})
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) applyDependencies(w http.ResponseWriter, r *http.Request, projectID, serviceID string, principal auth.VerifyResult) {
	if !requireWriteHeaders(w, r) {
		return
	}
	if !s.requireRole(w, r, principal, projectID, "service", serviceID, "owner", "admin", "developer") {
		return
	}
	services, err := s.Registry.ListServices(projectID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	var source registry.ServiceRecord
	for _, candidate := range services {
		if candidate.ID == serviceID {
			source = candidate
			break
		}
	}
	if source.ID == "" {
		writeRegistryFailure(w, r, registry.ErrNotFound)
		return
	}

	config, err := s.Registry.GetServiceConfiguration(projectID, serviceID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}

	bindings, err := s.Resources.ListBindings(r.Context(), projectID, source.EnvironmentID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}

	plan, err := registry.PlanDependencyRealization(r.Context(), config, bindings, func(ctx context.Context, targetID string) (resourcev1.Resource, error) {
		return s.Resources.Get(ctx, projectID, targetID)
	})
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}

	idempotencyKey := r.Header.Get("Idempotency-Key")
	resBindings := append([]serviceconfigurationv1.ResourceBinding(nil), config.ResourceBindings...)
	reusedAll := true

	for i, item := range plan.Dependencies {
		if item.TargetKind != "managed_resource" || item.InjectionPhase != "runtime" {
			continue
		}
		if item.BindingAction == "create" {
			reusedAll = false
			bindingKey := fmt.Sprintf("%s:%s:%s", idempotencyKey, item.LogicalName, item.TargetIdentity)
			createReq := resourcev1.CreateBindingRequest{
				EnvironmentID: source.EnvironmentID,
				Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: serviceID},
				Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: item.TargetIdentity},
				Protocol:      resourcev1.Protocol(item.Protocol),
				LogicalName:   item.LogicalName,
			}
			created, reused, createErr := s.Resources.CreateBinding(r.Context(), projectID, bindingKey, createReq)
			if createErr != nil {
				writeRegistryFailure(w, r, createErr)
				return
			}
			if !reused {
				s.Registry.Audit(source.OrgID, projectID, principal.UserID, "RESOURCE_BINDING_CREATED", "resource_binding", created.ID, "success", map[string]any{"protocol": created.Protocol, "target_id": created.Target.ID, "logical_name": created.LogicalName})
			}
			plan.Dependencies[i].BindingID = created.ID
			plan.Dependencies[i].BindingAction = "create"
			if reused {
				plan.Dependencies[i].BindingAction = "reused"
			}
			plan.Dependencies[i].Status = "ready"
			resBindings = append(resBindings, serviceconfigurationv1.ResourceBinding{LogicalName: item.LogicalName, BindingID: created.ID})
		} else if item.BindingAction == "reused" {
			found := false
			for _, rb := range resBindings {
				if rb.LogicalName == item.LogicalName && rb.BindingID == item.BindingID {
					found = true
					break
				}
			}
			if !found {
				resBindings = append(resBindings, serviceconfigurationv1.ResourceBinding{LogicalName: item.LogicalName, BindingID: item.BindingID})
			}
		}
	}

	config.ResourceBindings = resBindings
	applyReq := registry.ServiceConfigurationApplyRequest{
		Draft:             config.ServiceConfigurationDraft,
		ExpectedRevision:  config.Revision,
		ExpectedStateHash: config.StateHash,
	}
	_, applyErr := s.Registry.ApplyServiceConfiguration(projectID, serviceID, principal.UserID, idempotencyKey+":cfg", applyReq)
	if applyErr != nil {
		// Even if config apply is skipped/stale if already matched, continue
	}

	s.Registry.Audit(source.OrgID, projectID, principal.UserID, "DEPENDENCY_REALIZATION_APPLIED", "service", serviceID, "success", map[string]any{
		"service_id": serviceID,
		"reused":     reusedAll,
	})

	writeJSON(w, http.StatusOK, registry.DependencyApplyResult{
		Realized: plan.Dependencies,
		Reused:   reusedAll,
	})
}

func (s *Server) authorizeOrg(w http.ResponseWriter, r *http.Request, orgID string) (auth.VerifyResult, bool) {
	if s.Auth == nil {
		return auth.VerifyResult{OrgID: orgID, Role: "owner"}, true
	}
	token := bearerToken(r)
	if token == "" {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "AUTH_REQUIRED", Message: "Authorization bearer token is required.", RequestID: r.Header.Get("X-Request-ID")})
		return auth.VerifyResult{}, false
	}
	result, err := s.Auth.VerifyOrgPAT(r.Context(), auth.VerifyRequest{Token: token, OrgID: orgID})
	if err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "PERMISSION_DENIED", Message: err.Error(), RequestID: r.Header.Get("X-Request-ID")})
		return auth.VerifyResult{}, false
	}
	return result, true
}

func (s *Server) authorizeProject(w http.ResponseWriter, r *http.Request, projectID string) (auth.VerifyResult, bool) {
	if s.Auth == nil {
		return auth.VerifyResult{ProjectID: projectID, Role: "owner"}, true
	}
	token := bearerToken(r)
	if token == "" {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "AUTH_REQUIRED", Message: "Authorization bearer token is required.", RequestID: r.Header.Get("X-Request-ID")})
		return auth.VerifyResult{}, false
	}
	result, err := s.Auth.VerifyPAT(r.Context(), auth.VerifyRequest{Token: token, ProjectID: projectID})
	if err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "PERMISSION_DENIED", Message: err.Error(), RequestID: r.Header.Get("X-Request-ID")})
		return auth.VerifyResult{}, false
	}
	return result, true
}

func requireWriteHeaders(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Idempotency-Key") == "" {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key header is required.", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	if r.Header.Get("X-Request-ID") == "" {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "REQUEST_ID_REQUIRED", Message: "X-Request-ID header is required."})
		return false
	}
	return true
}

func nodeByID(nodes []registry.Node, id string) (registry.Node, bool) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true
		}
	}
	return registry.Node{}, false
}

func healthyServerCount(nodes []registry.Node) int {
	count := 0
	for _, node := range nodes {
		if node.Role == "server" && node.Status == registry.NodeHealthy {
			count++
		}
	}
	return count
}

func (s *Server) auditOnce(orgID, projectID, actorUserID, action, resourceType, resourceID, result string, metadata map[string]any) {
	if events, err := s.Registry.ListAudit(projectID); err == nil {
		for _, event := range events {
			if event.Action == action && event.ResourceType == resourceType && event.ResourceID == resourceID {
				return
			}
		}
	}
	s.Registry.Audit(orgID, projectID, actorUserID, action, resourceType, resourceID, result, metadata)
}

func (s *Server) requireRole(w http.ResponseWriter, r *http.Request, principal auth.VerifyResult, projectID, resourceType, resourceID string, allowed ...string) bool {
	for _, role := range allowed {
		if principal.Role == role {
			return true
		}
	}
	s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RBAC_DENIED", resourceType, resourceID, "denied", map[string]any{"role": principal.Role})
	s.observer.Inc("rbac_denied_total")
	writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "PERMISSION_DENIED", Message: "role cannot perform this action", RequestID: r.Header.Get("X-Request-ID")})
	return false
}

func bootstrapCredential(method, username, privateKey, password, k3sToken string) (BootstrapCredential, error) {
	if k3sToken != "" {
		return BootstrapCredential{}, errors.New("k3s token is control-plane only")
	}
	if method == "command" {
		if username != "" || privateKey != "" || password != "" {
			return BootstrapCredential{}, errors.New("command auth does not accept SSH credentials")
		}
		return BootstrapCredential{AuthMethod: method, Username: "root"}, nil
	}
	if username == "" {
		return BootstrapCredential{}, errors.New("ssh_username is required")
	}
	if method == "" {
		switch {
		case privateKey != "":
			method = "private_key"
		case password != "":
			method = "password"
		}
	}
	switch method {
	case "private_key":
		if privateKey == "" || password != "" {
			return BootstrapCredential{}, errors.New("private_key auth requires ssh_private_key only")
		}
		return BootstrapCredential{AuthMethod: method, Username: username, PrivateKey: []byte(privateKey)}, nil
	case "password":
		if password == "" || privateKey != "" {
			return BootstrapCredential{}, errors.New("password auth requires ssh_password only")
		}
		return BootstrapCredential{AuthMethod: method, Username: username, Password: []byte(password)}, nil
	default:
		return BootstrapCredential{}, errors.New("auth_method must be command, private_key, or password")
	}
}

func (s *Server) bootstrapCommand(r *http.Request, token string) (string, error) {
	baseURL, err := s.bootstrapBaseURL(r)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("curl -fsSL '%s/v1/bootstrap/install' | OPSI_BOOTSTRAP_TOKEN='%s' sh", baseURL, token), nil
}

func clearBootstrapCredential(credential *BootstrapCredential) {
	zeroBootstrapCredential(credential)
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dst); err != nil {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "INVALID_JSON", Message: "Request body is not valid JSON.", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	return true
}

func writeRegistryResult[T any](w http.ResponseWriter, r *http.Request, value T, err error, status int) {
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	writeJSON(w, status, value)
}

func writeRegistryFailure(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr registry.APIError
	if errors.As(err, &apiErr) {
		writeRegistryError(w, apiErr)
		return
	}
	if errors.Is(err, registry.ErrNotFound) {
		writeRegistryError(w, registry.APIError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Resource was not found.", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if errors.Is(err, registry.ErrGitHubEventConflict) {
		writeRegistryError(w, registry.APIError{Status: http.StatusConflict, Code: "GITHUB_EVENT_CONFLICT", Message: "GitHub numeric identity conflicts with stored inventory", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeRegistryError(w, registry.APIError{Status: http.StatusInternalServerError, Code: "INTERNAL", Message: "Internal server error.", RequestID: r.Header.Get("X-Request-ID")})
}

func writeRegistryError(w http.ResponseWriter, err registry.APIError) {
	status := err.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, err)
}
