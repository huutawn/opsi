package webhookrelay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
	"github.com/opsi-dev/opsi/cloud/internal/otp"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type Server struct {
	Queue        RelayQueue
	Config       Config
	OTP          *otp.Service
	Auth         *auth.Service
	HTTPClient   *http.Client
	Registry     registry.API
	BuildRecords buildrecord.Service
	Topology     topology.Service
	Policies     deploymentpolicy.Service
	OIDC         interface {
		Verify(context.Context, string) (githuboidc.VerifiedIdentity, error)
	}
	oidcInitError           error
	credentials             CredentialVault
	registrations           RegistrationVault
	limits                  RateLimiter
	observer                *Observer
	alerts                  *AlertManager
	healthCheck             func(context.Context) error
	githubAppClient         *GitHubAppClient
	githubAppEventSink      GitHubAppEventSink
	githubReplay            *githubReplayStore
	buildRecordSlots        chan struct{}
	authMu                  sync.Mutex
	oauthStates             map[string]oauthState
	authGrants              map[string]authGrant
	installationClaimGrants map[string]installationClaimGrant
	now                     func() time.Time
	random                  io.Reader
}

func NewServer(cfg Config) *Server {
	service := otp.NewService()
	service.DevEcho = cfg.OTP.DevEcho
	oidcConfig := cfg.GitHubOIDC
	if oidcConfig.Issuer == "" {
		oidcConfig = githuboidc.DefaultConfig()
	}
	verifier, verifierErr := githuboidc.New(oidcConfig)
	registryService := registry.NewService()
	topologyService := topology.Service{Store: topology.NewMemoryStore(), Facts: registryService, HeartbeatTTL: time.Duration(cfg.Placement.HeartbeatTTL), ReservedCPU: cfg.Placement.ReservedCPUMilli, ReservedMemory: cfg.Placement.ReservedMemoryBytes}
	buildRecordService := buildrecord.Service{Store: buildrecord.NewMemoryStore(), Bindings: registryService, Policies: oidcConfig.Workloads}
	server := &Server{
		Queue:                   NewQueue(),
		Config:                  cfg,
		OTP:                     service,
		HTTPClient:              newGitHubHTTPClient(),
		Registry:                registryService,
		BuildRecords:            buildRecordService,
		Topology:                topologyService,
		Policies:                deploymentpolicy.Service{Store: deploymentpolicy.NewMemoryStore(), BuildRecords: buildRecordService.Store, Bindings: registryService, Topology: topologyService},
		OIDC:                    verifier,
		oidcInitError:           verifierErr,
		credentials:             NewCredentialStore(),
		registrations:           NewRegistrationTokenStore(),
		limits:                  newRateLimiter(),
		observer:                NewObserver(),
		alerts:                  NewAlertManager(cfg.Alerts),
		buildRecordSlots:        make(chan struct{}, buildRecordMaxConcurrency),
		oauthStates:             map[string]oauthState{},
		authGrants:              map[string]authGrant{},
		installationClaimGrants: map[string]installationClaimGrant{},
		random:                  rand.Reader,
	}
	server.BuildRecords.AuditSink = func(event buildrecord.AuditEvent) {
		registryService.AuditWorkload(event.ProjectID, "BUILD_RECORD_SUBMITTED", event.RecordID, event.Result, map[string]any{"repository_id": event.RepositoryID, "run_id": event.RunID, "run_attempt": event.RunAttempt, "service_key": event.ServiceKey, "sha": event.SHA, "config_hash": event.ConfigHash, "oci_digest": event.OCIDigest})
	}
	server.githubReplay = newGitHubReplayStore(githubReplayMaxEntries, githubReplayTTL, server.clock)
	return server
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
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

func (s *Server) SetHealthCheck(check func(context.Context) error) {
	s.healthCheck = check
}

func (s *Server) SetGitHubAppClient(client *GitHubAppClient) {
	s.githubAppClient = client
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/webhooks/github", s.handleGitHubWebhook)
	mux.HandleFunc("/v1/webhooks/github-app", s.handleGitHubAppWebhook)
	mux.HandleFunc("/v1/build-records", s.handleBuildRecordSubmission)
	mux.HandleFunc("/v1/auth/pat/verify", s.handlePATVerify)
	mux.HandleFunc("/v1/auth/browser/start", s.handleBrowserAuthStart)
	mux.HandleFunc("/v1/auth/browser/callback", s.handleBrowserAuthCallback)
	mux.HandleFunc("/v1/auth/browser/redeem", s.handleBrowserAuthRedeem)
	mux.HandleFunc("/v1/projects/{project_id}/github/installations/{installation_id}/claim/start", s.handleInstallationClaimStart)
	mux.HandleFunc("/v1/github/installations/claim/redeem", s.handleInstallationClaimRedeem)
	mux.HandleFunc("/v1/projects/{project_id}/github/installations", s.handleGitHubInstallationsAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/repositories", s.handleGitHubRepositoriesAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/repositories/{repository_id}/claim", s.handleGitHubRepositoryClaimAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/bindings", s.handleGitHubBindingsAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/bindings/{binding_id}", s.handleGitHubBindingAPI)
	mux.HandleFunc("/v1/auth/pat/rotate", s.handlePATRotate)
	mux.HandleFunc("/v1/auth/pat/revoke", s.handlePATRevoke)
	mux.HandleFunc("/v1/otp/request", s.handleOTPRequest)
	mux.HandleFunc("/v1/otp/verify", s.handleOTPVerify)
	mux.HandleFunc("/v1/agents/register", s.handleAgentRegister)
	mux.HandleFunc("/v1/agents/", s.handleAgentWebhookNext)
	mux.HandleFunc("/internal/bootstrap/sessions/lease", s.handleBootstrapWorkerLeaseWithCheckpoint)
	mux.HandleFunc("/internal/bootstrap/sessions/{session_id}/checkpoint", s.handleBootstrapWorkerCheckpoint)
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

type capturedHTTPResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *capturedHTTPResponse) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *capturedHTTPResponse) WriteHeader(status int) { r.status = status }

func (r *capturedHTTPResponse) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}

func (s *Server) handleBootstrapWorkerLeaseWithCheckpoint(w http.ResponseWriter, r *http.Request) {
	requestBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(requestBody))
	var leaseRequest struct {
		WorkerID string `json:"worker_id"`
	}
	_ = json.Unmarshal(requestBody, &leaseRequest)
	captured := &capturedHTTPResponse{}
	s.handleBootstrapWorker(captured, r)
	if captured.status != http.StatusOK {
		for key, values := range captured.Header() {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.WriteHeader(captured.status)
		_, _ = w.Write(captured.body.Bytes())
		return
	}
	var response map[string]any
	if err := json.Unmarshal(captured.body.Bytes(), &response); err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	bundle, ok := response["bundle"].(map[string]any)
	if !ok {
		writeRegistryFailure(w, r, fmt.Errorf("bootstrap lease response is missing bundle"))
		return
	}
	sessionID, _ := bundle["session_id"].(string)
	projectID, _ := bundle["project_id"].(string)
	leaseToken, _ := response["lease_token"].(string)
	session, err := s.Registry.GetBootstrapSessionForLease(projectID, sessionID, strings.TrimSpace(leaseRequest.WorkerID), leaseToken, s.clock())
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	bundle["checkpoint"] = session.Checkpoint
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleBootstrapWorkerCheckpoint(w http.ResponseWriter, r *http.Request) {
	if s.Config.BootstrapWorkerToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Bootstrap-Worker-Token")), []byte(s.Config.BootstrapWorkerToken)) != 1 {
		writeRegistryError(w, registry.APIError{Status: http.StatusUnauthorized, Code: "BOOTSTRAP_WORKER_AUTH_REQUIRED", Message: "bootstrap worker token is required", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		ProjectID string `json:"project_id"`
		registry.BootstrapCheckpoint
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	workerID, leaseToken := bootstrapLeaseHeaders(r)
	session, err := s.Registry.UpdateBootstrapCheckpointForLease(request.ProjectID, r.PathValue("session_id"), workerID, leaseToken, request.BootstrapCheckpoint, s.clock())
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session.Checkpoint)
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
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid pat verify request")
		return
	}
	result, err := s.Auth.VerifyPAT(r.Context(), auth.VerifyRequest{Token: bearerFromRequest(r), ProjectID: req.ProjectID})
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
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

func (s *Server) auditAuth(orgID, userID, projectID, action, result string, metadata map[string]any) {
	if s.Registry != nil && orgID != "" {
		s.Registry.Audit(orgID, projectID, userID, action, "auth", firstNonEmpty(projectID, userID, "auth"), result, metadata)
	}
}

func bearerFromRequest(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.healthCheck != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.healthCheck(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
			return
		}
	}
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
	signature := firstNonEmpty(r.Header.Get("X-Hub-Signature-256"), r.Header.Get("X-Hub-Signature"))
	if !validGitHubWebhookSignature(route.WebhookSecret, body, signature) {
		writeError(w, http.StatusUnauthorized, "github webhook signature is invalid")
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
		Signature:      signature,
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

func validGitHubWebhookSignature(secret string, body []byte, signature string) bool {
	if len(secret) < 32 || len(body) == 0 || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
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
	if strings.Contains(r.URL.Path, "/deployments/") && strings.HasSuffix(r.URL.Path, "/progress") {
		s.handleAgentDeploymentProgress(w, r)
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
		if lease.Command != nil && lease.Deployment.AttemptCount == 1 {
			if err := s.validateLeasedDeploymentAuthority(r.Context(), lease.Deployment); err != nil {
				_, _ = s.Registry.CompleteDeployment(projectID, nodeID, lease.Deployment.ID, r.Header.Get("X-Request-ID"), registry.DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: "failed", LeaseToken: lease.LeaseToken, SpecHash: lease.Deployment.SpecHash, ApplicationImage: lease.Command.Image.Reference, FailureCode: "DEPLOYMENT_AUTHORITY_REVOKED", FailureMessageRedacted: "deployment authority changed before first Agent lease"})
				writeRegistryFailure(w, r, err)
				return
			}
		}
		s.observer.Inc("agent_jobs_leased_total")
		s.Registry.Audit(lease.Deployment.OrgID, projectID, agent.ID, "DEPLOYMENT_AGENT_LEASED", "deployment_job", lease.Deployment.ID, "success", map[string]any{"status": lease.Deployment.Status, "attempt_count": lease.Deployment.AttemptCount})
		writeJSON(w, http.StatusOK, map[string]any{"kind": "deployment", "deployment": lease.Deployment, "service": lease.Service, "action": lease.Action, "lease_token": lease.LeaseToken, "command": lease.Command})
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

type immutableDeploymentProgressStore interface {
	ProgressImmutableDeployment(string, string, string, string, deploymentv1.Progress) (registry.DeploymentJob, error)
}

func (s *Server) handleAgentDeploymentProgress(w http.ResponseWriter, r *http.Request) {
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
	store, ok := s.Registry.(immutableDeploymentProgressStore)
	if deploymentID == "" || !ok {
		writeRegistryError(w, registry.APIError{Status: http.StatusServiceUnavailable, Code: "DEPLOYMENT_PROGRESS_UNAVAILABLE", Message: "deployment progress store is unavailable", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	var progress deploymentv1.Progress
	if !decodeJSON(w, r, &progress) {
		return
	}
	if progress.SchemaVersion != deploymentv1.EventSchemaVersion {
		writeRegistryError(w, registry.APIError{Status: http.StatusBadRequest, Code: "DEPLOYMENT_PROGRESS_INVALID", Message: "deployment progress schema is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	job, err := store.ProgressImmutableDeployment(projectID, nodeID, deploymentID, r.Header.Get("X-Request-ID"), progress)
	writeRegistryResult(w, r, job, err, http.StatusOK)
}

func (s *Server) validateLeasedDeploymentAuthority(ctx context.Context, job registry.DeploymentJob) error {
	if job.Snapshot == nil {
		return nil
	}
	snapshot := job.Snapshot
	record, err := s.BuildRecords.Get(ctx, job.ProjectID, snapshot.Authority.BuildRecord.ID)
	if err != nil || record.Build.Status != "succeeded" || record.Build.OCIRepository != snapshot.Image.Repository || record.Build.OCIDigest != snapshot.Image.Digest || record.ActiveBindingID != snapshot.Authority.BuildRecord.ActiveBindingID {
		return registry.APIError{Status: 409, Code: "DEPLOYMENT_BUILD_AUTHORITY_REVOKED", Message: "BuildRecord or active service binding changed before Agent lease"}
	}
	decision, err := s.Policies.Route(ctx, job.ProjectID, deploymentpolicyv1.RoutingRequest{BuildRecordID: record.ID, EnvironmentID: snapshot.Authority.EnvironmentID})
	if err != nil || !decision.Eligible || decision.DecisionHash != snapshot.Authority.RoutingDecisionHash || decision.RuntimeID != job.RuntimeID || decision.NodeID != job.NodeID || decision.AgentID != job.AgentID {
		return registry.APIError{Status: 409, Code: "DEPLOYMENT_ROUTING_AUTHORITY_REVOKED", Message: "routing decision changed before Agent lease"}
	}
	plan, err := s.Topology.Get(ctx, job.ProjectID)
	if err != nil || plan.ID != snapshot.Authority.TopologyPlanID || plan.Revision != snapshot.Authority.TopologyRevision || plan.PlanHash != snapshot.Authority.TopologyHash {
		return registry.APIError{Status: 409, Code: "DEPLOYMENT_TOPOLOGY_AUTHORITY_REVOKED", Message: "TopologyPlan changed before Agent lease"}
	}
	policy, err := s.Policies.Get(ctx, job.ProjectID, snapshot.Authority.DeploymentPolicyID)
	if err != nil || !policy.Draft.Enabled || policy.Revision != snapshot.Authority.DeploymentPolicyRevision || policy.PolicyHash != snapshot.Authority.DeploymentPolicyHash {
		return registry.APIError{Status: 409, Code: "DEPLOYMENT_POLICY_AUTHORITY_REVOKED", Message: "DeploymentPolicy changed before Agent lease"}
	}
	return nil
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
	var body struct {
		ProjectID string `json:"project_id"`
		UserID    string `json:"user_id,omitempty"`
		Purpose   string `json:"purpose"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid otp request")
		return
	}
	body.ProjectID = strings.TrimSpace(body.ProjectID)
	body.UserID = strings.TrimSpace(body.UserID)
	body.Purpose = strings.TrimSpace(body.Purpose)
	if body.ProjectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if body.Purpose == "" {
		writeError(w, http.StatusBadRequest, "purpose is required")
		return
	}
	if s.Auth == nil && body.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	req := otp.Request{
		ProjectID: body.ProjectID,
		UserID:    body.UserID,
		Purpose:   body.Purpose,
	}
	principal, ok := s.authorizeProject(w, r, req.ProjectID)
	if !ok {
		return
	}
	if s.Auth != nil {
		if req.UserID != "" && req.UserID != principal.UserID {
			writeError(w, http.StatusForbidden, "otp user does not match authenticated user")
			return
		}
		req.UserID = principal.UserID
		req.Email = principal.Email
		if req.Email == "" {
			writeError(w, http.StatusUnprocessableEntity, "authenticated user has no email address")
			return
		}
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
	principal, ok := s.authorizeProject(w, r, req.ProjectID)
	if !ok {
		return
	}
	if s.Auth != nil {
		if req.UserID != "" && req.UserID != principal.UserID {
			writeError(w, http.StatusForbidden, "otp user does not match authenticated user")
			return
		}
		req.UserID = principal.UserID
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
