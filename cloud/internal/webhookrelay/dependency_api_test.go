package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func TestServiceDependenciesAPI_ReviewAndApply(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-dep", "Dependency Project", "dep-proj", "user-1", "project-dep")
	if err != nil {
		t.Fatal(err)
	}
	app, err := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "web", ContainerPort: 3000}, "app-key")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// Create PostgreSQL resource
	pgSpec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:    "res-pg-api",
		ProjectID:     project.ID,
		EnvironmentID: app.EnvironmentID,
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
		CredentialID:  "mrcred-res-pg-api",
		TopologyRevision: 1,
	}
	pgSpec.ConfigurationHash = strings.Repeat("a", 64)
	pgSpec.TopologyHash = strings.Repeat("b", 64)
	pgSpec.SpecHash, _ = pgSpec.Hash()

	pgResource := resourcev1.Resource{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "res-pg-api",
		ProjectID:     project.ID,
		EnvironmentID: app.EnvironmentID,
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
				PVCName:           "pvc-1",
				PVName:            "pv-1",
				Image:             pgSpec.Image,
				ImageID:           pgSpec.Image,
				AvailableReplicas: 1,
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, _, err := server.Resources.Store.Create(context.Background(), pgResource, "pg-key", "payload"); err != nil {
		t.Fatal(err)
	}

	// 1. Configure application dependency draft
	cfg, err := server.Registry.GetServiceConfiguration(project.ID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "database",
				TargetKind:     "managed_resource",
				TargetIdentity: "res-pg-api",
				Protocol:       "postgres",
				Required:       true,
				InjectionPhase: "runtime",
				InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
					{EnvName: "APP_DATABASE_URL", SymbolicSource: "connection.url"},
					{EnvName: "DB_HOST", SymbolicSource: "resource.host"},
					{EnvName: "DB_PORT", SymbolicSource: "resource.port"},
					{EnvName: "DB_NAME", SymbolicSource: "credential.database"},
					{EnvName: "DB_USER", SymbolicSource: "credential.username"},
					{EnvName: "DB_PASSWORD", SymbolicSource: "credential.password"},
				},
			},
		},
	}

	// Apply configuration draft
	applyCfg := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+app.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{Draft: draft, ExpectedRevision: cfg.Revision, ExpectedStateHash: cfg.StateHash}, "cfg-1")
	if applyCfg.Code != http.StatusOK {
		t.Fatalf("apply config failed: %s", applyCfg.Body.String())
	}

	// 2. Review Dependencies Endpoint (Zero-mutation)
	reviewResp := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+app.ID+"/dependencies/review", nil, "")
	if reviewResp.Code != http.StatusOK {
		t.Fatalf("review dependencies failed: status=%d body=%s", reviewResp.Code, reviewResp.Body.String())
	}
	var reviewResult registry.DependencyReviewResult
	if err := json.Unmarshal(reviewResp.Body.Bytes(), &reviewResult); err != nil {
		t.Fatal(err)
	}
	if len(reviewResult.Dependencies) != 1 {
		t.Fatalf("expected 1 dep in review, got %d", len(reviewResult.Dependencies))
	}
	if reviewResult.Dependencies[0].BindingAction != "create" || reviewResult.Dependencies[0].Status != "pending_binding" {
		t.Fatalf("expected create / pending_binding before apply, got %+v", reviewResult.Dependencies[0])
	}
	// Zero secret leak verification
	if bytes.Contains(reviewResp.Body.Bytes(), []byte("postgres://")) || bytes.Contains(reviewResp.Body.Bytes(), []byte("topsecret")) {
		t.Fatalf("secret leaked in review response: %s", reviewResp.Body.String())
	}

	// 3. Apply Dependencies Endpoint (Creates ResourceBinding)
	applyResp := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+app.ID+"/dependencies/apply", nil, "apply-dep-1")
	if applyResp.Code != http.StatusOK {
		t.Fatalf("apply dependencies failed: status=%d body=%s", applyResp.Code, applyResp.Body.String())
	}
	var applyResult registry.DependencyApplyResult
	if err := json.Unmarshal(applyResp.Body.Bytes(), &applyResult); err != nil {
		t.Fatal(err)
	}
	if len(applyResult.Realized) != 1 || applyResult.Realized[0].BindingID == "" {
		t.Fatalf("unexpected apply result: %+v", applyResult)
	}
	createdBindingID := applyResult.Realized[0].BindingID

	// 4. Complete binding lifecycle to ready
	binding, err := server.Resources.GetBinding(context.Background(), project.ID, createdBindingID)
	if err != nil {
		t.Fatal(err)
	}
	binding.Lifecycle = resourcev1.LifecycleReady
	_, _ = server.Resources.Store.UpdateBinding(context.Background(), binding)

	// 5. Re-apply Dependencies Endpoint (Idempotent reuse)
	reApplyResp := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+app.ID+"/dependencies/apply", nil, "apply-dep-2")
	if reApplyResp.Code != http.StatusOK {
		t.Fatalf("re-apply failed: status=%d body=%s", reApplyResp.Code, reApplyResp.Body.String())
	}
	var reApplyResult registry.DependencyApplyResult
	if err := json.Unmarshal(reApplyResp.Body.Bytes(), &reApplyResult); err != nil {
		t.Fatal(err)
	}
	if !reApplyResult.Reused || reApplyResult.Realized[0].BindingID != createdBindingID || reApplyResult.Realized[0].BindingAction != "reused" {
		t.Fatalf("expected reused binding %s, got %+v", createdBindingID, reApplyResult)
	}

	// 6. Test ApplicationRuntimeConfiguration projection
	envVars, secRefs, err := server.Resources.ApplicationRuntimeConfiguration(context.Background(), project.ID, app.EnvironmentID, app.ID)
	if err != nil {
		t.Fatalf("runtime config failed: %v", err)
	}
	if len(envVars) != 3 || len(secRefs) != 3 {
		t.Fatalf("unexpected env/secrets count: env=%+v sec=%+v", envVars, secRefs)
	}

	// 7. Verify materials resolution
	materials, err := server.Resources.ResolveSecretMaterials(context.Background(), project.ID, secRefs)
	if err != nil {
		t.Fatalf("materials resolution failed: %v", err)
	}
	if len(materials) != 1 || materials[0].Values["APP_DATABASE_URL"] == "" {
		t.Fatalf("unexpected materials: %+v", materials)
	}
	if !strings.HasPrefix(materials[0].Values["APP_DATABASE_URL"], "postgres://") {
		t.Fatalf("expected postgres URL in material, got %s", materials[0].Values["APP_DATABASE_URL"])
	}
}

func TestApplicationToApplicationDependencies(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-app-dep", "App Dep Project", "app-dep-proj", "user-1", "project-app-dep")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Create API backend service
	apiApp, err := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "api", ContainerPort: 8080}, "api-key")
	if err != nil {
		t.Fatal(err)
	}
	apiCfg, err := server.Registry.GetServiceConfiguration(project.ID, apiApp.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Give API service a public route
	applyAPICfg := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+apiApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			PublicRoute: &registry.PublicRouteIntent{Hostname: "app.example.com", Path: "/api"},
		},
		ExpectedRevision: apiCfg.Revision,
		ExpectedStateHash: apiCfg.StateHash,
	}, "api-cfg-1")
	if applyAPICfg.Code != http.StatusOK {
		t.Fatalf("apply API config failed: %s", applyAPICfg.Body.String())
	}

	// 2. Create Web frontend service
	webApp, err := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "web", ContainerPort: 3000}, "web-key")
	if err != nil {
		t.Fatal(err)
	}
	webCfg, err := server.Registry.GetServiceConfiguration(project.ID, webApp.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Scenario A: Same-Origin Browser Dependency
	sameOriginDraft := registry.ServiceConfigurationDraft{
		PublicRoute: &registry.PublicRouteIntent{Hostname: "app.example.com", Path: "/"},
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			serviceconfigurationv1.SameOriginPreset("api-backend", apiApp.ID, "/api", "API_PATH", true),
		},
	}
	applyWebCfg := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft: sameOriginDraft,
		ExpectedRevision: webCfg.Revision,
		ExpectedStateHash: webCfg.StateHash,
	}, "web-cfg-1")
	if applyWebCfg.Code != http.StatusOK {
		t.Fatalf("apply same-origin config failed: %s", applyWebCfg.Body.String())
	}

	// Review dependencies for Web (Zero-mutation)
	reviewWebResp := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/dependencies/review", nil, "")
	if reviewWebResp.Code != http.StatusOK {
		t.Fatalf("review web dependencies failed: status=%d body=%s", reviewWebResp.Code, reviewWebResp.Body.String())
	}
	var webReview registry.DependencyReviewResult
	if err := json.Unmarshal(reviewWebResp.Body.Bytes(), &webReview); err != nil {
		t.Fatal(err)
	}
	if len(webReview.Dependencies) != 1 || webReview.Dependencies[0].Strategy != "same_origin" || webReview.Dependencies[0].AccessContext != "browser" {
		t.Fatalf("unexpected web review: %+v", webReview)
	}
	if len(webReview.Dependencies[0].Projections) != 1 || webReview.Dependencies[0].Projections[0].ValuePreview != "/api" {
		t.Fatalf("expected projection preview '/api', got %+v", webReview.Dependencies[0].Projections)
	}

	// Scenario B: Internal HTTP Server Dependency
	webCfg2, _ := server.Registry.GetServiceConfiguration(project.ID, webApp.ID)
	internalHTTPDraft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			serviceconfigurationv1.InternalHTTPPreset("api-internal", apiApp.ID, "API", true),
		},
	}
	applyInternal := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft:             internalHTTPDraft,
		ExpectedRevision:  webCfg2.Revision,
		ExpectedStateHash: webCfg2.StateHash,
	}, "web-cfg-2")
	if applyInternal.Code != http.StatusOK {
		t.Fatalf("apply internal HTTP failed: %s", applyInternal.Body.String())
	}

	// Scenario Matrix Rejections:
	// 1. browser + internal_http -> REJECT (BROWSER_INTERNAL_HTTP_FORBIDDEN)
	webCfgCur, _ := server.Registry.GetServiceConfiguration(project.ID, webApp.ID)
	forbiddenBrowserInternal := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "bad-dep",
				TargetKind:     "application",
				TargetIdentity: apiApp.ID,
				Protocol:       "http",
				Strategy:       "internal_http",
				AccessContext:  "browser",
				InjectionPhase: "runtime",
			},
		},
	}
	resp1 := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft:             forbiddenBrowserInternal,
		ExpectedRevision:  webCfgCur.Revision,
		ExpectedStateHash: webCfgCur.StateHash,
	}, "bad-1")
	if resp1.Code != http.StatusUnprocessableEntity || !strings.Contains(resp1.Body.String(), "BROWSER_INTERNAL_HTTP_FORBIDDEN") {
		t.Fatalf("expected BROWSER_INTERNAL_HTTP_FORBIDDEN, got status=%d body=%s", resp1.Code, resp1.Body.String())
	}

	// 2. server + same_origin -> REJECT (STRATEGY_CONTEXT_MISMATCH)
	mismatchServerSameOrigin := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    "bad-dep-2",
				TargetKind:     "application",
				TargetIdentity: apiApp.ID,
				Protocol:       "http",
				Strategy:       "same_origin",
				AccessContext:  "server",
				Path:           "/api",
				InjectionPhase: "runtime",
			},
		},
	}
	resp2 := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft:             mismatchServerSameOrigin,
		ExpectedRevision:  webCfgCur.Revision,
		ExpectedStateHash: webCfgCur.StateHash,
	}, "bad-2")
	if resp2.Code != http.StatusUnprocessableEntity || !strings.Contains(resp2.Body.String(), "STRATEGY_CONTEXT_MISMATCH") {
		t.Fatalf("expected STRATEGY_CONTEXT_MISMATCH, got status=%d body=%s", resp2.Code, resp2.Body.String())
	}

	// 3. same_origin hostname mismatch -> REJECT (SAME_ORIGIN_HOSTNAME_MISMATCH)
	hostnameMismatch := registry.ServiceConfigurationDraft{
		PublicRoute: &registry.PublicRouteIntent{Hostname: "different.example.com", Path: "/"},
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			serviceconfigurationv1.SameOriginPreset("api-backend", apiApp.ID, "/api", "API_PATH", true),
		},
	}
	resp3 := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft:             hostnameMismatch,
		ExpectedRevision:  webCfgCur.Revision,
		ExpectedStateHash: webCfgCur.StateHash,
	}, "bad-3")
	if resp3.Code != http.StatusUnprocessableEntity || !strings.Contains(resp3.Body.String(), "SAME_ORIGIN_HOSTNAME_MISMATCH") {
		t.Fatalf("expected SAME_ORIGIN_HOSTNAME_MISMATCH, got status=%d body=%s", resp3.Code, resp3.Body.String())
	}
}

func TestApplicationToApplicationScenarioCPublicHTTPBuildTimeFreshness(t *testing.T) {
	server, projectID, webApp, plan, _ := deploymentResolutionFixture(t)

	// 1. Create API backend service with initial public route URL-A
	apiApp, err := server.Registry.CreateService(projectID, registry.ServiceDraft{Name: "api", ContainerPort: 8080}, "api-key")
	if err != nil {
		t.Fatal(err)
	}
	apiCfg, _ := server.Registry.GetServiceConfiguration(projectID, apiApp.ID)
	if _, err := server.Registry.ApplyServiceConfiguration(projectID, apiApp.ID, "owner", "api-cfg-1", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			PublicRoute: &registry.PublicRouteIntent{Hostname: "api-a.example.com", Path: "/api"},
		},
		ExpectedRevision:  apiCfg.Revision,
		ExpectedStateHash: apiCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Configure Web consumer service with build-phase public_http dependency
	webCfg, _ := server.Registry.GetServiceConfiguration(projectID, webApp.ID)
	publicDep := serviceconfigurationv1.PublicHTTPPreset("api-public", apiApp.ID, serviceconfigurationv1.AccessContextServer, "PUBLIC_API", false)
	publicDep.InjectionPhase = serviceconfigurationv1.InjectionPhaseBuild
	appliedWebCfg, err := server.Registry.ApplyServiceConfiguration(projectID, webApp.ID, "owner", "web-cfg-1", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{publicDep},
		},
		ExpectedRevision:  webCfg.Revision,
		ExpectedStateHash: webCfg.StateHash,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 3. Review Web dependencies: zero ResourceBindings needed
	reviewResp := configurationRequest(t, server, http.MethodPost, "/api/projects/"+projectID+"/services/"+webApp.ID+"/dependencies/review", nil, "")
	if reviewResp.Code != http.StatusOK {
		t.Fatalf("review failed: %s", reviewResp.Body.String())
	}
	var review registry.DependencyReviewResult
	if err := json.Unmarshal(reviewResp.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	if len(review.Dependencies) != 1 || review.Dependencies[0].Status != "ready" || review.Dependencies[0].BindingAction != "none" {
		t.Fatalf("expected ready/none review, got %+v", review)
	}

	// 4. Build Record BR-A is created with URL-A build dependency state
	services, _ := server.Registry.ListServices(projectID)
	buildDepStateA := registry.ComputeBuildDependencyState(appliedWebCfg.Configuration, services)
	configHashA := registry.ComputeBuildConfigHash(strings.Repeat("a", 40), "", webApp.Dockerfile, webApp.BuildContext, "ghcr.io/org/web", buildDepStateA)

	recordA := buildrecordv1.Record{
		SchemaVersion:     buildrecordv1.SchemaVersion,
		ID:                "br-a",
		ProjectID:         projectID,
		RepositoryID:      7,
		RepositoryOwnerID: 8,
		ActiveBindingID:   "binding-1",
		ServiceID:         webApp.ID,
		ServiceKey:        webApp.Name,
		CreatedAt:         time.Now().UTC(),
		Workload: buildrecordv1.WorkloadIdentity{
			RepositoryID:      7,
			RepositoryOwnerID: 8,
			Ref:               "refs/heads/main",
			SHA:               strings.Repeat("a", 40),
			EventName:         "push",
			WorkflowRef:       "o/r/.github/workflows/cd.yml@refs/heads/main",
			RunID:             10,
			RunAttempt:        1,
		},
		Build: buildrecordv1.BuildMetadata{
			ConfigHash:    configHashA,
			PlanHash:      strings.Repeat("b", 64),
			Platform:      "linux/amd64",
			OCIRepository: "ghcr.io/org/web",
			OCIDigest:     "sha256:" + strings.Repeat("a", 64),
			BuildJobID:    "job-a",
			Status:        "succeeded",
		},
	}
	if _, _, err := server.BuildRecords.Store.Create(context.Background(), "payload-a", recordA); err != nil {
		t.Fatal(err)
	}

	// Update policy for recordA
	if _, err := server.Policies.Apply(context.Background(), projectID, "owner", "policy-key-a", deploymentpolicyv1.ApplyRequest{
		Draft: deploymentpolicyv1.Draft{
			SchemaVersion:          deploymentpolicyv1.SchemaVersion,
			ProjectID:              projectID,
			RepositoryID:           recordA.RepositoryID,
			ServiceKeys:            []string{webApp.Name},
			WorkflowRefs:           []string{recordA.Workload.WorkflowRef},
			AllowedEvents:          []string{recordA.Workload.EventName},
			AllowedGitRefs:         []string{recordA.Workload.Ref},
			EnvironmentID:          plan.Assignments[0].EnvironmentID,
			AllowedRuntimeIDs:      []string{plan.Assignments[0].RuntimeID},
			AllowedOCIRepositories: []string{recordA.Build.OCIRepository},
			AllowedPlatforms:       []string{"linux/amd64"},
			AllowedConfigHashes:    []string{recordA.Build.ConfigHash},
			AllowedBuildPlanHashes: []string{recordA.Build.PlanHash},
			Enabled:                true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 5. Deploy preview for BR-A succeeds while API is at URL-A
	requestA := deploymentv1.CreateRequest{
		SchemaVersion:                  deploymentv1.JobSchemaVersion,
		BuildRecordID:                  recordA.ID,
		EnvironmentID:                  plan.Assignments[0].EnvironmentID,
		ExpectedTopologyRevision:       plan.Revision,
		ExpectedTopologyHash:           plan.PlanHash,
		ExpectedConfigurationRevision:  appliedWebCfg.Configuration.Revision,
		ExpectedConfigurationStateHash: appliedWebCfg.Configuration.StateHash,
	}
	previewA, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", requestA)
	if err != nil || !previewA.Eligible {
		t.Fatalf("preview BR-A failed: eligible=%v err=%v", previewA.Eligible, err)
	}

	// 6. Change API public route to URL-B
	apiCfgCur, _ := server.Registry.GetServiceConfiguration(projectID, apiApp.ID)
	if _, err := server.Registry.ApplyServiceConfiguration(projectID, apiApp.ID, "owner", "api-cfg-2", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			PublicRoute: &registry.PublicRouteIntent{Hostname: "api-b.example.com", Path: "/api"},
		},
		ExpectedRevision:  apiCfgCur.Revision,
		ExpectedStateHash: apiCfgCur.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	// 7. Deploy preview for BR-A is now BLOCKED with BUILD_DEPENDENCY_STALE
	_, err = server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", requestA)
	if deploymentAPIErrorCode(err) != "BUILD_DEPENDENCY_STALE" {
		t.Fatalf("expected BUILD_DEPENDENCY_STALE for stale BR-A, got err: %v", err)
	}

	// 8. Rebuild: New build BR-B is generated with URL-B build dependency state
	servicesB, _ := server.Registry.ListServices(projectID)
	buildDepStateB := registry.ComputeBuildDependencyState(appliedWebCfg.Configuration, servicesB)
	if buildDepStateA == buildDepStateB {
		t.Fatalf("build dependency state should have changed: stateA=%s stateB=%s", buildDepStateA, buildDepStateB)
	}
	configHashB := registry.ComputeBuildConfigHash(strings.Repeat("a", 40), "", webApp.Dockerfile, webApp.BuildContext, "ghcr.io/org/web", buildDepStateB)

	recordB := buildrecordv1.Record{
		SchemaVersion:     buildrecordv1.SchemaVersion,
		ID:                "br-b",
		ProjectID:         projectID,
		RepositoryID:      7,
		RepositoryOwnerID: 8,
		ActiveBindingID:   "binding-1",
		ServiceID:         webApp.ID,
		ServiceKey:        webApp.Name,
		CreatedAt:         time.Now().UTC(),
		Workload: buildrecordv1.WorkloadIdentity{
			RepositoryID:      7,
			RepositoryOwnerID: 8,
			Ref:               "refs/heads/main",
			SHA:               strings.Repeat("a", 40),
			EventName:         "push",
			WorkflowRef:       "o/r/.github/workflows/cd.yml@refs/heads/main",
			RunID:             20,
			RunAttempt:        1,
		},
		Build: buildrecordv1.BuildMetadata{
			ConfigHash:    configHashB,
			PlanHash:      strings.Repeat("b", 64),
			Platform:      "linux/amd64",
			OCIRepository: "ghcr.io/org/web",
			OCIDigest:     "sha256:" + strings.Repeat("b", 64),
			BuildJobID:    "job-b",
			Status:        "succeeded",
		},
	}
	if _, _, err := server.BuildRecords.Store.Create(context.Background(), "payload-b", recordB); err != nil {
		t.Fatal(err)
	}

	// Update policy for recordB
	if _, err := server.Policies.Apply(context.Background(), projectID, "owner", "policy-key-b", deploymentpolicyv1.ApplyRequest{
		Draft: deploymentpolicyv1.Draft{
			SchemaVersion:          deploymentpolicyv1.SchemaVersion,
			ProjectID:              projectID,
			RepositoryID:           recordB.RepositoryID,
			ServiceKeys:            []string{webApp.Name},
			WorkflowRefs:           []string{recordB.Workload.WorkflowRef},
			AllowedEvents:          []string{recordB.Workload.EventName},
			AllowedGitRefs:         []string{recordB.Workload.Ref},
			EnvironmentID:          plan.Assignments[0].EnvironmentID,
			AllowedRuntimeIDs:      []string{plan.Assignments[0].RuntimeID},
			AllowedOCIRepositories: []string{recordB.Build.OCIRepository},
			AllowedPlatforms:       []string{"linux/amd64"},
			AllowedConfigHashes:    []string{recordB.Build.ConfigHash},
			AllowedBuildPlanHashes: []string{recordB.Build.PlanHash},
			Enabled:                true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 9. Deploy preview for BR-B succeeds
	requestB := deploymentv1.CreateRequest{
		SchemaVersion:                  deploymentv1.JobSchemaVersion,
		BuildRecordID:                  recordB.ID,
		EnvironmentID:                  plan.Assignments[0].EnvironmentID,
		ExpectedTopologyRevision:       plan.Revision,
		ExpectedTopologyHash:           plan.PlanHash,
		ExpectedConfigurationRevision:  appliedWebCfg.Configuration.Revision,
		ExpectedConfigurationStateHash: appliedWebCfg.Configuration.StateHash,
	}
	previewB, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", requestB)
	if err != nil || !previewB.Eligible {
		t.Fatalf("preview BR-B failed: eligible=%v err=%v", previewB.Eligible, err)
	}
}

func TestApplicationToApplicationZeroResourceBindings(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-z", "Zero Binding Project", "zero-proj", "user-1", "project-zero")
	if err != nil {
		t.Fatal(err)
	}
	apiApp, _ := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "api", ContainerPort: 8080}, "api-key")
	webApp, _ := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "web", ContainerPort: 3000}, "web-key")

	webCfg, _ := server.Registry.GetServiceConfiguration(project.ID, webApp.ID)
	_ = configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				serviceconfigurationv1.InternalHTTPPreset("api-internal", apiApp.ID, "API", true),
			},
		},
		ExpectedRevision:  webCfg.Revision,
		ExpectedStateHash: webCfg.StateHash,
	}, "web-cfg-1")

	// Apply dependencies
	applyResp := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+webApp.ID+"/dependencies/apply", nil, "apply-dep-1")
	if applyResp.Code != http.StatusOK {
		t.Fatalf("apply dependencies failed: %s", applyResp.Body.String())
	}

	// Verify that NO ResourceBinding rows exist for webApp or apiApp
	bindings, err := server.Resources.Store.ListBindings(context.Background(), project.ID, webApp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 {
		t.Fatalf("expected 0 ResourceBindings for App->App dependency, got %d", len(bindings))
	}
}

func TestApplicationToApplicationServerPublicHTTPRuntime(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-pub-rt", "Public RT Project", "pub-rt-proj", "user-1", "project-pub-rt")
	if err != nil {
		t.Fatal(err)
	}
	apiApp, _ := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "api", ContainerPort: 8080}, "api-key")
	apiCfg, _ := server.Registry.GetServiceConfiguration(project.ID, apiApp.ID)
	_ = configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+apiApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			PublicRoute: &registry.PublicRouteIntent{Hostname: "api.public.com", Path: "/v1"},
		},
		ExpectedRevision:  apiCfg.Revision,
		ExpectedStateHash: apiCfg.StateHash,
	}, "api-cfg-1")

	consumerApp, _ := server.Registry.CreateService(project.ID, registry.ServiceDraft{Name: "worker", ContainerPort: 8000}, "worker-key")
	consumerCfg, _ := server.Registry.GetServiceConfiguration(project.ID, consumerApp.ID)
	_ = configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+consumerApp.ID+"/configuration/apply", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "api-public",
					TargetKind:     "application",
					TargetIdentity: apiApp.ID,
					Protocol:       "http",
					Strategy:       serviceconfigurationv1.StrategyPublicHTTP,
					AccessContext:  serviceconfigurationv1.AccessContextServer,
					InjectionPhase: serviceconfigurationv1.InjectionPhaseRuntime,
					Required:       true,
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "PUBLIC_API_URL", SymbolicSource: "application.public_url"},
					},
				},
			},
		},
		ExpectedRevision:  consumerCfg.Revision,
		ExpectedStateHash: consumerCfg.StateHash,
	}, "worker-cfg-1")

	// Review dependencies: projection should resolve PUBLIC_API_URL to https://api.public.com/v1
	reviewResp := configurationRequest(t, server, http.MethodPost, "/api/projects/"+project.ID+"/services/"+consumerApp.ID+"/dependencies/review", nil, "")
	if reviewResp.Code != http.StatusOK {
		t.Fatalf("review failed: %s", reviewResp.Body.String())
	}
	var review registry.DependencyReviewResult
	if err := json.Unmarshal(reviewResp.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	if len(review.Dependencies) != 1 || len(review.Dependencies[0].Projections) != 1 {
		t.Fatalf("unexpected review: %+v", review)
	}
	if review.Dependencies[0].Projections[0].ValuePreview != "https://api.public.com/v1" {
		t.Fatalf("expected projection preview https://api.public.com/v1, got %s", review.Dependencies[0].Projections[0].ValuePreview)
	}
}
