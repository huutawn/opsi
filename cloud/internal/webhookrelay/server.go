package webhookrelay

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/otp"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type Server struct {
	Queue         RelayQueue
	Config        Config
	OTP           *otp.Service
	Auth          *auth.Service
	Registry      registry.API
	credentials   CredentialVault
	registrations RegistrationVault
	limits        RateLimiter
	observer      *Observer
	alerts        *AlertManager
	authMu        sync.Mutex
	oauthStates   map[string]oauthState
	authGrants    map[string]authGrant
}

func NewServer(cfg Config) *Server {
	service := otp.NewService()
	service.DevEcho = cfg.OTP.DevEcho
	return &Server{Queue: NewQueue(), Config: cfg, OTP: service, Registry: registry.NewService(), credentials: NewCredentialStore(), registrations: NewRegistrationTokenStore(), limits: newRateLimiter(), observer: NewObserver(), alerts: NewAlertManager(cfg.Alerts), oauthStates: map[string]oauthState{}, authGrants: map[string]authGrant{}}
}

func (s *Server) SetSecurityStores(credentials CredentialVault, registrations RegistrationVault, limits RateLimiter) {
	if credentials != nil {
		s.credentials = credentials
	}
	if registrations != nil {
		s.registrations = registrations
	}
	if limits != nil {
		s.limits = limits
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/webhooks/github", s.handleGitHubWebhook)
	mux.HandleFunc("/v1/auth/pat/verify", s.handlePATVerify)
	mux.HandleFunc("/v1/auth/browser/start", s.handleBrowserAuthStart)
	mux.HandleFunc("/v1/auth/browser/callback", s.handleBrowserAuthCallback)
	mux.HandleFunc("/v1/auth/browser/redeem", s.handleBrowserAuthRedeem)
	mux.HandleFunc("/v1/auth/pat/rotate", s.handlePATRotate)
	mux.HandleFunc("/v1/auth/pat/revoke", s.handlePATRevoke)
	mux.HandleFunc("/v1/otp/request", s.handleOTPRequest)
	mux.HandleFunc("/v1/otp/verify", s.handleOTPVerify)
	mux.HandleFunc("/v1/agents/register", s.handleAgentRegister)
	mux.HandleFunc("/v1/agents/", s.handleAgentWebhookNext)
	mux.HandleFunc("/internal/bootstrap/sessions/", s.handleBootstrapWorker)
	mux.HandleFunc("/api/internal/alerts", s.handleInternalAlerts)
	mux.HandleFunc("/api/", s.handleRegistryAPI)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !s.Config.EnableDebugUI {
			http.NotFound(w, r)
			return
		}
		s.handleUI(w, r)
	})
	return s.observer.Wrap(mux)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Server) handlePATVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.Auth == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service is not configured")
		return
	}
	var req struct {
		Token     string `json:"token"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid pat verify request")
		return
	}
	result, err := s.Auth.VerifyPAT(r.Context(), auth.VerifyRequest{Token: req.Token, ProjectID: req.ProjectID})
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type oauthState struct {
	LocalCallback string
	LocalState    string
	ProjectID     string
	ExpiresAt     time.Time
}

type authGrant struct {
	Token     string
	Session   auth.VerifyResult
	ExpiresAt time.Time
}

func (s *Server) handleBrowserAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.Auth == nil || !s.authConfigured() {
		s.auditAuth("", "", "", "login_started", "failure", map[string]any{"reason": "auth_not_configured"})
		writeError(w, http.StatusServiceUnavailable, "auth flow is not configured")
		return
	}
	var req struct {
		LocalCallback string `json:"local_callback"`
		LocalState    string `json:"local_state"`
		ProjectID     string `json:"project_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid auth start request")
		return
	}
	if !localCallbackAllowed(req.LocalCallback) || req.LocalState == "" {
		writeError(w, http.StatusBadRequest, "invalid local callback")
		return
	}
	state := randomToken("oauth")
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	s.authMu.Lock()
	s.oauthStates[state] = oauthState{LocalCallback: req.LocalCallback, LocalState: req.LocalState, ProjectID: req.ProjectID, ExpiresAt: expiresAt}
	s.authMu.Unlock()

	u, err := url.Parse(s.Config.Auth.AuthURL)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "auth provider URL is invalid")
		return
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", s.Config.Auth.ClientID)
	q.Set("redirect_uri", s.Config.Auth.RedirectURL)
	q.Set("state", state)
	if len(s.Config.Auth.Scopes) > 0 {
		q.Set("scope", strings.Join(s.Config.Auth.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	s.auditAuth("", "", req.ProjectID, "login_started", "success", map[string]any{"provider": s.Config.Auth.Provider})
	writeJSON(w, http.StatusOK, map[string]any{"auth_url": u.String(), "expires_at": expiresAt, "status": "pending"})
}

func (s *Server) handleBrowserAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	s.authMu.Lock()
	pending, ok := s.oauthStates[state]
	if ok {
		delete(s.oauthStates, state)
	}
	s.authMu.Unlock()
	if !ok || time.Now().UTC().After(pending.ExpiresAt) || code == "" {
		writeError(w, http.StatusUnauthorized, "auth state expired or invalid")
		return
	}
	email, err := s.exchangeOAuthCode(r.Context(), code)
	if err != nil {
		s.auditAuth("", "", pending.ProjectID, "auth_failure", "failure", map[string]any{"reason": err.Error()})
		writeError(w, http.StatusUnauthorized, "OAuth provider login failed")
		return
	}
	issued, err := s.Auth.IssuePATForEmail(r.Context(), email, pending.ProjectID, 90*24*time.Hour)
	if err != nil {
		s.auditAuth("", "", pending.ProjectID, "token_issued", "failure", map[string]any{"email": email, "reason": err.Error()})
		writeError(w, http.StatusForbidden, "project membership not found")
		return
	}
	grant := randomToken("grant")
	s.authMu.Lock()
	s.authGrants[grant] = authGrant{Token: issued.Token, Session: issued.Session, ExpiresAt: time.Now().UTC().Add(90 * time.Second)}
	s.authMu.Unlock()
	s.auditAuth(issued.Session.OrgID, issued.Session.UserID, issued.Session.ProjectID, "token_issued", "success", map[string]any{"provider": s.Config.Auth.Provider, "email": email})
	cb, _ := url.Parse(pending.LocalCallback)
	q := cb.Query()
	q.Set("code", grant)
	q.Set("state", pending.LocalState)
	cb.RawQuery = q.Encode()
	http.Redirect(w, r, cb.String(), http.StatusFound)
}

func (s *Server) handleBrowserAuthRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid auth redeem request")
		return
	}
	s.authMu.Lock()
	grant, ok := s.authGrants[req.Code]
	if ok {
		delete(s.authGrants, req.Code)
	}
	s.authMu.Unlock()
	if !ok || time.Now().UTC().After(grant.ExpiresAt) {
		writeError(w, http.StatusUnauthorized, "auth grant expired or invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": grant.Token, "session": grant.Session})
}

func (s *Server) handlePATRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.Auth == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service is not configured")
		return
	}
	var req struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	issued, old, err := s.Auth.RotatePAT(r.Context(), bearerFromRequest(r), req.ProjectID, 90*24*time.Hour)
	if err != nil {
		s.auditAuth("", "", req.ProjectID, "token_rotated", "failure", map[string]any{"reason": err.Error()})
		writeError(w, http.StatusUnauthorized, "PAT rotation failed")
		return
	}
	s.auditAuth(old.OrgID, old.UserID, old.ProjectID, "token_rotated", "success", map[string]any{"old_token_id": old.TokenID, "new_expires_at": issued.ExpiresAt})
	writeJSON(w, http.StatusOK, map[string]any{"token": issued.Token, "session": issued.Session})
}

func (s *Server) handlePATRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.Auth == nil {
		writeError(w, http.StatusServiceUnavailable, "auth service is not configured")
		return
	}
	var req struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	result, err := s.Auth.RevokePAT(r.Context(), bearerFromRequest(r), req.ProjectID)
	if err != nil {
		s.auditAuth("", "", req.ProjectID, "token_revoked", "failure", map[string]any{"reason": err.Error()})
		writeError(w, http.StatusUnauthorized, "PAT revocation failed")
		return
	}
	s.auditAuth(result.OrgID, result.UserID, result.ProjectID, "token_revoked", "success", map[string]any{"token_id": result.TokenID})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "session": result})
}

func (s *Server) exchangeOAuthCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", s.Config.Auth.ClientID)
	form.Set("client_secret", s.Config.Auth.ClientSecret)
	form.Set("redirect_uri", s.Config.Auth.RedirectURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Config.Auth.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token exchange status %d", resp.StatusCode)
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokenResp); err != nil {
		return "", err
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("missing access token")
	}
	userReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Config.Auth.UserInfoURL, nil)
	if err != nil {
		return "", err
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	userReq.Header.Set("Accept", "application/json")
	userResp, err := http.DefaultClient.Do(userReq)
	if err != nil {
		return "", err
	}
	defer userResp.Body.Close()
	if userResp.StatusCode < 200 || userResp.StatusCode >= 300 {
		return "", fmt.Errorf("userinfo status %d", userResp.StatusCode)
	}
	var user struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(userResp.Body, 1<<20)).Decode(&user); err != nil {
		return "", err
	}
	if user.Email == "" {
		return "", fmt.Errorf("userinfo email missing")
	}
	return user.Email, nil
}

func (s *Server) authConfigured() bool {
	cfg := s.Config.Auth
	return cfg.ClientID != "" && cfg.ClientSecret != "" && cfg.AuthURL != "" && cfg.TokenURL != "" && cfg.UserInfoURL != "" && cfg.RedirectURL != ""
}

func (s *Server) auditAuth(orgID, userID, projectID, action, result string, metadata map[string]any) {
	if s.Registry != nil && orgID != "" {
		s.Registry.Audit(orgID, projectID, userID, action, "auth", firstNonEmpty(projectID, userID, "auth"), result, metadata)
	}
}

func localCallbackAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost")
}

func bearerFromRequest(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func randomToken(prefix string) string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + base64Raw(raw[:])
}

func base64Raw(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "queued_webhooks": s.Queue.Len()})
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var push githubPush
	if err := json.Unmarshal(body, &push); err != nil {
		writeError(w, http.StatusBadRequest, "invalid github payload")
		return
	}
	branch := branchFromRef(push.Ref)
	route, ok := s.matchRoute(push.Repository.CloneURL, push.Repository.FullName, branch)
	if !ok {
		writeError(w, http.StatusNotFound, "no relay route for repo/branch")
		return
	}
	now := time.Now().UTC()
	ttl := time.Duration(s.Config.TTL)
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	env := Envelope{
		ID:             newID(),
		ProjectID:      route.ProjectID,
		ServiceID:      route.ServiceID,
		ServiceName:    route.ServiceName,
		ServiceType:    route.ServiceType,
		RepoURL:        firstNonEmpty(route.RepoURL, push.Repository.CloneURL),
		Ref:            push.Ref,
		After:          push.After,
		Branch:         branch,
		TriggeredBy:    push.Pusher.Name,
		Body:           string(body),
		Signature:      firstNonEmpty(r.Header.Get("X-Hub-Signature-256"), r.Header.Get("X-Hub-Signature")),
		IdempotencyKey: firstNonEmpty(r.Header.Get("X-GitHub-Delivery"), sha256Hex(body)),
		ReceivedAt:     now,
		ExpiresAt:      now.Add(ttl),
	}
	if err := s.Queue.Enqueue(env); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": env.ID, "expires_at": env.ExpiresAt})
}

func (s *Server) handleAgentWebhookNext(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/heartbeat") {
		s.handleAgentHeartbeat(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "/deployments/") && strings.HasSuffix(r.URL.Path, "/result") {
		s.handleAgentDeploymentResult(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "/node-lifecycle/") && strings.HasSuffix(r.URL.Path, "/result") {
		s.handleAgentNodeLifecycleResult(w, r)
		return
	}
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/webhooks/next") {
		http.NotFound(w, r)
		return
	}
	projectID := r.URL.Query().Get("project_id")
	nodeID := nodeIDFromAgentPath(r.URL.Path)
	agent, ok := s.authorizeAgent(w, r, projectID, nodeID)
	if !ok {
		return
	}
	lease, ok, err := s.Registry.LeaseDeployment(projectID, nodeID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		s.Registry.Audit(lease.Deployment.OrgID, projectID, agent.ID, "DEPLOYMENT_AGENT_LEASED", "deployment_job", lease.Deployment.ID, "success", map[string]any{"status": lease.Deployment.Status, "attempt_count": lease.Deployment.AttemptCount})
		writeJSON(w, http.StatusOK, map[string]any{"kind": "deployment", "deployment": lease.Deployment, "service": lease.Service, "action": lease.Action, "lease_token": lease.LeaseToken})
		return
	}
	lifecycle, ok, err := s.Registry.LeaseNodeLifecycle(projectID, nodeID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		job := lifecycle.Job
		s.Registry.Audit(job.OrgID, projectID, agent.ID, "NODE_LIFECYCLE_ACCEPTED", "node_lifecycle_job", job.ID, "success", map[string]any{"action": job.Action, "target_node_id": job.TargetNodeID, "status": job.Status, "attempt_count": job.AttemptCount})
		writeJSON(w, http.StatusOK, map[string]any{"kind": "node_lifecycle", "id": job.ID, "action": job.Action, "project_id": job.ProjectID, "target_node_id": job.TargetNodeID, "target_node_name": job.TargetNodeName, "confirm_remove": job.ConfirmRemove, "lease_token": lifecycle.LeaseToken})
		return
	}
	wait := 30 * time.Second
	if raw := r.URL.Query().Get("wait"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid wait")
			return
		}
		wait = parsed
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	env, err := s.Queue.Next(r.Context(), projectID, wait)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if env == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

func (s *Server) handleAgentDeploymentResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.URL.Query().Get("project_id")
	nodeID := nodeIDFromAgentPath(r.URL.Path)
	agent, ok := s.authorizeAgent(w, r, projectID, nodeID)
	if !ok {
		return
	}
	deploymentID := deploymentIDFromAgentPath(r.URL.Path)
	if deploymentID == "" {
		writeError(w, http.StatusBadRequest, "deployment id is required")
		return
	}
	var result registry.DeploymentResult
	if !decodeJSON(w, r, &result) {
		return
	}
	job, err := s.Registry.CompleteDeployment(projectID, nodeID, deploymentID, r.Header.Get("X-Request-ID"), result)
	if err == nil {
		s.observer.Inc("deployment_results_total")
		if job.Status == registry.DeploymentFailed {
			s.observer.Inc("deployment_failures_total")
		}
		outcome := "failed"
		if job.Status == registry.DeploymentSucceeded || job.Status == registry.DeploymentRolledBack {
			outcome = "success"
		}
		s.Registry.Audit(job.OrgID, projectID, "agent", "DEPLOYMENT_AGENT_RESULT_RECORDED", "deployment_job", job.ID, outcome, map[string]any{"status": job.Status, "failure_code": job.FailureCode})
	} else {
		s.Registry.Audit(agent.OrgID, projectID, agent.ID, "DEPLOYMENT_AGENT_RESULT_REJECTED", "deployment_job", deploymentID, "denied", map[string]any{"error": err.Error()})
	}
	writeRegistryResult(w, r, job, err, http.StatusOK)
}

func (s *Server) handleAgentNodeLifecycleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.URL.Query().Get("project_id")
	nodeID := nodeIDFromAgentPath(r.URL.Path)
	agent, ok := s.authorizeAgent(w, r, projectID, nodeID)
	if !ok {
		return
	}
	jobID := nodeLifecycleIDFromAgentPath(r.URL.Path)
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "node lifecycle job id is required")
		return
	}
	var result registry.NodeLifecycleResult
	if !decodeJSON(w, r, &result) {
		return
	}
	job, err := s.Registry.CompleteNodeLifecycle(projectID, nodeID, jobID, r.Header.Get("X-Request-ID"), result)
	if err == nil {
		outcome := "failure"
		action := "NODE_LIFECYCLE_FAILED"
		if job.Status == registry.NodeLifecycleCompleted {
			outcome = "success"
			action = "NODE_LIFECYCLE_COMPLETED"
		}
		if job.Status == registry.NodeLifecycleUnsupported {
			action = "NODE_LIFECYCLE_UNSUPPORTED"
		}
		s.Registry.Audit(job.OrgID, projectID, agent.ID, action, "node_lifecycle_job", job.ID, outcome, map[string]any{"status": job.Status, "action": job.Action, "target_node_id": job.TargetNodeID, "verified": job.Verified, "failure_code": job.FailureCode})
	} else {
		s.Registry.Audit(agent.OrgID, projectID, agent.ID, "NODE_LIFECYCLE_RESULT_REJECTED", "node_lifecycle_job", jobID, "denied", map[string]any{"error": err.Error()})
	}
	writeRegistryResult(w, r, job, err, http.StatusOK)
}

func nodeLifecycleIDFromAgentPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "node-lifecycle" {
			return parts[i+1]
		}
	}
	return ""
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.URL.Query().Get("project_id")
	nodeID := nodeIDFromAgentPath(r.URL.Path)
	if _, ok := s.authorizeAgent(w, r, projectID, nodeID); !ok {
		return
	}
	var req registry.AgentHeartbeat
	if !decodeJSON(w, r, &req) {
		return
	}
	node, err := s.Registry.RecordAgentHeartbeat(projectID, nodeID, req)
	if err == nil {
		s.observer.Inc("agent_heartbeat_total")
		s.Registry.Audit(node.OrgID, projectID, "agent", "AGENT_HEARTBEAT_RECORDED", "node", node.ID, "success", map[string]any{"status": node.Status})
	}
	writeRegistryResult(w, r, node, err, http.StatusOK)
}

func (s *Server) authorizeAgent(w http.ResponseWriter, r *http.Request, projectID, nodeID string) (registry.Agent, bool) {
	if projectID == "" || nodeID == "" {
		writeError(w, http.StatusBadRequest, "project_id and node id are required")
		return registry.Agent{}, false
	}
	token := bearerToken(r)
	agent, err := s.Registry.VerifyAgent(projectID, nodeID, token)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return registry.Agent{}, false
	}
	if s.Config.RequireAgentSignatures && !validAgentSignature(r, token) {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "AGENT_SIGNATURE_INVALID", Message: "agent request signature is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return registry.Agent{}, false
	}
	return agent, true
}

func validAgentSignature(r *http.Request, token string) bool {
	ts := r.Header.Get("X-Agent-Timestamp")
	sig := r.Header.Get("X-Agent-Signature")
	if ts == "" || sig == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	now := time.Now().UTC()
	if parsed.Before(now.Add(-5*time.Minute)) || parsed.After(now.Add(5*time.Minute)) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(r.Method + "\n" + r.URL.RequestURI() + "\n" + ts))
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

func nodeIDFromAgentPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "agents" {
		return ""
	}
	return parts[2]
}

func deploymentIDFromAgentPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 6 || parts[3] != "deployments" {
		return ""
	}
	return parts[4]
}

func (s *Server) handleOTPRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req otp.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid otp request")
		return
	}
	resp, err := s.OTP.RequestOTP(r.Context(), req)
	if err != nil {
		if err == otp.ErrRateLimited {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"request_id": resp.RequestID, "expires_at": resp.ExpiresAt}
	if resp.Code != "" {
		out["code"] = resp.Code
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Server) handleOTPVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RequestID string `json:"request_id"`
		ProjectID string `json:"project_id"`
		UserID    string `json:"user_id"`
		Purpose   string `json:"purpose"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid otp verify request")
		return
	}
	err := s.OTP.VerifyOTP(r.Context(), req.RequestID, req.ProjectID, req.UserID, req.Purpose, req.Code)
	if err != nil {
		statusCode := http.StatusUnauthorized
		if err == otp.ErrExpired || err == otp.ErrUsed || err == otp.ErrNotFound {
			statusCode = http.StatusGone
		}
		writeError(w, statusCode, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) matchRoute(repoURL, fullName, branch string) (Route, bool) {
	for _, route := range s.Config.Routes {
		if route.Branch != "" && route.Branch != branch {
			continue
		}
		if route.RepoURL != "" && route.RepoURL == repoURL {
			return route, true
		}
		if route.RepoFullName != "" && route.RepoFullName == fullName {
			return route, true
		}
	}
	return Route{}, false
}

type githubPush struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		CloneURL string `json:"clone_url"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

func branchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("webhook-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data[:])
}
