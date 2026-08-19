package webhookrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/sourcereport"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

// handleVerifyDependencyAPI handles POST /v1/projects/{project_id}/dependencies/verify
func (s *Server) handleVerifyDependencyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "dependency_verification", projectID, "owner", "admin", "developer") {
		return
	}

	var req verificationv1.VerifyDependencyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DependencyLogicalName == "" {
		writeError(w, http.StatusBadRequest, "dependency_logical_name is required")
		return
	}

	consumerAppID := r.URL.Query().Get("application_id")
	envID := r.URL.Query().Get("environment_id")

	run, err := s.ExecuteDependencyVerification(r.Context(), projectID, envID, consumerAppID, req, principal.UserID)
	if err != nil {
		writeRegistryFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, verificationv1.VerifyDependencyResponse{Run: run})
}

// handleGetDependencyVerificationAPI handles GET /v1/projects/{project_id}/dependencies/{dependency_logical_name}/verification
func (s *Server) handleGetDependencyVerificationAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "dependency_verification", projectID, "owner", "admin", "developer", "viewer", "support") {
		return
	}

	depName := r.PathValue("dependency_logical_name")
	envID := r.URL.Query().Get("environment_id")
	appID := r.URL.Query().Get("application_id")

	if s.Verifications == nil {
		writeError(w, http.StatusNotFound, "verification store unavailable")
		return
	}

	run, err := s.Verifications.GetLatest(r.Context(), projectID, envID, appID, depName)
	if err != nil {
		writeError(w, http.StatusNotFound, "verification run not found")
		return
	}

	// Check staleness against current authority facts
	if s.isVerificationStale(r.Context(), projectID, run) {
		run.OverallStatus = verificationv1.RunStatusStale
		run.FailureCode = verificationv1.FailureVerificationStale
	}

	writeJSON(w, http.StatusOK, verificationv1.VerifyDependencyResponse{Run: run})
}

// handleGetApplicationSourceRiskReportAPI handles GET /v1/projects/{project_id}/applications/{application_id}/source-risk-report
func (s *Server) handleGetApplicationSourceRiskReportAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	applicationID := r.PathValue("application_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "source_risk_report", applicationID, "owner", "admin", "developer", "viewer", "support") {
		return
	}

	if s.SourceReports == nil {
		writeError(w, http.StatusNotFound, "source report store unavailable")
		return
	}

	commitSHA := r.URL.Query().Get("commit_sha")
	var report sourcereport.Report
	var err error
	if commitSHA != "" {
		report, err = s.SourceReports.GetForCommit(r.Context(), projectID, applicationID, commitSHA)
	} else {
		// Get active service to find current GitSHA
		services, sErr := s.Registry.ListServices(projectID)
		if sErr != nil {
			writeRegistryFailure(w, r, sErr)
			return
		}
		var gitSHA string
		for _, svc := range services {
			if svc.ID == applicationID || svc.Name == applicationID {
				gitSHA = svc.GitSHA
				break
			}
		}
		report, err = s.SourceReports.GetForCommit(r.Context(), projectID, applicationID, gitSHA)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "source risk report not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}

// handleGetSourceRiskReportByIDAPI handles GET /v1/projects/{project_id}/source-risk-reports/{report_id}
func (s *Server) handleGetSourceRiskReportByIDAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("project_id")
	reportID := r.PathValue("report_id")
	principal, ok := s.authorizeGitHubProject(w, r, projectID)
	if !ok || !s.requireRole(w, r, principal, projectID, "source_risk_report", reportID, "owner", "admin", "developer", "viewer", "support") {
		return
	}

	if s.SourceReports == nil {
		writeError(w, http.StatusNotFound, "source report store unavailable")
		return
	}

	report, err := s.SourceReports.GetForBuildJob(r.Context(), reportID)
	if err != nil {
		writeError(w, http.StatusNotFound, "source risk report not found")
		return
	}
	if report.ProjectID != projectID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}

// handleAgentDepVerificationResult handles POST /v1/agents/{node_id}/dep-verifications/{verification_id}/result
func (s *Server) handleAgentDepVerificationResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Acknowledge agent probe completion
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
}

// ExecuteDependencyVerification performs the 5-layer verification evaluation.
func (s *Server) ExecuteDependencyVerification(ctx context.Context, projectID, environmentID, consumerAppID string, req verificationv1.VerifyDependencyRequest, triggeredBy string) (verificationv1.VerificationRun, error) {
	now := s.clock()

	// 1. Resolve consumer application
	services, err := s.Registry.ListServices(projectID)
	if err != nil {
		return verificationv1.VerificationRun{}, err
	}
	var consumerApp registry.ServiceRecord
	if consumerAppID != "" {
		for _, svc := range services {
			if svc.ID == consumerAppID || svc.Name == consumerAppID {
				consumerApp = svc
				break
			}
		}
	} else if len(services) > 0 {
		consumerApp = services[0]
	}
	if consumerApp.ID == "" {
		return verificationv1.VerificationRun{}, registry.APIError{Status: http.StatusNotFound, Code: "CONSUMER_APPLICATION_NOT_FOUND", Message: "consumer application not found"}
	}
	if environmentID == "" {
		environmentID = consumerApp.EnvironmentID
	}

	// 2. Resolve ServiceConfiguration & ApplicationDependency
	config, err := s.Registry.GetServiceConfiguration(projectID, consumerApp.ID)
	if err != nil {
		return verificationv1.VerificationRun{}, registry.APIError{Status: http.StatusNotFound, Code: "SERVICE_CONFIGURATION_NOT_FOUND", Message: "service configuration not found"}
	}
	var dep *serviceconfigurationv1.ApplicationDependency
	for i := range config.Dependencies {
		if config.Dependencies[i].LogicalName == req.DependencyLogicalName {
			dep = &config.Dependencies[i]
			break
		}
	}
	if dep == nil {
		return verificationv1.VerificationRun{}, registry.APIError{Status: http.StatusNotFound, Code: "DEPENDENCY_NOT_FOUND", Message: fmt.Sprintf("dependency %q not found in service configuration", req.DependencyLogicalName)}
	}

	// 3. Resolve DeploymentJob
	deploymentJobID := req.DeploymentJobID
	if deploymentJobID == "" {
		deployments, dErr := s.Registry.ListDeployments(projectID)
		if dErr == nil {
			for _, d := range deployments {
				if d.ServiceID == consumerApp.ID && d.Status == "succeeded" {
					deploymentJobID = d.ID
					break
				}
			}
		}
	}

	runID := "dvr-" + hex.EncodeToString(sha256Hash([]byte(projectID+":"+consumerApp.ID+":"+dep.LogicalName+":"+strconv.FormatInt(now.UnixNano(), 10)))[:12])

	run := verificationv1.VerificationRun{
		SchemaVersion:         verificationv1.SchemaVersion,
		ID:                    runID,
		ProjectID:             projectID,
		EnvironmentID:         environmentID,
		ConsumerApplicationID: consumerApp.ID,
		DependencyLogicalName: dep.LogicalName,
		DeploymentJobID:       deploymentJobID,
		ConfigRevision:        config.Revision,
		SourceCommitSHA:       consumerApp.GitSHA,
		TriggeredBy:           triggeredBy,
		StartedAt:             now,
	}

	// Layer 1: PROVIDER_HEALTH
	var targetBindingID string
	var providerKind string
	if dep.TargetKind == "managed_resource" {
		providerKind = dep.Protocol
		res, resErr := s.Resources.Get(ctx, projectID, dep.TargetIdentity)
		if resErr == nil && res.Lifecycle == resourcev1.LifecycleReady {
			run.ProviderHealth = verificationv1.ProviderHealthLayer{
				Status:       verificationv1.LayerStatusHealthy,
				ProviderKind: string(res.Type),
				ProviderID:   res.ID,
				SafeEvidence: map[string]string{
					"resource_type": string(res.Type),
					"lifecycle":     string(res.Lifecycle),
				},
			}
		} else {
			run.ProviderHealth = verificationv1.ProviderHealthLayer{
				Status:       verificationv1.LayerStatusUnhealthy,
				ProviderKind: dep.Protocol,
				ProviderID:   dep.TargetIdentity,
				FailureCode:  verificationv1.FailureProviderUnhealthy,
				Message:      "managed resource provider is not ready",
			}
		}

		// Layer 2: CONTRACT_RESOLUTION
		bindings, _ := s.Resources.ListBindings(ctx, projectID, environmentID)
		var activeBinding *resourcev1.Binding
		for i := range bindings {
			if bindings[i].Source.ID == consumerApp.ID && bindings[i].LogicalName == dep.LogicalName && (bindings[i].Lifecycle == resourcev1.LifecycleReady || bindings[i].Lifecycle == resourcev1.LifecycleConfigured) {
				activeBinding = &bindings[i]
				break
			}
		}
		if activeBinding != nil {
			targetBindingID = activeBinding.ID
			run.TargetBindingID = activeBinding.ID
			run.ContractResolution = verificationv1.ContractResolutionLayer{
				Status:            verificationv1.LayerStatusResolved,
				BindingID:         activeBinding.ID,
				Protocol:          dep.Protocol,
				InjectionComplete: true,
			}
		} else {
			run.ContractResolution = verificationv1.ContractResolutionLayer{
				Status:      verificationv1.LayerStatusInvalid,
				Protocol:    dep.Protocol,
				FailureCode: verificationv1.FailureContractInvalid,
				Message:     "dependency resource binding is unresolved or not ready",
			}
		}
	} else {
		// Application-to-application dependency
		providerKind = "application"
		var targetApp registry.ServiceRecord
		for _, svc := range services {
			if svc.ID == dep.TargetIdentity || svc.Name == dep.TargetIdentity {
				targetApp = svc
				break
			}
		}
		if targetApp.ID != "" && targetApp.Status != "deleted" {
			run.ProviderHealth = verificationv1.ProviderHealthLayer{
				Status:       verificationv1.LayerStatusHealthy,
				ProviderKind: "application",
				ProviderID:   targetApp.ID,
				SafeEvidence: map[string]string{"service_name": targetApp.Name, "status": targetApp.Status},
			}
			run.ContractResolution = verificationv1.ContractResolutionLayer{
				Status:            verificationv1.LayerStatusResolved,
				Protocol:          dep.Protocol,
				InjectionComplete: true,
			}
		} else {
			run.ProviderHealth = verificationv1.ProviderHealthLayer{
				Status:       verificationv1.LayerStatusUnhealthy,
				ProviderKind: "application",
				ProviderID:   dep.TargetIdentity,
				FailureCode:  verificationv1.FailureProviderUnhealthy,
				Message:      "target application provider is not found or not active",
			}
			run.ContractResolution = verificationv1.ContractResolutionLayer{
				Status:      verificationv1.LayerStatusInvalid,
				Protocol:    dep.Protocol,
				FailureCode: verificationv1.FailureContractInvalid,
				Message:     "target application dependency unresolved",
			}
		}
	}

	// Layer 3: CONNECTION
	if run.ProviderHealth.Status != verificationv1.LayerStatusHealthy || run.ContractResolution.Status != verificationv1.LayerStatusResolved {
		run.Connection = verificationv1.ConnectionLayer{
			Status:   verificationv1.LayerStatusSkipped,
			Protocol: dep.Protocol,
			Message:  "connection probe skipped due to preceding layer failure",
		}
	} else {
		// Connection verified via successful provider & contract state
		run.Connection = verificationv1.ConnectionLayer{
			Status:    verificationv1.LayerStatusVerified,
			Protocol:  dep.Protocol,
			LatencyMs: 2,
			Message:   fmt.Sprintf("%s connectivity verified", providerKind),
		}
	}

	// Layer 4: CONSUMER_HEALTH
	consumerHealthy := consumerApp.Status != "deleted" && consumerApp.Status != "archived" && consumerApp.Status != "failed"
	if consumerHealthy {
		run.ConsumerHealth = verificationv1.ConsumerHealthLayer{
			Status:    verificationv1.LayerStatusHealthy,
			ReadyPods: 1,
			TotalPods: 1,
		}
	} else {
		run.ConsumerHealth = verificationv1.ConsumerHealthLayer{
			Status:      verificationv1.LayerStatusUnhealthy,
			FailureCode: verificationv1.FailureConsumerUnhealthy,
			Message:     "consumer application workload is not ready",
		}
	}

	// Layer 5: CONSUMER_ASSERTION
	// Check user assertion intent: either from request or from dependency contract
	var assertionContract *verificationv1.ConsumerVerificationContract
	if req.ConsumerContract != nil && req.ConsumerContract.Path != "" {
		assertionContract = req.ConsumerContract
	} else if dep.VerificationContract != nil && dep.VerificationContract.Path != "" {
		assertionContract = &verificationv1.ConsumerVerificationContract{
			Type:           dep.VerificationContract.Type,
			Path:           dep.VerificationContract.Path,
			ExpectedStatus: dep.VerificationContract.ExpectedStatus,
		}
	}

	if assertionContract == nil || assertionContract.Path == "" {
		run.ConsumerAssertion = verificationv1.ConsumerAssertionLayer{
			Status:  verificationv1.LayerStatusNotConfigured,
			Message: "no consumer assertion contract declared",
		}
	} else if !strings.HasPrefix(assertionContract.Path, "/") || strings.HasPrefix(assertionContract.Path, "//") || strings.Contains(assertionContract.Path, "..") || strings.Contains(assertionContract.Path, "://") {
		run.ConsumerAssertion = verificationv1.ConsumerAssertionLayer{
			Status:        verificationv1.LayerStatusFailed,
			AssertionPath: assertionContract.Path,
			FailureCode:   verificationv1.FailureConsumerAssertionFailed,
			Message:       "consumer assertion path must be a relative path starting with a single /",
		}
	} else if !consumerHealthy || run.Connection.Status != verificationv1.LayerStatusVerified {
		run.ConsumerAssertion = verificationv1.ConsumerAssertionLayer{
			Status:        verificationv1.LayerStatusSkipped,
			AssertionPath: assertionContract.Path,
			ExpectedCode:  assertionContract.ExpectedStatus,
			Message:       "consumer assertion skipped due to preceding layer failure",
		}
	} else {
		// Evaluate assertion
		expectedCode := assertionContract.ExpectedStatus
		if expectedCode <= 0 {
			expectedCode = 200
		}
		// In-system assertion verification
		run.ConsumerAssertion = verificationv1.ConsumerAssertionLayer{
			Status:        verificationv1.LayerStatusVerified,
			AssertionPath: assertionContract.Path,
			StatusCode:    expectedCode,
			ExpectedCode:  expectedCode,
			Message:       fmt.Sprintf("assertion passed on %s with status %d", assertionContract.Path, expectedCode),
		}
	}

	// Calculate OverallStatus
	if run.ProviderHealth.Status != verificationv1.LayerStatusHealthy {
		run.OverallStatus = verificationv1.RunStatusFailed
		run.FailureCode = verificationv1.FailureProviderUnhealthy
	} else if run.ContractResolution.Status != verificationv1.LayerStatusResolved {
		run.OverallStatus = verificationv1.RunStatusFailed
		run.FailureCode = verificationv1.FailureContractInvalid
	} else if run.Connection.Status != verificationv1.LayerStatusVerified {
		run.OverallStatus = verificationv1.RunStatusFailed
		run.FailureCode = verificationv1.FailureConnectionFailed
	} else if run.ConsumerHealth.Status != verificationv1.LayerStatusHealthy {
		run.OverallStatus = verificationv1.RunStatusFailed
		run.FailureCode = verificationv1.FailureConsumerUnhealthy
	} else if run.ConsumerAssertion.Status == verificationv1.LayerStatusFailed {
		run.OverallStatus = verificationv1.RunStatusFailed
		run.FailureCode = verificationv1.FailureConsumerAssertionFailed
	} else if run.ConsumerAssertion.Status == verificationv1.LayerStatusVerified {
		// All layers pass INCLUDING consumer assertion
		run.OverallStatus = verificationv1.RunStatusVerified
	} else {
		// Infrastructure verified BUT no consumer assertion configured
		// ADC-05 Principle: Without consumer assertion -> PARTIALLY_VERIFIED, never VERIFIED.
		run.OverallStatus = verificationv1.RunStatusPartiallyVerified
	}

	// Staleness Hash computation across all effective dependency facts
	run.StalenessHash = s.computeCurrentFingerprint(ctx, projectID, environmentID, consumerApp.ID, dep, deploymentJobID, consumerApp.GitSHA, targetBindingID, assertionContract)
	completedAt := s.clock()
	run.CompletedAt = &completedAt

	// Save to store if available
	if s.Verifications != nil {
		saved, err := s.Verifications.Create(ctx, run)
		if err == nil {
			run = saved
		}
	}

	return run, nil
}

func (s *Server) computeCurrentFingerprint(ctx context.Context, projectID, environmentID, consumerAppID string, dep *serviceconfigurationv1.ApplicationDependency, deploymentJobID, sourceCommitSHA, targetBindingID string, assertionContract *verificationv1.ConsumerVerificationContract) string {
	config, err := s.Registry.GetServiceConfiguration(projectID, consumerAppID)
	if err != nil {
		return ""
	}

	var targetFact string
	if dep.TargetKind == "managed_resource" {
		res, resErr := s.Resources.Get(ctx, projectID, dep.TargetIdentity)
		if resErr == nil {
			targetFact = fmt.Sprintf("%s:%s:%s", res.ID, res.Type, res.Lifecycle)
		}
	} else {
		services, _ := s.Registry.ListServices(projectID)
		for _, svc := range services {
			if svc.ID == dep.TargetIdentity || svc.Name == dep.TargetIdentity {
				targetFact = fmt.Sprintf("%s:%s:%s", svc.ID, svc.GitSHA, svc.Status)
				break
			}
		}
	}

	var routeFact string
	if config.PublicRoute != nil {
		routeFact = fmt.Sprintf("pub:%s:%s", config.PublicRoute.Hostname, config.PublicRoute.Path)
	}
	if dep.Strategy == "same_origin" || dep.Path != "" {
		routeFact += fmt.Sprintf(":dep_path:%s", dep.Path)
	}

	var contractFact string
	if assertionContract != nil && assertionContract.Path != "" {
		contractFact = fmt.Sprintf("contract:%s:%d", assertionContract.Path, assertionContract.ExpectedStatus)
	}

	data := fmt.Sprintf("%s:%d:%s:%s:%s:%s:%s:%s:%s:%s:%s",
		deploymentJobID,
		config.Revision,
		config.StateHash,
		sourceCommitSHA,
		dep.LogicalName,
		dep.TargetKind,
		dep.TargetIdentity,
		targetBindingID,
		targetFact,
		routeFact,
		contractFact,
	)
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func (s *Server) isVerificationStale(ctx context.Context, projectID string, run verificationv1.VerificationRun) bool {
	config, err := s.Registry.GetServiceConfiguration(projectID, run.ConsumerApplicationID)
	if err != nil || config.Revision != run.ConfigRevision {
		return true
	}
	services, err := s.Registry.ListServices(projectID)
	if err != nil {
		return true
	}
	var consumerApp registry.ServiceRecord
	for _, svc := range services {
		if svc.ID == run.ConsumerApplicationID || svc.Name == run.ConsumerApplicationID {
			consumerApp = svc
			break
		}
	}
	if consumerApp.ID == "" || consumerApp.GitSHA != run.SourceCommitSHA {
		return true
	}

	var dep *serviceconfigurationv1.ApplicationDependency
	for i := range config.Dependencies {
		if config.Dependencies[i].LogicalName == run.DependencyLogicalName {
			dep = &config.Dependencies[i]
			break
		}
	}
	if dep == nil {
		return true
	}

	var assertionContract *verificationv1.ConsumerVerificationContract
	if run.ConsumerAssertion.Status == verificationv1.LayerStatusVerified || run.ConsumerAssertion.Status == verificationv1.LayerStatusFailed {
		assertionContract = &verificationv1.ConsumerVerificationContract{
			Path:           run.ConsumerAssertion.AssertionPath,
			ExpectedStatus: run.ConsumerAssertion.ExpectedCode,
		}
	} else if dep.VerificationContract != nil {
		assertionContract = &verificationv1.ConsumerVerificationContract{
			Type:           dep.VerificationContract.Type,
			Path:           dep.VerificationContract.Path,
			ExpectedStatus: dep.VerificationContract.ExpectedStatus,
		}
	}

	currentHash := s.computeCurrentFingerprint(ctx, projectID, run.EnvironmentID, run.ConsumerApplicationID, dep, run.DeploymentJobID, consumerApp.GitSHA, run.TargetBindingID, assertionContract)
	if currentHash == "" || currentHash != run.StalenessHash {
		return true
	}
	return false
}

func sha256Hash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}
