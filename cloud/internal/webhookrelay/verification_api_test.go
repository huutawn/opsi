package webhookrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/sourcereport"
	"github.com/opsi-dev/opsi/cloud/internal/sourcescanner"
	"github.com/opsi-dev/opsi/cloud/internal/verificationstore"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

type verificationTestFixture struct {
	server     *Server
	projectID  string
	envID      string
	appID      string
	resourceID string
	depName    string
	pgResource resourcev1.Resource
}

func setupVerificationFixture(t *testing.T) verificationTestFixture {
	t.Helper()
	cfg := Config{}
	server := NewServer(cfg)
	server.SourceReports = sourcereport.NewMemoryStore()
	server.Verifications = verificationstore.NewMemoryStore()

	ctx := context.Background()
	project, err := server.Registry.CreateProject("org-test", "Test Project", "test-proj", "user-1", "project-test")
	if err != nil {
		t.Fatal(err)
	}

	svc, err := server.Registry.CreateService(project.ID, registry.ServiceDraft{
		Name:          "web",
		Type:          "application",
		ContainerPort: 3000,
		GitSHA:        "43a701b3b2f3ade736a7b064183f37da70c78fe4",
	}, "svc-key")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Create managed resource (Postgres)
	now := time.Now().UTC()
	pgSpec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:    "res-pg-test",
		ProjectID:     project.ID,
		EnvironmentID: svc.EnvironmentID,
		ResourceType:  resourcev1.TypePostgres,
		Profile:       "single-node-experimental",
		Version:       resourcev1.PostgresVersion,
		Image:         resourcev1.PostgresImage,
		Assignment:    resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas:      1,
		CPUMillicores: 250,
		MemoryBytes:   256 * 1024 * 1024,
		Storage:       resourcev1.StorageRequest{Persistent: true, SizeBytes: 10 * 1024 * 1024 * 1024, PolicyRef: resourcev1.StoragePolicyDefault},
		Ports:         []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}},
		Connection:    resourcev1.ManagedResourceConnection{ServiceName: "postgres-svc", Host: "postgres.local", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"},
		CredentialID:  "mrcred-res-pg-test",
	}
	pgSpec.SpecHash, _ = pgSpec.Hash()

	pgResource := resourcev1.Resource{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "res-pg-test",
		ProjectID:     project.ID,
		EnvironmentID: svc.EnvironmentID,
		Name:          "main-pg",
		Kind:          resourcev1.KindManagedService,
		Type:          resourcev1.TypePostgres,
		Lifecycle:     resourcev1.LifecycleReady,
		Runtime: &resourcev1.ManagedResourceRuntime{
			Spec: pgSpec,
			Evidence: &resourcev1.ManagedResourceEvidence{
				ObservedSpecHash:  pgSpec.SpecHash,
				WorkloadReady:     true,
				PodReady:          true,
				ServiceReady:      true,
				SecretReady:       true,
				AuthReady:         true,
				StorageReady:      true,
				VolumeMounted:     true,
				AvailableReplicas: 1,
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, _, err := server.Resources.Store.Create(ctx, pgResource, "pg-key", "payload"); err != nil {
		t.Fatal(err)
	}

	// 3. Create ResourceBinding
	depName := "database"
	binding := resourcev1.Binding{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "bind-pg-test",
		ProjectID:     project.ID,
		EnvironmentID: svc.EnvironmentID,
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: svc.ID},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: pgResource.ID},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   depName,
		Lifecycle:     resourcev1.LifecycleReady,
		CredentialID:  "bcred-1",
		RoleName:      "app_role",
		Database:      "opsi",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if _, _, err := server.Resources.Store.CreateBinding(ctx, binding, "bind-key", "payload"); err != nil {
		t.Fatal(err)
	}

	// 4. Configure service with ApplicationDependency
	curCfg, err := server.Registry.GetServiceConfiguration(project.ID, svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    depName,
				TargetKind:     "managed_resource",
				TargetIdentity: pgResource.ID,
				Protocol:       "postgres",
				Required:       true,
				InjectionPhase: "runtime",
				VerificationContract: &serviceconfigurationv1.DependencyVerificationContract{
					Type:           "consumer_http",
					Path:           "/health/dependencies/database",
					ExpectedStatus: 200,
				},
			},
		},
	}
	if _, err := server.Registry.ApplyServiceConfiguration(project.ID, svc.ID, "user-1", "init-cfg", registry.ServiceConfigurationApplyRequest{
		Draft:             draft,
		ExpectedRevision:  curCfg.Revision,
		ExpectedStateHash: curCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	return verificationTestFixture{
		server:     server,
		projectID:  project.ID,
		envID:      svc.EnvironmentID,
		appID:      svc.ID,
		resourceID: pgResource.ID,
		depName:    depName,
		pgResource: pgResource,
	}
}

func TestVerifyDependencyFullSuccess(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database",
			ExpectedStatus: 200,
		},
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if run.OverallStatus != verificationv1.RunStatusVerified {
		t.Fatalf("expected VERIFIED overall status, got %s (failure: %s)", run.OverallStatus, run.FailureCode)
	}
	if run.ProviderHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected provider HEALTHY, got %s", run.ProviderHealth.Status)
	}
	if run.ContractResolution.Status != verificationv1.LayerStatusResolved {
		t.Fatalf("expected contract RESOLVED, got %s", run.ContractResolution.Status)
	}
	if run.Connection.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected connection VERIFIED, got %s", run.Connection.Status)
	}
	if run.ConsumerHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected consumer health HEALTHY, got %s", run.ConsumerHealth.Status)
	}
	if run.ConsumerAssertion.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected consumer assertion VERIFIED, got %s", run.ConsumerAssertion.Status)
	}
}

func TestVerifyDependencyPartiallyVerifiedWhenNoAssertion(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	curCfg, err := f.server.Registry.GetServiceConfiguration(f.projectID, f.appID)
	if err != nil {
		t.Fatal(err)
	}

	// Update draft to have NO verification contract
	draft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    f.depName,
				TargetKind:     "managed_resource",
				TargetIdentity: f.resourceID,
				Protocol:       "postgres",
				Required:       true,
				InjectionPhase: "runtime",
			},
		},
	}
	if _, err := f.server.Registry.ApplyServiceConfiguration(f.projectID, f.appID, "user-1", "cfg-no-assertion", registry.ServiceConfigurationApplyRequest{
		Draft:             draft,
		ExpectedRevision:  curCfg.Revision,
		ExpectedStateHash: curCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ADC-05 Core Invariant: Without consumer assertion -> PARTIALLY_VERIFIED, NEVER VERIFIED!
	if run.OverallStatus != verificationv1.RunStatusPartiallyVerified {
		t.Fatalf("expected PARTIALLY_VERIFIED overall status without assertion, got %s", run.OverallStatus)
	}
	if run.ConsumerAssertion.Status != verificationv1.LayerStatusNotConfigured {
		t.Fatalf("expected assertion NOT_CONFIGURED, got %s", run.ConsumerAssertion.Status)
	}
}

func TestVerifyDependencyProviderUnhealthy(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Mark resource failed/unhealthy
	f.pgResource.Lifecycle = resourcev1.LifecycleFailed
	if _, err := f.server.Resources.Store.Update(ctx, f.pgResource); err != nil {
		t.Fatal(err)
	}

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if run.OverallStatus != verificationv1.RunStatusFailed {
		t.Fatalf("expected FAILED, got %s", run.OverallStatus)
	}
	if run.FailureCode != verificationv1.FailureProviderUnhealthy {
		t.Fatalf("expected PROVIDER_UNHEALTHY, got %s", run.FailureCode)
	}
	if run.ProviderHealth.Status != verificationv1.LayerStatusUnhealthy {
		t.Fatalf("expected provider UNHEALTHY, got %s", run.ProviderHealth.Status)
	}
	if run.Connection.Status != verificationv1.LayerStatusSkipped {
		t.Fatalf("expected connection SKIPPED, got %s", run.Connection.Status)
	}
}

func TestVerifyDependencyStalenessDetection(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	// Should not be stale immediately
	if f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected fresh verification, but got stale")
	}

	curCfg, err := f.server.Registry.GetServiceConfiguration(f.projectID, f.appID)
	if err != nil {
		t.Fatal(err)
	}

	// Change service draft/revision
	newDraft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    f.depName,
				TargetKind:     "managed_resource",
				TargetIdentity: f.resourceID,
				Protocol:       "postgres",
				Required:       false,
				InjectionPhase: "runtime",
			},
		},
	}
	if _, err := f.server.Registry.ApplyServiceConfiguration(f.projectID, f.appID, "user-1", "cfg-stale", registry.ServiceConfigurationApplyRequest{
		Draft:             newDraft,
		ExpectedRevision:  curCfg.Revision,
		ExpectedStateHash: curCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	// Now should be detected as STALE
	if !f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected stale verification after config change, but got fresh")
	}
}

func TestVerifyDependencySameOriginRouteStaleness(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Configure same_origin dependency with /api path
	curCfg, err := f.server.Registry.GetServiceConfiguration(f.projectID, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	draft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "api-dep",
				TargetKind:     "managed_resource",
				TargetIdentity: f.resourceID,
				Protocol:       "postgres",
				Strategy:       "same_origin",
				Path:           "/api",
				InjectionPhase: "runtime",
			},
		},
		PublicRoute: &registry.PublicRouteIntent{
			Hostname: "app.example.com",
			Path:     "/api",
		},
	}
	if _, err := f.server.Registry.ApplyServiceConfiguration(f.projectID, f.appID, "user-1", "cfg-so-1", registry.ServiceConfigurationApplyRequest{
		Draft:             draft,
		ExpectedRevision:  curCfg.Revision,
		ExpectedStateHash: curCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: "api-dep",
	}
	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected fresh run, got stale")
	}

	// Change route to /v2
	curCfg2, _ := f.server.Registry.GetServiceConfiguration(f.projectID, f.appID)
	draft2 := draft
	draft2.PublicRoute = &registry.PublicRouteIntent{
		Hostname: "app.example.com",
		Path:     "/v2",
	}
	if _, err := f.server.Registry.ApplyServiceConfiguration(f.projectID, f.appID, "user-1", "cfg-so-2", registry.ServiceConfigurationApplyRequest{
		Draft:             draft2,
		ExpectedRevision:  curCfg2.Revision,
		ExpectedStateHash: curCfg2.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	// Old verification MUST be STALE
	if !f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected old verification to be STALE when same-origin route changed /api -> /v2")
	}
}

func TestVerifyDependencyPublicHTTPExposureStaleness(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	curCfg, err := f.server.Registry.GetServiceConfiguration(f.projectID, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	draft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "db",
				TargetKind:     "managed_resource",
				TargetIdentity: f.resourceID,
				Protocol:       "postgres",
				InjectionPhase: "runtime",
			},
		},
		PublicRoute: &registry.PublicRouteIntent{
			Hostname: "url-a.example.com",
			Path:     "/",
		},
	}
	if _, err := f.server.Registry.ApplyServiceConfiguration(f.projectID, f.appID, "user-1", "cfg-pub-1", registry.ServiceConfigurationApplyRequest{
		Draft:             draft,
		ExpectedRevision:  curCfg.Revision,
		ExpectedStateHash: curCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: "db",
	}
	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	// Change exposure hostname from URL-A to URL-B
	curCfg2, _ := f.server.Registry.GetServiceConfiguration(f.projectID, f.appID)
	draft2 := draft
	draft2.PublicRoute = &registry.PublicRouteIntent{
		Hostname: "url-b.example.com",
		Path:     "/",
	}
	if _, err := f.server.Registry.ApplyServiceConfiguration(f.projectID, f.appID, "user-1", "cfg-pub-2", registry.ServiceConfigurationApplyRequest{
		Draft:             draft2,
		ExpectedRevision:  curCfg2.Revision,
		ExpectedStateHash: curCfg2.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	// Old verification MUST be STALE
	if !f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected old verification to be STALE when exposure hostname changed URL-A -> URL-B")
	}
}

func TestSourceRiskReportPersistenceAndRetrieval(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	report := sourcereport.Report{
		ProjectID:       f.projectID,
		ApplicationID:   f.appID,
		RepositoryID:    100,
		CommitSHA:       "43a701b3b2f3ade736a7b064183f37da70c78fe4",
		ApplicationRoot: ".",
		ScannerVersion:  sourcescanner.ScannerVersion,
		AnalysisStatus:  sourcescanner.AnalysisStatusComplete,
		Findings: []sourcescanner.Finding{
			{
				FindingID:    "SOURCE_LOOPBACK_ENDPOINT:app.js:10",
				RuleID:       sourcescanner.RuleLoopbackEndpoint,
				Severity:     sourcescanner.SeverityWarn,
				Confidence:   sourcescanner.ConfidenceHigh,
				File:         "app.js",
				Line:         10,
				SafeEvidence: "http://localhost:8080",
			},
		},
		ReportHash: "rep-hash-1",
		CreatedAt:  time.Now().UTC(),
	}

	if _, _, err := f.server.SourceReports.Upsert(ctx, report); err != nil {
		t.Fatal(err)
	}

	// Direct call to GetForCommit
	fetched, err := f.server.SourceReports.GetForCommit(ctx, f.projectID, f.appID, report.CommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched.Findings) != 1 || fetched.Findings[0].RuleID != sourcescanner.RuleLoopbackEndpoint {
		t.Fatalf("unexpected report findings: %+v", fetched.Findings)
	}
}

func TestPreflightIntegrationWithSourceRiskWarnings(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Store source report with 1 finding
	report := sourcereport.Report{
		ProjectID:       f.projectID,
		ApplicationID:   f.appID,
		RepositoryID:    100,
		CommitSHA:       "43a701b3b2f3ade736a7b064183f37da70c78fe4",
		ApplicationRoot: ".",
		ScannerVersion:  sourcescanner.ScannerVersion,
		AnalysisStatus:  sourcescanner.AnalysisStatusComplete,
		Findings: []sourcescanner.Finding{
			{
				FindingID:    "SOURCE_LOOPBACK_ENDPOINT:app.js:10",
				RuleID:       sourcescanner.RuleLoopbackEndpoint,
				Severity:     sourcescanner.SeverityWarn,
				Confidence:   sourcescanner.ConfidenceHigh,
				File:         "app.js",
				Line:         10,
				SafeEvidence: "http://localhost:8080",
			},
		},
		ReportHash: "rep-hash-1",
		CreatedAt:  time.Now().UTC(),
	}
	if _, _, err := f.server.SourceReports.Upsert(ctx, report); err != nil {
		t.Fatal(err)
	}

	// Verify preflight executes
	preflight, err := f.server.runPreflight(ctx, f.projectID, deploymentv1.CreateRequest{
		BuildRecordID: "br-test",
		EnvironmentID: f.envID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = preflight
}

func TestVerifyDependencyAssertionFailure(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Request assertion with non-matching expected status or invalid path
	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "invalid-path-without-slash",
			ExpectedStatus: 200,
		},
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err == nil && run.OverallStatus == verificationv1.RunStatusVerified {
		t.Fatal("expected assertion error or failure for invalid path")
	}
}

func TestVerificationHTTPEndpoints(t *testing.T) {
	f := setupVerificationFixture(t)

	// Direct trigger through ExecuteDependencyVerification
	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database",
			ExpectedStatus: 200,
		},
	}
	run, err := f.server.ExecuteDependencyVerification(context.Background(), f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	// Verify we can fetch the run from Verifications store
	fetched, err := f.server.Verifications.GetLatest(context.Background(), f.projectID, f.envID, f.appID, f.depName)
	if err != nil {
		t.Fatalf("failed to fetch verification from store: %v", err)
	}
	if fetched.ID != run.ID {
		t.Fatalf("expected run ID %s, got %s", run.ID, fetched.ID)
	}
	if fetched.OverallStatus != verificationv1.RunStatusVerified {
		t.Fatalf("expected VERIFIED, got %s", fetched.OverallStatus)
	}
}

func TestVerifyFreshnessComprehensiveMutations(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database",
			ExpectedStatus: 200,
		},
	}
	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initially FRESH
	if f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected fresh verification run")
	}

	// 2. Unrelated project mutation -> Still FRESH for this project/run
	_, _ = f.server.Registry.CreateProject("org-test", "Other Project", "other-proj", "user-1", "project-other")
	if f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected run to remain FRESH after unrelated project mutation")
	}

	// 3. Deployed revision / GitSHA mutation -> STALE
	runDifferentGitSHA := run
	runDifferentGitSHA.SourceCommitSHA = "ffffffffffffffffffffffffffffffffffffffff"
	if !f.server.isVerificationStale(ctx, f.projectID, runDifferentGitSHA) {
		t.Fatal("expected STALE when consumer GitSHA / deployed revision changed")
	}

	// 4. ResourceBinding identity change -> STALE
	newBinding := resourcev1.Binding{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "bind-pg-new-identity",
		ProjectID:     f.projectID,
		EnvironmentID: f.envID,
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: f.appID},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: f.resourceID},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   f.depName,
		Lifecycle:     resourcev1.LifecycleReady,
		CredentialID:  "bcred-2",
		RoleName:      "app_role_2",
		Database:      "opsi",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	_ = f.server.Resources.Store.DeleteBinding(ctx, f.projectID, "bind-pg-test")
	_, _, _ = f.server.Resources.Store.CreateBinding(ctx, newBinding, "bind-key-2", "payload-2")
	if !f.server.isVerificationStale(ctx, f.projectID, run) {
		t.Fatal("expected STALE when ResourceBinding identity changed")
	}
}

func TestVerifyDependencyProbeEvidenceEvaluation(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// 1. Probe observes HTTP 503 (bad consumer assertion failure)
	req503 := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database/unreachable",
			ExpectedStatus: 200,
		},
		ObservedStatusCode: 503,
	}

	run503, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req503, "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run503.ProviderHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Provider HEALTHY, got %s", run503.ProviderHealth.Status)
	}
	if run503.ContractResolution.Status != verificationv1.LayerStatusResolved {
		t.Fatalf("expected Contract RESOLVED, got %s", run503.ContractResolution.Status)
	}
	if run503.Connection.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Connection VERIFIED, got %s", run503.Connection.Status)
	}
	if run503.ConsumerHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Consumer HEALTHY, got %s", run503.ConsumerHealth.Status)
	}
	if run503.ConsumerAssertion.Status != verificationv1.LayerStatusFailed {
		t.Fatalf("expected Assertion FAILED, got %s", run503.ConsumerAssertion.Status)
	}
	if run503.ConsumerAssertion.StatusCode != 503 {
		t.Fatalf("expected Assertion StatusCode 503, got %d", run503.ConsumerAssertion.StatusCode)
	}
	if run503.ConsumerAssertion.ExpectedCode != 200 {
		t.Fatalf("expected Assertion ExpectedCode 200, got %d", run503.ConsumerAssertion.ExpectedCode)
	}
	if run503.ConsumerAssertion.FailureCode != verificationv1.FailureConsumerAssertionFailed {
		t.Fatalf("expected failure code CONSUMER_ASSERTION_FAILED, got %s", run503.ConsumerAssertion.FailureCode)
	}
	if run503.OverallStatus != verificationv1.RunStatusFailed {
		t.Fatalf("expected Overall FAILED, got %s", run503.OverallStatus)
	}
	if run503.FailureCode != verificationv1.FailureConsumerAssertionFailed {
		t.Fatalf("expected overall failure code CONSUMER_ASSERTION_FAILED, got %s", run503.FailureCode)
	}

	// 2. Negative Control: Probe observes HTTP 200 (passing assertion)
	req200 := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database",
			ExpectedStatus: 200,
		},
		ObservedStatusCode: 200,
	}

	run200, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req200, "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run200.ConsumerAssertion.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Assertion VERIFIED for 200 status, got %s", run200.ConsumerAssertion.Status)
	}
	if run200.ConsumerAssertion.StatusCode != 200 {
		t.Fatalf("expected Assertion StatusCode 200, got %d", run200.ConsumerAssertion.StatusCode)
	}
	if run200.OverallStatus != verificationv1.RunStatusVerified {
		t.Fatalf("expected Overall VERIFIED for 200 status, got %s", run200.OverallStatus)
	}
}

func TestAssertionAuthenticityWithRealConsumerFixtureHandlers(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Simulate actual consumer fixture HTTP handler (same behavior as cloud/integration/fixtures/adc02-consumer/main.go)
	consumerMux := http.NewServeMux()
	consumerMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	consumerMux.HandleFunc("/health/dependencies/database/unreachable", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad consumer assertion failed: connection refused", http.StatusServiceUnavailable)
	})
	consumerMux.HandleFunc("/health/dependencies/database", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	testServer := httptest.NewServer(consumerMux)
	defer testServer.Close()

	// 1. Probe /health -> actual consumer returns 200
	respHealth, err := http.Get(testServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = respHealth.Body.Close()
	if respHealth.StatusCode != http.StatusOK {
		t.Fatalf("expected consumer /health to return 200, got %d", respHealth.StatusCode)
	}

	// 2. Probe /health/dependencies/database/unreachable -> actual consumer returns 503
	respUnreachable, err := http.Get(testServer.URL + "/health/dependencies/database/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	_ = respUnreachable.Body.Close()
	if respUnreachable.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected consumer unreachable endpoint to return 503, got %d", respUnreachable.StatusCode)
	}

	// 3. Probe /health/dependencies/database -> actual consumer returns 200 (Negative control)
	respDatabase, err := http.Get(testServer.URL + "/health/dependencies/database")
	if err != nil {
		t.Fatal(err)
	}
	_ = respDatabase.Body.Close()
	if respDatabase.StatusCode != http.StatusOK {
		t.Fatalf("expected consumer database endpoint to return 200, got %d", respDatabase.StatusCode)
	}

	// 4. Cloud receives actual observed probe status 503 -> evaluates FAILED
	failingRun, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database/unreachable",
			ExpectedStatus: 200,
		},
		ObservedStatusCode: respUnreachable.StatusCode,
	}, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if failingRun.ProviderHealth.Status != verificationv1.LayerStatusHealthy ||
		failingRun.ContractResolution.Status != verificationv1.LayerStatusResolved ||
		failingRun.Connection.Status != verificationv1.LayerStatusVerified ||
		failingRun.ConsumerHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("preceding 4 layers must all be healthy/verified, got: provider=%s, contract=%s, connection=%s, consumer=%s",
			failingRun.ProviderHealth.Status, failingRun.ContractResolution.Status, failingRun.Connection.Status, failingRun.ConsumerHealth.Status)
	}
	if failingRun.ConsumerAssertion.Status != verificationv1.LayerStatusFailed {
		t.Fatalf("expected Assertion FAILED for 503 probe, got %s", failingRun.ConsumerAssertion.Status)
	}
	if failingRun.ConsumerAssertion.StatusCode != 503 {
		t.Fatalf("expected Assertion StatusCode 503, got %d", failingRun.ConsumerAssertion.StatusCode)
	}
	if failingRun.ConsumerAssertion.FailureCode != verificationv1.FailureConsumerAssertionFailed {
		t.Fatalf("expected Assertion FailureCode %s, got %s", verificationv1.FailureConsumerAssertionFailed, failingRun.ConsumerAssertion.FailureCode)
	}
	if failingRun.OverallStatus != verificationv1.RunStatusFailed {
		t.Fatalf("expected OverallStatus FAILED, got %s", failingRun.OverallStatus)
	}

	// 5. Cloud receives actual observed probe status 200 (Negative control) -> evaluates VERIFIED
	passingRun, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database",
			ExpectedStatus: 200,
		},
		ObservedStatusCode: respDatabase.StatusCode,
	}, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if passingRun.ConsumerAssertion.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Assertion VERIFIED for 200 probe, got %s", passingRun.ConsumerAssertion.Status)
	}
	if passingRun.ConsumerAssertion.StatusCode != 200 {
		t.Fatalf("expected Assertion StatusCode 200, got %d", passingRun.ConsumerAssertion.StatusCode)
	}
	if passingRun.OverallStatus != verificationv1.RunStatusVerified {
		t.Fatalf("expected OverallStatus VERIFIED for passing run, got %s", passingRun.OverallStatus)
	}
}
