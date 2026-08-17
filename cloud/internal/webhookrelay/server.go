package webhookrelay

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/actiondevice"
	"github.com/opsi-dev/opsi/cloud/internal/auth"
	backupdomain "github.com/opsi-dev/opsi/cloud/internal/backup"
	"github.com/opsi-dev/opsi/cloud/internal/bootstrapworker"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	cutoverdomain "github.com/opsi-dev/opsi/cloud/internal/cutover"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
	"github.com/opsi-dev/opsi/cloud/internal/otp"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
	restoredomain "github.com/opsi-dev/opsi/cloud/internal/restore"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

type Server struct {
	Config                  Config
	OTP                     *otp.Service
	Auth                    *auth.Service
	HTTPClient              *http.Client
	Registry                registry.API
	Resources               resource.Service
	Backups                 backupdomain.Service
	Restores                restoredomain.Service
	Cutovers                cutoverdomain.Service
	BuildJobs               buildjob.Service
	BuildRecords            buildrecord.Service
	RegistryPullCredentials RegistryPullCredentialProvider
	Topology                topology.Service
	Policies                deploymentpolicy.Service
	OIDC                    interface {
		Verify(context.Context, string) (githuboidc.VerifiedIdentity, error)
	}
	RunnerOIDC interface {
		Verify(context.Context, string) (githuboidc.VerifiedIdentity, error)
	}
	oidcInitError           error
	runnerOIDCInitError     error
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
	authSelectionGrants     map[string]authGrant
	installationClaimGrants map[string]installationClaimGrant
	now                     func() time.Time
	random                  io.Reader
	actionDeviceMu          sync.Mutex
	actionDevices           actiondevice.Store
	bootstrapInstall        bootstrapworker.InstallConfig
	bootstrapRunnerPath     string
	bootstrapRunnerSHA256   string
}

func NewServer(cfg Config) *Server {
	service := otp.NewService()
	service.DevEcho = cfg.OTP.DevEcho
	oidcConfig := cfg.GitHubOIDC
	if oidcConfig.Issuer == "" {
		oidcConfig = githuboidc.DefaultConfig()
	}
	verifier, verifierErr := githuboidc.New(oidcConfig)
	runnerOIDCConfig := githuboidc.DefaultConfig()
	runnerOIDCConfig.Enabled = true
	runnerOIDCConfig.Audience = buildjob.RunnerOIDCAudience
	runnerVerifier, runnerVerifierErr := githuboidc.NewIdentityVerifier(runnerOIDCConfig)
	registryService := registry.NewService()
	resourceService := resource.Service{Store: resource.NewMemoryStore(), Scopes: registryService, Credentials: resource.NewMemoryCredentialAuthority()}
	backupService := backupdomain.Service{Store: backupdomain.NewMemoryStore(), Resources: resourceService}
	restoreService := restoredomain.Service{Store: restoredomain.NewMemoryStore(), Backups: backupService, Resources: resourceService}
	if cfg.BackupStore.Enabled() {
		backupService.Artifacts = backupStoreAuthority(cfg.BackupStore)
	}
	resourceService.Operations = []resource.ActiveOperationAuthority{backupService, restoreService}
	backupService.Resources = resourceService
	restoreService.Resources = resourceService
	restoreService.Backups = backupService
	restoreService.Artifacts = backupService.Artifacts
	cutoverService := cutoverdomain.Service{Store: cutoverdomain.NewMemoryStore(), Applications: registryService, Resources: resourceService, Restores: restoreService, Backups: backupService, Credentials: resourceService.Credentials}
	topologyService := topology.Service{Store: topology.NewMemoryStore(), Facts: registryService, HeartbeatTTL: time.Duration(cfg.Placement.HeartbeatTTL), ReservedCPU: cfg.Placement.ReservedCPUMilli, ReservedMemory: cfg.Placement.ReservedMemoryBytes}
	buildRecordService := buildrecord.Service{Store: buildrecord.NewMemoryStore(), Bindings: registryService, Policies: oidcConfig.Workloads}
	buildJobService := buildjob.Service{Store: buildjob.NewMemoryStore(), Sources: registryService, Executor: cfg.BuildExecutor, Registry: cfg.BuildRegistry}
	server := &Server{
		Config:                  cfg,
		OTP:                     service,
		HTTPClient:              newGitHubHTTPClient(),
		Registry:                registryService,
		Resources:               resourceService,
		Backups:                 backupService,
		Restores:                restoreService,
		Cutovers:                cutoverService,
		BuildJobs:               buildJobService,
		BuildRecords:            buildRecordService,
		Topology:                topologyService,
		Policies:                deploymentpolicy.Service{Store: deploymentpolicy.NewMemoryStore(), BuildRecords: buildRecordService.Store, Bindings: registryService, Topology: topologyService},
		OIDC:                    verifier,
		RunnerOIDC:              runnerVerifier,
		oidcInitError:           verifierErr,
		runnerOIDCInitError:     runnerVerifierErr,
		credentials:             NewCredentialStore(),
		registrations:           NewRegistrationTokenStore(),
		limits:                  newRateLimiter(),
		observer:                NewObserver(),
		alerts:                  NewAlertManager(cfg.Alerts),
		buildRecordSlots:        make(chan struct{}, buildRecordMaxConcurrency),
		oauthStates:             map[string]oauthState{},
		authGrants:              map[string]authGrant{},
		authSelectionGrants:     map[string]authGrant{},
		installationClaimGrants: map[string]installationClaimGrant{},
		random:                  rand.Reader,
	}
	server.Cutovers.Deployments = registryService
	server.Cutovers.BuildRecords = buildRecordService.Store
	server.Cutovers.Topology = topologyService
	server.Cutovers.Policies = server.Policies
	server.Cutovers.RuntimeResolver = resourceService
	server.BuildRecords.AuditSink = func(event buildrecord.AuditEvent) {
		registryService.AuditWorkload(event.ProjectID, "BUILD_RECORD_SUBMITTED", event.RecordID, event.Result, map[string]any{"repository_id": event.RepositoryID, "run_id": event.RunID, "run_attempt": event.RunAttempt, "service_key": event.ServiceKey, "sha": event.SHA, "config_hash": event.ConfigHash, "oci_digest": event.OCIDigest})
	}
	server.githubReplay = newGitHubReplayStore(githubReplayMaxEntries, githubReplayTTL, server.clock)
	if cfg.BuildRegistry.Visibility == "private" {
		server.RegistryPullCredentials = NewGHCRRegistryPullCredentialProvider(cfg.BuildRegistry, nil, cfg.RegistryPull)
	}
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

func (s *Server) SetBootstrapRunner(install bootstrapworker.InstallConfig, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	s.bootstrapInstall = install
	s.bootstrapRunnerPath = path
	s.bootstrapRunnerSHA256 = hex.EncodeToString(hash.Sum(nil))
	return nil
}

func (s *Server) SetGitHubAppClient(client *GitHubAppClient) {
	s.githubAppClient = client
	s.BuildJobs.Repository = client
	s.BuildJobs.Dispatcher = client
}

func (s *Server) SetActionDeviceStore(store actiondevice.Store) {
	s.actionDeviceMu.Lock()
	defer s.actionDeviceMu.Unlock()
	s.actionDevices = store
}

func (s *Server) Handler() http.Handler {
	s.ensureActionDeviceStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/webhooks/github-app", s.handleGitHubAppWebhook)
	mux.HandleFunc("/v1/build-records", s.handleBuildRecordSubmission)
	mux.HandleFunc("/v1/auth/pat/verify", s.handlePATVerify)
	mux.HandleFunc("/v1/auth/browser/start", s.handleBrowserAuthStart)
	mux.HandleFunc("/v1/auth/browser/callback", s.handleBrowserAuthCallback)
	mux.HandleFunc("/v1/auth/browser/redeem", s.handleBrowserAuthRedeem)
	mux.HandleFunc("/v1/auth/browser/select-project", s.handleBrowserAuthSelectProject)
	mux.HandleFunc("/v1/projects/{project_id}/github/installations/{installation_id}/claim/start", s.handleInstallationClaimStart)
	mux.HandleFunc("/v1/github/installations/claim/redeem", s.handleInstallationClaimRedeem)
	mux.HandleFunc("/v1/projects/{project_id}/github/installations", s.handleGitHubInstallationsAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/repositories", s.handleGitHubRepositoriesAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/repositories/{repository_id}/claim", s.handleGitHubRepositoryClaimAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/bindings", s.handleGitHubBindingsAPI)
	mux.HandleFunc("/v1/projects/{project_id}/github/bindings/{binding_id}", s.handleGitHubBindingAPI)
	mux.HandleFunc("/v1/projects/{project_id}/applications/{application_id}/build-jobs", s.handleBuildJobsAPI)
	mux.HandleFunc("/v1/projects/{project_id}/applications/{application_id}/build-jobs/{build_job_id}/dispatch", s.handleBuildJobDispatchAPI)
	mux.HandleFunc("/v1/projects/{project_id}/applications/{application_id}/build-jobs/{build_job_id}", s.handleBuildJobAPI)
	mux.HandleFunc("/v1/build-runner/claim", s.handleBuildRunnerClaim)
	mux.HandleFunc("/v1/build-runner/build-spec", s.handleBuildRunnerBuildSpec)
	mux.HandleFunc("/v1/build-runner/source-access", s.handleBuildRunnerSourceAccess)
	mux.HandleFunc("/v1/build-runner/result", s.handleBuildRunnerResult)
	mux.HandleFunc("/v1/build-runner/failure", s.handleBuildRunnerFailure)
	mux.HandleFunc("/v1/auth/pat/rotate", s.handlePATRotate)
	mux.HandleFunc("/v1/auth/pat/revoke", s.handlePATRevoke)
	mux.HandleFunc("/v1/otp/request", s.handleOTPRequest)
	mux.HandleFunc("/v1/otp/verify", s.handleOTPVerify)
	mux.HandleFunc("/v1/agents/register", s.handleAgentRegister)
	mux.HandleFunc("/v1/bootstrap/install", s.handleBootstrapInstall)
	mux.HandleFunc("/v1/bootstrap/runner/linux-amd64", s.handleBootstrapRunner)
	mux.HandleFunc("/v1/bootstrap/claim", s.handleBootstrapCommandClaim)
	mux.HandleFunc("/v1/bootstrap/sessions/{session_id}/checkpoint", s.handleBootstrapWorkerCheckpoint)
	mux.HandleFunc("/v1/bootstrap/sessions/", s.handleBootstrapWorker)
	mux.HandleFunc("/v1/agents/", s.handleAgentWebhookNext)
	mux.HandleFunc("/v1/agent/projects/{project_id}/action-devices/{device_id}", s.handleAgentActionDevice)
	mux.HandleFunc("/internal/bootstrap/sessions/lease", s.handleBootstrapWorker)
	mux.HandleFunc("/internal/bootstrap/sessions/{session_id}/checkpoint", s.handleBootstrapWorkerCheckpoint)
	mux.HandleFunc("/internal/bootstrap/sessions/", s.handleBootstrapWorker)
	mux.HandleFunc("/api/internal/alerts", s.handleInternalAlerts)
	mux.HandleFunc("/api/projects/{project_id}/action-devices", s.handleActionDevices)
	mux.HandleFunc("/api/projects/{project_id}/action-devices/{device_id}/revoke", s.handleActionDeviceRevoke)
	mux.HandleFunc("/api/", s.handleRegistryAPI)
	mux.HandleFunc("/", http.NotFound)
	return s.observer.Wrap(mux)
}

func (s *Server) handleBootstrapWorkerCheckpoint(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
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
	if strings.Contains(r.URL.Path, "/managed-resources/") && strings.HasSuffix(r.URL.Path, "/result") {
		s.handleAgentManagedResourceResult(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "/retained-storages/") && strings.HasSuffix(r.URL.Path, "/result") {
		s.handleAgentRetainedStorageResult(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "/backups/") && strings.HasSuffix(r.URL.Path, "/result") {
		s.handleAgentBackupResult(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "/restore-reviews/") && strings.HasSuffix(r.URL.Path, "/result") {
		s.handleAgentRestoreReviewResult(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "/restores/") && strings.HasSuffix(r.URL.Path, "/result") {
		s.handleAgentRestoreResult(w, r)
		return
	}
	if (strings.Contains(r.URL.Path, "/cutover-finalizations/") || strings.Contains(r.URL.Path, "/application-cutover-finalizations/")) && !strings.HasSuffix(r.URL.Path, "/webhooks/next") {
		s.handleAgentCutoverFinalizationResult(w, r)
		return
	}
	if (strings.Contains(r.URL.Path, "/cutover-rollbacks/") || strings.Contains(r.URL.Path, "/application-cutover-rollbacks/")) && !strings.HasSuffix(r.URL.Path, "/webhooks/next") {
		s.handleAgentCutoverRollbackResult(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "/cutover-reviews/") && (strings.HasSuffix(r.URL.Path, "/result") || !strings.HasSuffix(r.URL.Path, "/webhooks/next")) {
		s.handleAgentCutoverReviewResult(w, r)
		return
	}
	if (strings.Contains(r.URL.Path, "/cutovers/") || strings.Contains(r.URL.Path, "/application-cutovers/")) && !strings.HasSuffix(r.URL.Path, "/webhooks/next") {
		s.handleAgentCutoverResult(w, r)
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
	s.recoverAutomaticDeliveries(r.Context(), projectID)
	s.enqueueExpiredPreviewCleanup(projectID, agent.ID)
	lease, ok, err := s.Registry.LeaseDeployment(projectID, nodeID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if ok {
		s.resolveRegistryPullCredential(r.Context(), lease.Command)
		if lease.Command != nil && len(lease.Command.Workload.SecretReferences) > 0 {
			materials, err := s.Resources.ResolveSecretMaterials(r.Context(), projectID, lease.Command.Workload.SecretReferences)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]any{"failure_code": resourcev1.FailureBindingSecretMaterialization})
				return
			}
			lease.Command.SecretMaterials = materials
		}
		if lease.Command != nil && lease.Deployment.AttemptCount == 1 {
			if err := s.validateLeasedDeploymentAuthority(r.Context(), lease.Deployment); err != nil {
				_, _ = s.Registry.CompleteDeployment(projectID, nodeID, lease.Deployment.ID, r.Header.Get("X-Request-ID"), registry.DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: "failed", LeaseToken: lease.LeaseToken, SpecHash: lease.Deployment.SpecHash, ApplicationImage: lease.Command.Image.Reference, FailureCode: "DEPLOYMENT_AUTHORITY_REVOKED", FailureMessageRedacted: "deployment authority changed before first Agent lease"})
				writeRegistryFailure(w, r, err)
				return
			}
		}
		s.observer.Inc("agent_jobs_leased_total")
		s.Registry.Audit(lease.Deployment.OrgID, projectID, agent.ID, "DEPLOYMENT_AGENT_LEASED", "deployment_job", lease.Deployment.ID, "success", map[string]any{"status": lease.Deployment.Status, "attempt_count": lease.Deployment.AttemptCount})
		writeJSON(w, http.StatusOK, map[string]any{"kind": "deployment", "deployment": lease.Deployment, "action": lease.Action, "lease_token": lease.LeaseToken, "command": lease.Command})
		return
	}
	reviewLease, ok, err := s.Restores.LeaseReview(r.Context(), projectID, nodeID)
	if err != nil {
		writeRestoreResult(w, r, reviewLease, err, http.StatusOK)
		return
	}
	if reviewLease.Review.Lifecycle == restorev1.ReviewFailed {
		s.Registry.Audit(agent.OrgID, projectID, agent.ID, "RESTORE_FAILED", "restore_review", reviewLease.Review.ID, "failure", map[string]any{"failure_code": reviewLease.Review.FailureCode})
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		writeJSON(w, http.StatusOK, map[string]any{"kind": "restore_review", "lease_token": reviewLease.LeaseToken, "review": reviewLease.Review, "target_spec": reviewLease.TargetSpec})
		return
	}
	restoreLease, ok, err := s.Restores.Lease(r.Context(), projectID, nodeID)
	if err != nil {
		writeRestoreResult(w, r, restoreLease, err, http.StatusOK)
		return
	}
	if restoreLease.Restore.Lifecycle == restorev1.LifecycleFailed {
		s.Registry.Audit(agent.OrgID, projectID, agent.ID, "RESTORE_FAILED", "restore", restoreLease.Restore.ID, "failure", map[string]any{"target_resource_id": restoreLease.Restore.TargetResourceID, "failure_code": restoreLease.Restore.FailureCode})
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		writeJSON(w, http.StatusOK, map[string]any{"kind": "restore", "lease_token": restoreLease.LeaseToken, "restore": restoreLease.Restore, "backup": restoreLease.Backup, "target_spec": restoreLease.TargetSpec, "store": restoreLease.Store, "credential": restoreLease.Credential})
		return
	}
	cutoverLease, ok, err := s.Cutovers.LeaseReview(r.Context(), projectID, nodeID)
	if err != nil {
		writeCutoverResult(w, r, cutoverLease, err, http.StatusOK)
		return
	}
	if cutoverLease.Review.Lifecycle == cutoverv1.ReviewFailed {
		s.Registry.Audit(agent.OrgID, projectID, agent.ID, "CUTOVER_REVIEW_FAILED", "cutover_review", cutoverLease.Review.ID, "failure", map[string]any{"application_id": cutoverLease.Review.ApplicationID, "failure_code": cutoverLease.Review.FailureCode})
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":                         "cutover_review",
			"lease_token":                  cutoverLease.LeaseToken,
			"cutover_review_lease":         cutoverLease,
			"review":                       cutoverLease.Review,
			"source_spec":                  cutoverLease.SourceSpec,
			"target_spec":                  cutoverLease.TargetSpec,
			"source_credential":            cutoverLease.SourceCredential,
			"target_credential":            cutoverLease.TargetCredential,
			"target_management_credential": cutoverLease.TargetManagementCredential,
		})
		return
	}
	backupLease, ok, err := s.Backups.Lease(r.Context(), projectID, nodeID)
	if err != nil {
		writeBackupResult(w, r, backupLease, err, http.StatusOK)
		return
	}
	if backupLease.Backup.Lifecycle == backupv1.LifecycleFailed {
		s.Registry.Audit(agent.OrgID, projectID, agent.ID, "BACKUP_FAILED", "backup", backupLease.Backup.ID, "failure", map[string]any{"resource_id": backupLease.Backup.SourceResourceID, "failure_code": backupLease.Backup.FailureCode})
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		writeJSON(w, http.StatusOK, map[string]any{"kind": "backup", "lease_token": backupLease.LeaseToken, "backup": backupLease.Backup, "source_spec": backupLease.SourceSpec, "store": backupLease.Store, "credential": backupLease.Credential})
		return
	}
	retained, ok, err := s.Resources.LeaseRetainedStorageDestroy(r.Context(), projectID, nodeID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if ok {
		s.observer.Inc("agent_jobs_leased_total")
		writeJSON(w, http.StatusOK, map[string]any{"kind": "retained_storage", "lease_token": retained.LeaseToken, "spec": retained.Spec})
		return
	}
	managed, ok, err := s.Resources.LeaseManaged(r.Context(), projectID, nodeID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	if ok {
		if managed.Action == "apply" && (managed.Spec.ResourceType == resourcev1.TypeRedis || managed.Spec.ResourceType == resourcev1.TypePostgres) && managed.Credential == nil {
			writeJSON(w, http.StatusConflict, map[string]any{"failure_code": resourcev1.FailureCredentialUnavailable})
			return
		}
		s.observer.Inc("agent_jobs_leased_total")
		writeJSON(w, http.StatusOK, map[string]any{"kind": "managed_resource", "action": managed.Action, "lease_token": managed.LeaseToken, "spec": managed.Spec, "credential": managed.Credential, "bindings": managed.Bindings})
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
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentBackupResult(w http.ResponseWriter, r *http.Request) {
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
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 {
		http.NotFound(w, r)
		return
	}
	var result backupv1.Result
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	id := parts[len(parts)-2]
	started := false
	if result.Status == backupv1.LifecycleRunning {
		current, getErr := s.Backups.Get(r.Context(), projectID, id)
		started = getErr == nil && current.Lifecycle == backupv1.LifecycleLeased
	}
	value, err := s.Backups.Complete(r.Context(), projectID, id, result)
	if err != nil {
		writeBackupResult(w, r, value, err, http.StatusOK)
		return
	}
	action, outcome, metadata := "BACKUP_FAILED", "failure", map[string]any{"resource_id": value.SourceResourceID, "failure_code": value.FailureCode}
	if result.Status == backupv1.LifecycleRunning && started {
		action, outcome, metadata = "BACKUP_STARTED", "success", map[string]any{"resource_id": value.SourceResourceID, "attempt_count": value.AttemptCount}
	} else if result.Status == backupv1.LifecycleRunning {
		writeJSON(w, http.StatusOK, map[string]any{"backup": value})
		return
	} else if value.Lifecycle == backupv1.LifecycleSucceeded {
		action, outcome, metadata = "BACKUP_SUCCEEDED", "success", map[string]any{"resource_id": value.SourceResourceID, "sha256": value.SHA256, "artifact_size": value.ArtifactSize}
	}
	s.Registry.Audit(agent.OrgID, projectID, agent.ID, action, "backup", id, outcome, metadata)
	writeJSON(w, http.StatusOK, map[string]any{"backup": value})
}

func (s *Server) handleAgentManagedResourceResult(w http.ResponseWriter, r *http.Request) {
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
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 {
		http.NotFound(w, r)
		return
	}
	var result resource.ManagedResult
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	value, err := s.Resources.CompleteManaged(r.Context(), projectID, parts[len(parts)-2], result)
	if err != nil {
		writeResourceResult(w, r, value, err, http.StatusOK)
		return
	}
	if result.Status == "deleted" {
		if retained, retainedErr := s.Resources.GetRetainedStorageByResource(r.Context(), projectID, parts[len(parts)-2]); retainedErr == nil {
			s.Registry.Audit(agent.OrgID, projectID, agent.ID, "RESOURCE_RUNTIME_DELETED_STORAGE_RETAINED", "resource", parts[len(parts)-2], "success", map[string]any{"retained_storage_id": retained.ID, "pvc_uid": retained.PVCUID, "pv_uid": retained.PVUID})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource": value})
}

func (s *Server) handleAgentRetainedStorageResult(w http.ResponseWriter, r *http.Request) {
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
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 {
		http.NotFound(w, r)
		return
	}
	var result resource.RetainedStorageResult
	if !decodeResourceJSON(w, r, &result) {
		return
	}
	id := parts[len(parts)-2]
	value, err := s.Resources.CompleteRetainedStorageDestroy(r.Context(), projectID, id, result)
	if err != nil {
		writeResourceResult(w, r, value, err, http.StatusOK)
		return
	}
	action, outcome := "RETAINED_STORAGE_DESTROY_FAILED", "failure"
	if value.Lifecycle == resourcev1.RetainedStorageDestroyed {
		action, outcome = "RETAINED_STORAGE_DESTROYED", "success"
	}
	s.Registry.Audit(agent.OrgID, projectID, agent.ID, action, "retained_storage", id, outcome, map[string]any{"lifecycle": value.Lifecycle, "failure_code": value.FailureCode})
	writeJSON(w, http.StatusOK, map[string]any{"retained_storage": value})
}

func (s *Server) recoverAutomaticDeliveries(ctx context.Context, projectID string) {
	result, err := s.BuildRecords.List(ctx, projectID, buildrecord.ListFilter{Status: "succeeded", Limit: 100})
	if err != nil {
		return
	}
	for _, record := range result.Records {
		_, _, _ = s.ensureAutomaticDelivery(ctx, record)
	}
}

func (s *Server) enqueueExpiredPreviewCleanup(projectID, actor string) {
	store, ok := s.Registry.(previewCleanupStore)
	if !ok {
		return
	}
	jobs, err := s.Registry.ListDeployments(projectID)
	if err != nil {
		return
	}
	now := s.clock()
	for _, job := range jobs {
		if job.Snapshot == nil || job.Snapshot.Preview == nil || !now.After(job.Snapshot.Preview.ExpiresAt) || job.Action == deploymentv1.RolloutOperationCleanup {
			continue
		}
		_, _, _ = store.StartPreviewCleanup(projectID, actor, "ttl:"+job.ID, "ttl-expiry", deploymentv1.PreviewCleanupRequest{DeploymentID: job.ID, Reason: "ttl_expired"})
	}
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
	} else {
		code := "DEPLOYMENT_RESULT_REJECTED"
		var apiError registry.APIError
		if errors.As(err, &apiError) && apiError.Code != "" {
			code = apiError.Code
		}
		s.Registry.Audit(agent.OrgID, projectID, "agent", "DEPLOYMENT_AGENT_RESULT_REJECTED", "deployment_job", deploymentID, "denied", map[string]any{"error_code": code, "deployment_id": deploymentID})
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
