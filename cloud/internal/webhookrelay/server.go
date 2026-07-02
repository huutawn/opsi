package webhookrelay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/otp"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type Server struct {
	Queue         *Queue
	Config        Config
	OTP           *otp.Service
	Auth          *auth.Service
	Registry      registry.API
	credentials   CredentialVault
	registrations RegistrationVault
	limits        RateLimiter
	observer      *Observer
}

func NewServer(cfg Config) *Server {
	service := otp.NewService()
	service.DevEcho = cfg.OTP.DevEcho
	return &Server{Queue: NewQueue(), Config: cfg, OTP: service, Registry: registry.NewService(), credentials: NewCredentialStore(), registrations: NewRegistrationTokenStore(), limits: newRateLimiter(), observer: NewObserver()}
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
	mux.HandleFunc("/v1/ai/incidents/analyze", s.handleAnalyzeIncident)
	mux.HandleFunc("/v1/otp/request", s.handleOTPRequest)
	mux.HandleFunc("/v1/otp/verify", s.handleOTPVerify)
	mux.HandleFunc("/v1/agents/register", s.handleAgentRegister)
	mux.HandleFunc("/v1/agents/", s.handleAgentWebhookNext)
	mux.HandleFunc("/internal/bootstrap/sessions/", s.handleBootstrapWorker)
	mux.HandleFunc("/api/", s.handleRegistryAPI)
	mux.HandleFunc("/", s.handleUI)
	return s.observer.Wrap(mux)
}

func (s *Server) handleAnalyzeIncident(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SchemaVersion string `json:"schema_version"`
		IncidentID    string `json:"incident_id"`
		ProjectID     string `json:"project_id"`
		ServiceID     string `json:"service_id"`
		AnomalyType   string `json:"anomaly_type"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid incident context")
		return
	}
	if req.SchemaVersion != "opsi.incident_context.v1" || req.IncidentID == "" || req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, "invalid incident context")
		return
	}
	// ponytail: Gemini call skipped; add provider adapter when API key/config exists.
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": "opsi.rca.v1",
		"incident_id":    req.IncidentID,
		"root_cause":     "Cloud fixture RCA: " + firstNonEmpty(req.AnomalyType, "unknown anomaly"),
		"confidence":     0.64,
		"contributing_factors": []string{
			"sanitized metric context",
			"service-level anomaly",
		},
		"recommended_actions": []map[string]any{
			{"id": "scale-replicas", "type": "scale_replicas", "description": "Scale service replicas", "rollback_safe": true, "params": map[string]string{"service_id": req.ServiceID, "replicas": "2"}},
			{"id": "rate-limit-ingress", "type": "rate_limit_ingress", "description": "Apply ingress rate limit", "rollback_safe": true, "params": map[string]string{"service_id": req.ServiceID, "rps": "10"}},
		},
	})
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
		ID:          newID(),
		ProjectID:   route.ProjectID,
		ServiceID:   route.ServiceID,
		ServiceName: route.ServiceName,
		ServiceType: route.ServiceType,
		RepoURL:     firstNonEmpty(route.RepoURL, push.Repository.CloneURL),
		Ref:         push.Ref,
		After:       push.After,
		Branch:      branch,
		TriggeredBy: push.Pusher.Name,
		Body:        string(body),
		Signature:   firstNonEmpty(r.Header.Get("X-Hub-Signature-256"), r.Header.Get("X-Hub-Signature")),
		ReceivedAt:  now,
		ExpiresAt:   now.Add(ttl),
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
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/webhooks/next") {
		http.NotFound(w, r)
		return
	}
	projectID := r.URL.Query().Get("project_id")
	nodeID := nodeIDFromAgentPath(r.URL.Path)
	if _, ok := s.authorizeAgent(w, r, projectID, nodeID); !ok {
		return
	}
	lease, ok, err := s.Registry.LeaseDeployment(projectID, nodeID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		writeJSON(w, http.StatusOK, map[string]any{"kind": "deployment", "deployment": lease.Deployment, "service": lease.Service, "action": lease.Action})
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
	if _, ok := s.authorizeAgent(w, r, projectID, nodeID); !ok {
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
	}
	writeRegistryResult(w, r, job, err, http.StatusOK)
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
	writeJSON(w, http.StatusAccepted, map[string]any{"request_id": resp.RequestID, "expires_at": resp.ExpiresAt, "code": resp.Code})
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
