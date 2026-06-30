package webhookrelay

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
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

func (s *Server) handleOrgProjects(w http.ResponseWriter, r *http.Request, orgID string, principal auth.VerifyResult) {
	switch r.Method {
	case http.MethodGet:
		projects, err := s.Registry.ListProjects(orgID)
		writeRegistryResult(w, r, map[string]any{"projects": projects}, err, http.StatusOK)
	case http.MethodPost:
		if !requireWriteHeaders(w, r) {
			return
		}
		if !s.requireRole(w, r, principal, "", "project", orgID, "owner", "admin") {
			return
		}
		var req struct {
			Name      string `json:"name"`
			Slug      string `json:"slug"`
			CreatedBy string `json:"created_by"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.CreatedBy == "" {
			req.CreatedBy = principal.UserID
		}
		project, err := s.Registry.CreateProject(orgID, req.Name, req.Slug, req.CreatedBy, r.Header.Get("Idempotency-Key"))
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		s.Registry.Audit(orgID, project.ID, principal.UserID, "PROJECT_CREATED", "project", project.ID, "success", nil)
		writeJSON(w, http.StatusCreated, project)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProjectAPI(w http.ResponseWriter, r *http.Request, parts []string, principal auth.VerifyResult) {
	projectID := parts[1]
	if len(parts) == 3 && parts[2] == "readiness" && r.Method == http.MethodGet {
		value, err := s.Registry.ProjectReadiness(projectID)
		writeRegistryResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 3 && parts[2] == "nodes" {
		if r.Method == http.MethodGet {
			value, err := s.Registry.ListNodes(projectID)
			writeRegistryResult(w, r, value, err, http.StatusOK)
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
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if !s.limits.Allow("agent:"+projectID+":"+req.NodeID, 5, time.Hour) {
			writeRegistryError(w, registry.APIError{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: "agent registration rate limit exceeded", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		agentToken := newSecret("agent")
		hash, err := auth.HashPAT(agentToken)
		if err != nil {
			writeRegistryFailure(w, r, err)
			return
		}
		value, err := s.Registry.RegisterAgent(projectID, req.NodeID, req.PublicKeyFingerprint, hash, req.Version, r.Header.Get("Idempotency-Key"), req.Capabilities)
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
		value, err := s.Registry.CreateBootstrapSession(projectID, req.Role, req.PublicHost, req.SSHUsername, credential.AuthMethod, principal.UserID, r.Header.Get("Idempotency-Key"), req.SSHPort)
		if err == nil {
			ttl := time.Until(value.ExpiresAt)
			if ttl <= 0 {
				ttl = 30 * time.Minute
			}
			registrationToken := newSecret("areg")
			s.credentials.Put(value.ID, credential, ttl)
			s.registrations.Put(value.ID, value.OrgID, projectID, value.NodeID, registrationToken, ttl)
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, "BOOTSTRAP_SESSION_CREATED", "bootstrap_session", value.ID, "success", map[string]any{"role": value.Role})
		}
		writeRegistryResult(w, r, value, err, http.StatusCreated)
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
		var req struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			SourceType string `json:"source_type"`
			RepoURL    string `json:"repo_url"`
			Image      string `json:"image"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		value, err := s.Registry.CreateService(projectID, req.Name, req.Type, req.SourceType, req.RepoURL, req.Image, r.Header.Get("Idempotency-Key"))
		if err == nil {
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, "SERVICE_CREATED", "service", value.ID, "success", map[string]any{"type": value.Type})
		}
		writeRegistryResult(w, r, value, err, http.StatusCreated)
		return
	}
	if len(parts) == 5 && parts[2] == "services" && parts[4] == "deployments" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) {
			return
		}
		if !s.requireRole(w, r, principal, projectID, "deployment_job", parts[3], "owner", "admin", "developer") {
			return
		}
		if !s.limits.Allow("deploy:"+projectID, 60, time.Minute) {
			writeRegistryError(w, registry.APIError{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: "deployment rate limit exceeded", RequestID: r.Header.Get("X-Request-ID")})
			return
		}
		var req struct {
			RequestedBy string `json:"requested_by"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.RequestedBy == "" {
			req.RequestedBy = principal.UserID
		}
		value, err := s.Registry.StartDeployment(projectID, parts[3], req.RequestedBy, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
		if err == nil {
			s.Registry.Audit(value.OrgID, projectID, principal.UserID, "DEPLOYMENT_STARTED", "deployment_job", value.ID, "success", map[string]any{"service_id": value.ServiceID})
		}
		writeRegistryResult(w, r, value, err, http.StatusAccepted)
		return
	}
	http.NotFound(w, r)
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

func (s *Server) requireRole(w http.ResponseWriter, r *http.Request, principal auth.VerifyResult, projectID, resourceType, resourceID string, allowed ...string) bool {
	for _, role := range allowed {
		if principal.Role == role {
			return true
		}
	}
	s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "RBAC_DENIED", resourceType, resourceID, "denied", map[string]any{"role": principal.Role})
	writeRegistryError(w, registry.APIError{Status: http.StatusForbidden, Code: "PERMISSION_DENIED", Message: "role cannot perform this action", RequestID: r.Header.Get("X-Request-ID")})
	return false
}

func bootstrapCredential(method, username, privateKey, password, k3sToken string) (BootstrapCredential, error) {
	if k3sToken != "" {
		return BootstrapCredential{}, errors.New("k3s token is control-plane only")
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
		return BootstrapCredential{}, errors.New("auth_method must be private_key or password")
	}
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
	writeRegistryError(w, registry.APIError{Status: http.StatusInternalServerError, Code: "INTERNAL", Message: "Internal server error.", RequestID: r.Header.Get("X-Request-ID")})
}

func writeRegistryError(w http.ResponseWriter, err registry.APIError) {
	status := err.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, err)
}
