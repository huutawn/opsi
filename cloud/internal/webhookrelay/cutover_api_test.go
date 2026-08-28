package webhookrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	"golang.org/x/crypto/bcrypt"
)

func setupCutoverAPITestServer(t *testing.T) (*Server, string, string, string, string, string, string) {
	t.Helper()
	server := NewServer(Config{})
	ctx := context.Background()

	project, err := server.Registry.CreateProject("org-cutover", "Project", "project-cutover", "user-1", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.ID
	services := server.Registry.(*registry.Service)
	facts, err := services.PlacementFacts(ctx, projectID)
	if err != nil || len(facts.Environments) == 0 {
		t.Fatalf("facts err: %v", err)
	}
	envID := facts.Environments[0].ID
	runtimeID := facts.Runtimes[0].ID

	node, err := server.Registry.UpsertNode(projectID, "server", "server", "healthy", "127.0.0.1", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	nodeID := node.ID

	hash, _ := bcrypt.GenerateFromPassword([]byte("agent-secret"), bcrypt.DefaultCost)
	agent, err := server.Registry.RegisterAgent(projectID, nodeID, "fp", string(hash), "v1", "agent-key", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	agentID := agent.ID
	if _, err := server.Registry.RecordAgentHeartbeat(projectID, nodeID, registry.AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}

	sourceResource := webhookReadyPostgres(projectID, envID, runtimeID, nodeID, agentID)
	sourceResource.ID = "res-source"
	sourceResource.Runtime.Spec.ResourceID = "res-source"
	sourceResource.Runtime.Spec.Assignment.NodeID = nodeID
	sourceResource.Runtime.Spec.Assignment.AgentID = agentID
	sourceResource.Runtime.Spec.Assignment.RuntimeID = runtimeID
	sourceResource.Runtime.Spec.Connection.ServiceName = "opsi-mr-source"
	sourceResource.Runtime.Spec.SpecHash, _ = sourceResource.Runtime.Spec.Hash()
	sourceResource.Runtime.Evidence.ObservedSpecHash = sourceResource.Runtime.Spec.SpecHash
	sourceResource.Runtime.Evidence.WorkloadReady = true
	sourceResource.Runtime.Evidence.PodReady = true
	sourceResource.Runtime.Evidence.ServiceReady = true
	sourceResource.Runtime.Evidence.SecretReady = true
	sourceResource.Runtime.Evidence.AuthReady = true
	sourceResource.Runtime.Evidence.StorageReady = true
	sourceResource.Runtime.Evidence.VolumeMounted = true
	sourceResource.Runtime.Evidence.PVCUID = "pvc-source-uid"
	sourceResource.Runtime.Evidence.PVUID = "pv-source-uid"
	sourceResource.Runtime.Evidence.StorageHash = resourcev1.ManagedResourceStorageHash(sourceResource.Runtime.Spec)
	if _, _, err := server.Resources.Store.Create(ctx, sourceResource, "source-create", strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}

	targetResource := webhookReadyPostgres(projectID, envID, runtimeID, nodeID, agentID)
	targetResource.ID = "res-target"
	targetResource.Runtime.Spec.ResourceID = "res-target"
	targetResource.Runtime.Spec.Assignment.NodeID = nodeID
	targetResource.Runtime.Spec.Assignment.AgentID = agentID
	targetResource.Runtime.Spec.Assignment.RuntimeID = runtimeID
	targetResource.Runtime.Spec.Connection.ServiceName = "opsi-mr-target"
	targetResource.Runtime.Spec.SpecHash, _ = targetResource.Runtime.Spec.Hash()
	targetResource.Runtime.Evidence.ObservedSpecHash = targetResource.Runtime.Spec.SpecHash
	targetResource.Runtime.Evidence.WorkloadReady = true
	targetResource.Runtime.Evidence.PodReady = true
	targetResource.Runtime.Evidence.ServiceReady = true
	targetResource.Runtime.Evidence.SecretReady = true
	targetResource.Runtime.Evidence.AuthReady = true
	targetResource.Runtime.Evidence.StorageReady = true
	targetResource.Runtime.Evidence.VolumeMounted = true
	targetResource.Runtime.Evidence.PVCUID = "pvc-target-uid"
	targetResource.Runtime.Evidence.PVUID = "pv-target-uid"
	targetResource.Runtime.Evidence.StorageHash = resourcev1.ManagedResourceStorageHash(targetResource.Runtime.Spec)
	if _, _, err := server.Resources.Store.Create(ctx, targetResource, "target-create", strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}

	app, err := server.Registry.CreateService(projectID, registryDraft(envID, runtimeID, "web-app"), "app-key")
	if err != nil {
		t.Fatal(err)
	}

	sourceBinding := resourcev1.Binding{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "bind-source",
		ProjectID:     projectID,
		EnvironmentID: envID,
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: app.ID},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: sourceResource.ID},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   "DATABASE",
		Lifecycle:     resourcev1.LifecycleReady,
		CredentialID:  "cred-source-bind",
		RoleName:      "rb_source_user",
		Database:      "opsi",
	}
	if _, _, err := server.Resources.Store.CreateBinding(ctx, sourceBinding, "src-bind-key", strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}

	targetBinding := resourcev1.Binding{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "bind-target",
		ProjectID:     projectID,
		EnvironmentID: envID,
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: app.ID},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: targetResource.ID},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   "DATABASE",
		Lifecycle:     resourcev1.LifecycleReady,
		CredentialID:  "cred-target-bind",
		RoleName:      "rb_target_user",
		Database:      "opsi",
	}
	if _, _, err := server.Resources.Store.CreateBinding(ctx, targetBinding, "tgt-bind-key", strings.Repeat("4", 64)); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	completedAt := now.Add(5 * time.Minute)

	backup := backupv1.Backup{
		SchemaVersion:         backupv1.SchemaVersion,
		ID:                    "bak-cutover-1",
		ProjectID:             projectID,
		EnvironmentID:         envID,
		SourceResourceID:      sourceResource.ID,
		SourceNodeID:          nodeID,
		ResourceType:          sourceResource.Type,
		SourcePostgresVersion: resourcev1.PostgresVersion,
		Format:                backupv1.FormatCustom,
		Lifecycle:             backupv1.LifecycleSucceeded,
		ArtifactSize:          2048,
		SHA256:                strings.Repeat("a", 64),
		ArchiveVerified:       true,
		CreatedAt:             now,
		CompletedAt:           &completedAt,
	}
	if _, _, err := server.Backups.Store.Create(ctx, backup, "bak-key", "bak-payload"); err != nil {
		t.Fatal(err)
	}

	restore := restorev1.Restore{
		SchemaVersion:        restorev1.SchemaVersion,
		ID:                   "rst-cutover-1",
		ProjectID:            projectID,
		EnvironmentID:        envID,
		ReviewID:             "rrv-cutover-1",
		BackupID:             backup.ID,
		SourceResourceID:     sourceResource.ID,
		TargetResourceID:     targetResource.ID,
		TargetNodeID:         nodeID,
		ArtifactSHA256:       backup.SHA256,
		ArtifactSize:         backup.ArtifactSize,
		TargetSpecHash:       targetResource.Runtime.Spec.SpecHash,
		TargetStorageHash:    resourcev1.ManagedResourceStorageHash(targetResource.Runtime.Spec),
		PristineEvidenceHash: strings.Repeat("b", 64),
		PGRestoreVersion:     "pg_restore (PostgreSQL) 16.2",
		ArchiveVerified:      true,
		Lifecycle:            restorev1.LifecycleSucceeded,
		VerifyingAt:          &completedAt,
		CompletedAt:          &completedAt,
		VerificationMetadata: map[string]string{
			"connectivity": "authenticated",
			"transaction":  "committed",
		},
	}
	if _, _, err := server.Restores.Store.Create(ctx, restore, "rst-key", "rst-payload"); err != nil {
		t.Fatal(err)
	}

	credAuthority := resource.NewMemoryCredentialAuthority()
	_, _ = credAuthority.EnsureBinding(ctx, resourcev1.BindingCredentialSpec{
		CredentialID: sourceBinding.CredentialID,
		BindingID:    sourceBinding.ID,
		ResourceID:   sourceResource.ID,
		Username:     sourceBinding.RoleName,
		Database:     sourceBinding.Database,
	})
	_, _ = credAuthority.EnsureBinding(ctx, resourcev1.BindingCredentialSpec{
		CredentialID: targetBinding.CredentialID,
		BindingID:    targetBinding.ID,
		ResourceID:   targetResource.ID,
		Username:     targetBinding.RoleName,
		Database:     targetBinding.Database,
	})
	_, _ = credAuthority.Ensure(ctx, sourceResource.Runtime.Spec.CredentialID)
	_, _ = credAuthority.Ensure(ctx, targetResource.Runtime.Spec.CredentialID)
	server.Resources.Credentials = credAuthority
	server.Cutovers.Credentials = credAuthority
	server.Cutovers.Resources = server.Resources

	appDraft := app.Configuration.ServiceConfigurationDraft
	appDraft.ResourceBindings = []serviceconfigurationv1.ResourceBinding{
		{LogicalName: "DATABASE", BindingID: sourceBinding.ID},
	}
	appliedCfg, err := server.Registry.ApplyServiceConfiguration(projectID, app.ID, "user-1", "init-cfg", registry.ServiceConfigurationApplyRequest{
		Draft:             appDraft,
		ExpectedRevision:  app.Configuration.Revision,
		ExpectedStateHash: app.Configuration.StateHash,
	})
	if err != nil {
		t.Fatal(err)
	}

	workload := deploymentv1.WorkloadSpec{
		SchemaVersion:            deploymentv1.WorkloadSchemaVersion,
		ServiceKey:               app.Name,
		Replicas:                 1,
		ApplicationContainerName: deploymentv1.ApplicationContainer,
		ContainerPort:            8080,
		Resources: deploymentv1.Resources{
			Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"},
			Limits:   deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"},
		},
		TerminationGracePeriodSecond: 30,
		Exposure:                     deploymentv1.ExposureIntent{Mode: "internal"},
	}
	workloadHash, _ := workload.Hash()
	image, _ := deploymentv1.NewImmutableImage("ghcr.io/opsi-dev/app", "sha256:"+strings.Repeat("a", 64))
	record := buildrecordv1.Record{
		SchemaVersion:   buildrecordv1.SchemaVersion,
		ID:              "br-app-1",
		ProjectID:       projectID,
		ServiceID:       app.ID,
		ServiceKey:      app.Name,
		ActiveBindingID: sourceBinding.ID,
		Build: buildrecordv1.BuildMetadata{
			OCIRepository: image.Repository,
			OCIDigest:     image.Digest,
			Status:        "succeeded",
		},
	}
	_, _, _ = server.BuildRecords.Store.Create(ctx, projectID, record)
	snapshot := deploymentv1.JobSnapshot{
		SchemaVersion: deploymentv1.JobSchemaVersion,
		ProjectID:     projectID,
		Image:         image,
		Workload:      workload,
		SpecHash:      workloadHash,
		PayloadHash:   "base-payload",
		Authority: deploymentv1.AuthoritySnapshot{
			BuildRecord:                   record,
			TopologyPlanID:                "topology-1",
			TopologyRevision:              1,
			TopologyHash:                  strings.Repeat("1", 64),
			ServiceConfigurationRevision:  appliedCfg.Configuration.Revision,
			ServiceConfigurationStateHash: appliedCfg.Configuration.StateHash,
			DeploymentPolicyID:            "policy-1",
			DeploymentPolicyRevision:      1,
			DeploymentPolicyHash:          strings.Repeat("2", 64),
			RoutingDecisionHash:           strings.Repeat("3", 64),
			EnvironmentID:                 envID,
			RuntimeID:                     runtimeID,
			NodeID:                        nodeID,
			AgentID:                       agentID,
		},
	}
	baseJob, _, err := server.Registry.(*registry.Service).StartImmutableDeployment(snapshot, "user-1", "base-key", "base-request")
	if err != nil {
		t.Fatal(err)
	}
	baseLease, ok, err := server.Registry.(*registry.Service).LeaseDeployment(projectID, nodeID)
	if err != nil || !ok {
		t.Fatalf("base lease ok=%v err=%v", ok, err)
	}
	for index, state := range []string{deploymentv1.RolloutStateApplying, deploymentv1.RolloutStateWaiting, deploymentv1.RolloutStateSucceeded} {
		currentDigest := ""
		if state == deploymentv1.RolloutStateSucceeded {
			currentDigest = image.Digest
		}
		progress := deploymentv1.Progress{
			SchemaVersion:    deploymentv1.EventSchemaVersion,
			LeaseToken:       baseLease.LeaseToken,
			State:            state,
			RolloutID:        baseJob.RolloutIntent.RolloutID,
			IntentHash:       baseJob.IntentHash,
			StateHash:        strings.Repeat(string(rune('a'+index)), 64),
			WorkloadSpecHash: baseJob.RolloutIntent.Desired.WorkloadSpecHash,
			ExposureSpecHash: baseJob.RolloutIntent.Desired.ExposureSpecHash,
			DesiredDigest:    image.Digest,
			CurrentDigest:    currentDigest,
			PreviousDigest:   baseJob.PreviousDigest,
			Attempt:          baseJob.RolloutIntent.Attempt,
		}
		if _, err := server.Registry.(*registry.Service).ProgressImmutableDeployment(projectID, nodeID, baseJob.ID, "base-progress-"+state, progress); err != nil {
			t.Fatal(err)
		}
	}

	intent := baseLease.Command.Rollout
	agentResult := &deploymentv1.AgentResult{
		SchemaVersion:         deploymentv1.ResultSchemaVersion,
		Status:                deploymentv1.RolloutStateSucceeded,
		RolloutID:             intent.RolloutID,
		RolloutState:          deploymentv1.RolloutStateSucceeded,
		IntentHash:            intent.IntentHash,
		StateHash:             strings.Repeat("c", 64),
		SpecHash:              intent.Desired.WorkloadSpecHash,
		WorkloadSpecHash:      intent.Desired.WorkloadSpecHash,
		ExposureSpecHash:      intent.Desired.ExposureSpecHash,
		DesiredDigest:         intent.Desired.Image.Digest,
		CurrentDigest:         intent.Desired.Image.Digest,
		PreviousDigest:        intent.PreviousDigest,
		KnownGoodID:           baseJob.RolloutIntent.RolloutID,
		KnownGoodHash:         strings.Repeat("a", 64),
		ReadinessEvidenceHash: strings.Repeat("e", 64),
		ApplicationImage:      intent.Desired.Image.Reference,
		ApplicationImageID:    "containerd://" + intent.Desired.Image.Digest,
		Namespace:             "opsi",
		DeploymentName:        "api",
		ServiceName:           "api",
		AvailableReplicas:     intent.Desired.Workload.Replicas,
		Attempt:               intent.Attempt,
		Resources: []deploymentv1.ResourceIdentity{
			{Kind: "Deployment", Namespace: "opsi", Name: "api", UID: "uid-api", ResourceVersion: "1", FunctionalHash: strings.Repeat("f", 64)},
			{Kind: "Service", Namespace: "opsi", Name: "api", UID: "uid-service", ResourceVersion: "1", FunctionalHash: strings.Repeat("d", 64)},
		},
	}
	result := registry.DeploymentResult{
		SchemaVersion: deploymentv1.ResultSchemaVersion,
		Status:        deploymentv1.RolloutStateSucceeded,
		LeaseToken:    baseLease.LeaseToken,
		IntentHash:    intent.IntentHash,
		RolloutResult: agentResult,
	}
	if _, err := server.Registry.(*registry.Service).CompleteDeployment(projectID, nodeID, baseJob.ID, "base-result", result); err != nil {
		t.Fatal(err)
	}

	return server, projectID, app.ID, sourceBinding.ID, targetBinding.ID, nodeID, agentID
}

func registryDraft(envID, runtimeID, name string) registry.ServiceDraft {
	return registry.ServiceDraft{
		Name:          name,
		Type:          "web",
		SourceType:    "image",
		Image:         "ghcr.io/opsi-dev/app@sha256:7b1e8ff602a632eefae0cbe9f4e24ef78d234a958e94a821e2fb95b18db4b830",
		ContainerPort: 8080,
		HealthPath:    "/health",
		Replicas:      1,
	}
}

func TestCutoverReviewAPIEndToEnd(t *testing.T) {
	server, projectID, appID, sourceBindingID, targetBindingID, nodeID, _ := setupCutoverAPITestServer(t)

	// 1. Create Cutover Review via POST /api/projects/{project}/applications/{app}/cutover-reviews
	reqBody, _ := json.Marshal(cutoverv1.ReviewRequest{
		SourceBindingID: sourceBindingID,
		TargetBindingID: targetBindingID,
	})
	path := "/api/projects/" + projectID + "/applications/" + appID + "/cutover-reviews"
	resp := requestResourceAPI(t, server, http.MethodPost, path, string(reqBody), "review-idemp-1")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", resp.Code, resp.Body.String())
	}
	var createdResult struct {
		CutoverReview cutoverv1.ApplicationCutoverReview `json:"cutover_review"`
		Reused        bool                               `json:"reused"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &createdResult); err != nil {
		t.Fatal(err)
	}
	reviewID := createdResult.CutoverReview.ID
	if reviewID == "" || createdResult.CutoverReview.Lifecycle != cutoverv1.ReviewQueued {
		t.Fatalf("unexpected review created: %+v", createdResult.CutoverReview)
	}

	// 2. Also check POST /api/projects/{project}/services/{service}/cutover-reviews with same Idempotency-Key returns reused: true
	servicePath := "/api/projects/" + projectID + "/services/" + appID + "/cutover-reviews"
	respReused := requestResourceAPI(t, server, http.MethodPost, servicePath, string(reqBody), "review-idemp-1")
	if respReused.Code != http.StatusAccepted || !strings.Contains(respReused.Body.String(), `"reused":true`) {
		t.Fatalf("expected reused review on same key: %s", respReused.Body.String())
	}

	// 3. Agent polls job on /v1/agents/{node}/webhooks/next
	agentPollPath := "/v1/agents/" + nodeID + "/webhooks/next?project_id=" + projectID + "&wait=0s"
	agentReq := httptest.NewRequest(http.MethodGet, agentPollPath, nil)
	agentReq.Header.Set("Authorization", "Bearer agent-secret")
	agentResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(agentResp, agentReq)

	if agentResp.Code != http.StatusOK {
		t.Fatalf("expected agent lease 200 OK, got %d: %s", agentResp.Code, agentResp.Body.String())
	}
	var leasePayload struct {
		Kind             string                                `json:"kind"`
		LeaseToken       string                                `json:"lease_token"`
		Review           cutoverv1.ApplicationCutoverReview    `json:"review"`
		SourceCredential *resourcev1.ManagedResourceCredential `json:"source_credential"`
		TargetCredential *resourcev1.ManagedResourceCredential `json:"target_credential"`
	}
	if err := json.Unmarshal(agentResp.Body.Bytes(), &leasePayload); err != nil {
		t.Fatal(err)
	}
	if leasePayload.Kind != "cutover_review" || leasePayload.LeaseToken == "" || leasePayload.SourceCredential == nil || leasePayload.TargetCredential == nil {
		t.Fatalf("invalid lease payload: %+v", leasePayload)
	}

	// 4. Agent posts result to /v1/agents/{node}/cutover-reviews/{review}/result
	resultPayload, _ := json.Marshal(cutoverv1.ReviewResult{
		Status:               cutoverv1.ReviewSucceeded,
		LeaseToken:           leasePayload.LeaseToken,
		SourceSQLPreflight:   "PASS",
		TargetSQLPreflight:   "PASS",
		TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		ValidationSummary: cutoverv1.ValidationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			SourceBindingReady:   true,
			TargetBindingReady:   true,
			TargetRestoreReady:   true,
		},
	})
	agentResultPath := "/v1/agents/" + nodeID + "/cutover-reviews/" + reviewID + "/result?project_id=" + projectID
	agentResultReq := httptest.NewRequest(http.MethodPost, agentResultPath, strings.NewReader(string(resultPayload)))
	agentResultReq.Header.Set("Authorization", "Bearer agent-secret")
	agentResultReq.Header.Set("Content-Type", "application/json")
	agentResultResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(agentResultResp, agentResultReq)

	if agentResultResp.Code != http.StatusOK {
		t.Fatalf("expected agent complete 200 OK, got %d: %s", agentResultResp.Code, agentResultResp.Body.String())
	}

	// 5. Query review via GET /api/projects/{project}/application-cutover-reviews/{review}
	getResp := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/application-cutover-reviews/"+reviewID, "", "")
	if getResp.Code != http.StatusOK {
		t.Fatalf("get review failed: %d %s", getResp.Code, getResp.Body.String())
	}
	var getResult struct {
		Review cutoverv1.ApplicationCutoverReview `json:"review"`
	}
	if err := json.Unmarshal(getResp.Body.Bytes(), &getResult); err != nil {
		t.Fatal(err)
	}
	if getResult.Review.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("review not succeeded: %+v", getResult.Review)
	}
	if err := getResult.Review.ValidateSucceeded(); err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	// 6. Security check: API response has NO bearer tokens or passwords
	bodyStr := getResp.Body.String()
	for _, forbidden := range []string{"password", "secret", "review_token", "ReviewToken", "bearer"} {
		if strings.Contains(strings.ToLower(bodyStr), `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q found in API response", forbidden)
		}
	}

	// 7. Verify Audits
	audits, _ := server.Registry.ListAudit(projectID)
	requestedFound, succeededFound := false, false
	for _, a := range audits {
		if a.Action == "CUTOVER_REVIEW_REQUESTED" && a.ResourceID == reviewID {
			requestedFound = true
		}
		if a.Action == "CUTOVER_REVIEW_SUCCEEDED" && a.ResourceID == reviewID {
			succeededFound = true
		}
	}
	if !requestedFound || !succeededFound {
		t.Fatalf("missing audits: requested=%t succeeded=%t audits=%+v", requestedFound, succeededFound, audits)
	}

	// 8. Zero mutations proof
	appConfig, _ := server.Registry.GetServiceConfiguration(projectID, appID)
	if appConfig.Revision != 1 {
		t.Fatalf("application config mutated during review: %+v", appConfig)
	}
	srcBinding, _ := server.Resources.Store.GetBinding(context.Background(), projectID, sourceBindingID)
	if srcBinding.Target.ID != "res-source" || srcBinding.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("source binding mutated during review: %+v", srcBinding)
	}
	deployments, _ := server.Registry.ListDeployments(projectID)
	if len(deployments) != 1 {
		t.Fatalf("unexpected deployments created during review: %+v", deployments)
	}
}

func TestCutoverApplyAPIEndToEndAndAudit(t *testing.T) {
	server, projectID, appID, sourceBindingID, targetBindingID, nodeID, _ := setupCutoverAPITestServer(t)

	// 1. Create and complete a Cutover Review
	reqBody, _ := json.Marshal(cutoverv1.ReviewRequest{
		SourceBindingID: sourceBindingID,
		TargetBindingID: targetBindingID,
	})
	reviewResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutover-reviews", string(reqBody), "rev-key-1")
	if reviewResp.Code != http.StatusAccepted {
		t.Fatalf("create review failed: %d %s", reviewResp.Code, reviewResp.Body.String())
	}
	var revPayload struct {
		Review cutoverv1.ApplicationCutoverReview `json:"review"`
	}
	_ = json.Unmarshal(reviewResp.Body.Bytes(), &revPayload)
	reviewID := revPayload.Review.ID

	// Agent claims and completes review
	agentReq := httptest.NewRequest(http.MethodGet, "/v1/agents/"+nodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	agentReq.Header.Set("Authorization", "Bearer agent-secret")
	agentResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(agentResp, agentReq)
	var leasePayload struct {
		LeaseToken string `json:"lease_token"`
	}
	_ = json.Unmarshal(agentResp.Body.Bytes(), &leasePayload)

	resultPayload, _ := json.Marshal(cutoverv1.ReviewResult{
		Status:               cutoverv1.ReviewSucceeded,
		LeaseToken:           leasePayload.LeaseToken,
		SourceSQLPreflight:   "PASS",
		TargetSQLPreflight:   "PASS",
		TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		ValidationSummary: cutoverv1.ValidationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			SourceBindingReady:   true,
			TargetBindingReady:   true,
			TargetRestoreReady:   true,
		},
	})
	agentResultReq := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/cutover-reviews/"+reviewID+"/result?project_id="+projectID, strings.NewReader(string(resultPayload)))
	agentResultReq.Header.Set("Authorization", "Bearer agent-secret")
	agentResultReq.Header.Set("Content-Type", "application/json")
	agentResultResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(agentResultResp, agentResultReq)
	if agentResultResp.Code != http.StatusOK {
		t.Fatalf("agent complete review failed: %d %s", agentResultResp.Code, agentResultResp.Body.String())
	}

	// 2. Explicit Cutover Apply: POST /api/projects/{project}/applications/{application}/cutovers
	applyBody, _ := json.Marshal(cutoverv1.ApplyRequest{
		CutoverReviewID: reviewID,
	})
	cutoverResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers", string(applyBody), "cutover-key-1")
	if cutoverResp.Code != http.StatusAccepted {
		t.Fatalf("create cutover failed: %d %s", cutoverResp.Code, cutoverResp.Body.String())
	}

	var cutoverPayload struct {
		Cutover cutoverv1.ApplicationCutover `json:"cutover"`
		Reused  bool                         `json:"reused"`
	}
	if err := json.Unmarshal(cutoverResp.Body.Bytes(), &cutoverPayload); err != nil {
		t.Fatal(err)
	}
	cutover := cutoverPayload.Cutover
	if cutover.ID == "" || cutover.Lifecycle != cutoverv1.CutoverDeploying {
		t.Fatalf("unexpected cutover: %+v", cutover)
	}
	if cutoverPayload.Reused {
		t.Fatal("expected first cutover apply to not be reused")
	}

	// 3. Replay with same Idempotency-Key returns reused: true
	replayResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers", string(applyBody), "cutover-key-1")
	if replayResp.Code != http.StatusAccepted || !strings.Contains(replayResp.Body.String(), `"reused":true`) {
		t.Fatalf("expected reused cutover on replay: %s", replayResp.Body.String())
	}

	// 4. GET /api/projects/{project}/applications/{application}/cutovers/{cutover}
	getResp := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutover.ID, "", "")
	if getResp.Code != http.StatusOK {
		t.Fatalf("get cutover failed: %d %s", getResp.Code, getResp.Body.String())
	}

	// 5. GET /api/projects/{project}/application-cutovers
	listResp := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/application-cutovers?application_id="+appID, "", "")
	if listResp.Code != http.StatusOK || !strings.Contains(listResp.Body.String(), cutover.ID) {
		t.Fatalf("list application-cutovers failed: %s", listResp.Body.String())
	}

	// 6. Complete Cutover Verification: POST /v1/agents/{node}/cutovers/{cutover}/result
	completeResult, _ := json.Marshal(cutoverv1.CutoverApplyResult{
		Status: "succeeded",
		VerificationSummary: cutoverv1.CutoverVerificationSummary{
			SourceSQLPreflight:       "PASS",
			TargetSQLPreflight:       "PASS",
			TargetRoleAttributes:     "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:          true,
			WorkloadReady:            true,
			TargetDBConnected:        true,
			RestoredDataVerified:     true,
			TargetOnlyMarkerPresent:  true,
			SourceOnlyMarkerAbsent:   true,
			PostCutoverTargetWritten: true,
			SourceRollbackPreserved:  true,
		},
	})
	completeReq := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/cutovers/"+cutover.ID+"/result?project_id="+projectID, strings.NewReader(string(completeResult)))
	completeReq.Header.Set("Authorization", "Bearer agent-secret")
	completeReq.Header.Set("Content-Type", "application/json")
	completeResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(completeResp, completeReq)
	if completeResp.Code != http.StatusOK {
		t.Fatalf("agent complete cutover failed: %d %s", completeResp.Code, completeResp.Body.String())
	}

	// 7. Verify Succeeded Cutover
	finalGetResp := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/application-cutovers/"+cutover.ID, "", "")
	var finalPayload struct {
		Cutover cutoverv1.ApplicationCutover `json:"cutover"`
	}
	_ = json.Unmarshal(finalGetResp.Body.Bytes(), &finalPayload)
	if finalPayload.Cutover.Lifecycle != cutoverv1.CutoverSucceeded {
		t.Fatalf("expected succeeded cutover, got %+v", finalPayload.Cutover)
	}
	if err := finalPayload.Cutover.ValidateSucceeded(); err != nil {
		t.Fatalf("cutover validation failed: %v\ncutover=%+v\nhash=%s\nwant=%s", err, finalPayload.Cutover, finalPayload.Cutover.EvidenceHash, cutoverv1.CutoverEvidenceHash(finalPayload.Cutover))
	}

	// 8. Verify SOURCE rollback authority is strictly preserved (NOT deleted, NOT revoked)
	srcBinding, err := server.Resources.Store.GetBinding(context.Background(), projectID, sourceBindingID)
	if err != nil || srcBinding.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("source binding must remain ready as rollback authority: %+v err=%v", srcBinding, err)
	}
	srcResource, err := server.Resources.Store.Get(context.Background(), projectID, "res-source")
	if err != nil || srcResource.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("source resource must remain ready: %+v err=%v", srcResource, err)
	}

	// 9. Verify Audits
	audits, _ := server.Registry.ListAudit(projectID)
	actions := map[string]bool{}
	for _, a := range audits {
		if a.ResourceID == cutover.ID {
			actions[a.Action] = true
		}
	}
	for _, expectedAction := range []string{"CUTOVER_REQUESTED", "CUTOVER_APPLY_STARTED", "CUTOVER_SUCCEEDED"} {
		if !actions[expectedAction] {
			t.Fatalf("missing expected audit action %s: %+v", expectedAction, audits)
		}
	}
}

func TestCutoverRollbackAPIEndToEndAndAudit(t *testing.T) {
	server, projectID, appID, sourceBindingID, targetBindingID, nodeID, _ := setupCutoverAPITestServer(t)

	// 1. Create and complete Cutover Review
	reviewBody, _ := json.Marshal(cutoverv1.ReviewRequest{
		SourceBindingID: sourceBindingID,
		TargetBindingID: targetBindingID,
	})
	reviewResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutover-reviews", string(reviewBody), "rev-key-1")
	if reviewResp.Code != http.StatusAccepted {
		t.Fatalf("review failed: %d %s", reviewResp.Code, reviewResp.Body.String())
	}
	var revPayload struct {
		Review cutoverv1.ApplicationCutoverReview `json:"review"`
	}
	_ = json.Unmarshal(reviewResp.Body.Bytes(), &revPayload)

	// Agent claims and completes review
	agentReq := httptest.NewRequest(http.MethodGet, "/v1/agents/"+nodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	agentReq.Header.Set("Authorization", "Bearer agent-secret")
	agentResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(agentResp, agentReq)
	var leasePayload struct {
		LeaseToken string `json:"lease_token"`
	}
	_ = json.Unmarshal(agentResp.Body.Bytes(), &leasePayload)

	compReview, _ := json.Marshal(cutoverv1.ReviewResult{
		Status:               cutoverv1.ReviewSucceeded,
		LeaseToken:           leasePayload.LeaseToken,
		SourceSQLPreflight:   "PASS",
		TargetSQLPreflight:   "PASS",
		TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		ValidationSummary: cutoverv1.ValidationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			SourceBindingReady:   true,
			TargetBindingReady:   true,
			TargetRestoreReady:   true,
		},
	})
	revCompReq := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/cutover-reviews/"+revPayload.Review.ID+"/result?project_id="+projectID, strings.NewReader(string(compReview)))
	revCompReq.Header.Set("Authorization", "Bearer agent-secret")
	revCompReq.Header.Set("Content-Type", "application/json")
	revCompResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(revCompResp, revCompReq)
	if revCompResp.Code != http.StatusOK {
		t.Fatalf("complete review failed: %d %s", revCompResp.Code, revCompResp.Body.String())
	}

	// 2. Apply Cutover
	applyBody, _ := json.Marshal(cutoverv1.ApplyRequest{
		CutoverReviewID: revPayload.Review.ID,
	})
	applyResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers", string(applyBody), "apply-key-1")
	if applyResp.Code != http.StatusAccepted {
		t.Fatalf("apply failed: %d %s", applyResp.Code, applyResp.Body.String())
	}
	var cutoverPayload struct {
		Cutover cutoverv1.ApplicationCutover `json:"cutover"`
	}
	_ = json.Unmarshal(applyResp.Body.Bytes(), &cutoverPayload)

	compCutover, _ := json.Marshal(cutoverv1.CutoverApplyResult{
		Status: "succeeded",
		VerificationSummary: cutoverv1.CutoverVerificationSummary{
			SourceSQLPreflight:       "PASS",
			TargetSQLPreflight:       "PASS",
			TargetRoleAttributes:     "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:          true,
			WorkloadReady:            true,
			TargetDBConnected:        true,
			RestoredDataVerified:     true,
			TargetOnlyMarkerPresent:  true,
			SourceOnlyMarkerAbsent:   true,
			PostCutoverTargetWritten: true,
			SourceRollbackPreserved:  true,
		},
	})
	cutCompReq := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/cutovers/"+cutoverPayload.Cutover.ID+"/result?project_id="+projectID, strings.NewReader(string(compCutover)))
	cutCompReq.Header.Set("Authorization", "Bearer agent-secret")
	cutCompReq.Header.Set("Content-Type", "application/json")
	cutCompResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(cutCompResp, cutCompReq)
	if cutCompResp.Code != http.StatusOK {
		t.Fatalf("complete cutover failed: %d %s", cutCompResp.Code, cutCompResp.Body.String())
	}

	// Complete cutover deployment
	cutoverLease, ok, err := server.Registry.LeaseDeployment(projectID, nodeID)
	if err != nil || !ok {
		t.Fatalf("cutover lease failed: %v", err)
	}
	for index, state := range []string{deploymentv1.RolloutStateApplying, deploymentv1.RolloutStateWaiting, deploymentv1.RolloutStateSucceeded} {
		currentDigest := ""
		if state == deploymentv1.RolloutStateSucceeded {
			currentDigest = cutoverLease.Command.Rollout.Desired.Image.Digest
		}
		progress := deploymentv1.Progress{
			SchemaVersion:    deploymentv1.EventSchemaVersion,
			LeaseToken:       cutoverLease.LeaseToken,
			State:            state,
			RolloutID:        cutoverLease.Command.Rollout.RolloutID,
			IntentHash:       cutoverLease.Command.Rollout.IntentHash,
			StateHash:        strings.Repeat(string(rune('a'+index)), 64),
			WorkloadSpecHash: cutoverLease.Command.Rollout.Desired.WorkloadSpecHash,
			ExposureSpecHash: cutoverLease.Command.Rollout.Desired.ExposureSpecHash,
			DesiredDigest:    cutoverLease.Command.Rollout.Desired.Image.Digest,
			CurrentDigest:    currentDigest,
			PreviousDigest:   cutoverLease.Command.Rollout.PreviousDigest,
			Attempt:          cutoverLease.Command.Rollout.Attempt,
		}
		if _, err := server.Registry.(*registry.Service).ProgressImmutableDeployment(projectID, nodeID, cutoverLease.Deployment.ID, "cutover-"+state, progress); err != nil {
			t.Fatal(err)
		}
	}
	cutoverIntent := cutoverLease.Command.Rollout
	cutoverAgentResult := &deploymentv1.AgentResult{
		SchemaVersion:         deploymentv1.ResultSchemaVersion,
		Status:                deploymentv1.RolloutStateSucceeded,
		RolloutID:             cutoverIntent.RolloutID,
		RolloutState:          deploymentv1.RolloutStateSucceeded,
		IntentHash:            cutoverIntent.IntentHash,
		StateHash:             strings.Repeat("c", 64),
		SpecHash:              cutoverIntent.Desired.WorkloadSpecHash,
		WorkloadSpecHash:      cutoverIntent.Desired.WorkloadSpecHash,
		ExposureSpecHash:      cutoverIntent.Desired.ExposureSpecHash,
		DesiredDigest:         cutoverIntent.Desired.Image.Digest,
		CurrentDigest:         cutoverIntent.Desired.Image.Digest,
		PreviousDigest:        cutoverIntent.PreviousDigest,
		KnownGoodID:           cutoverIntent.RolloutID,
		KnownGoodHash:         strings.Repeat("a", 64),
		ReadinessEvidenceHash: strings.Repeat("e", 64),
		ApplicationImage:      cutoverIntent.Desired.Image.Reference,
		ApplicationImageID:    "containerd://" + cutoverIntent.Desired.Image.Digest,
		Namespace:             "opsi",
		DeploymentName:        "api",
		ServiceName:           "api",
		AvailableReplicas:     cutoverIntent.Desired.Workload.Replicas,
		Attempt:               cutoverIntent.Attempt,
		Resources: []deploymentv1.ResourceIdentity{
			{Kind: "Deployment", Namespace: "opsi", Name: "api", UID: "uid-api", ResourceVersion: "1", FunctionalHash: strings.Repeat("f", 64)},
			{Kind: "Service", Namespace: "opsi", Name: "api", UID: "uid-service", ResourceVersion: "1", FunctionalHash: strings.Repeat("d", 64)},
		},
	}
	cutoverDepResult := registry.DeploymentResult{
		SchemaVersion: deploymentv1.ResultSchemaVersion,
		Status:        deploymentv1.RolloutStateSucceeded,
		LeaseToken:    cutoverLease.LeaseToken,
		IntentHash:    cutoverIntent.IntentHash,
		RolloutResult: cutoverAgentResult,
	}
	if _, err := server.Registry.(*registry.Service).CompleteDeployment(projectID, nodeID, cutoverPayload.Cutover.DeploymentJobID, "cutover-result", cutoverDepResult); err != nil {
		t.Fatal(err)
	}

	// 3. Request Rollback
	rollbackResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverPayload.Cutover.ID+"/rollbacks", "{}", "rollback-key-1")
	if rollbackResp.Code != http.StatusAccepted {
		t.Fatalf("rollback failed: %d %s", rollbackResp.Code, rollbackResp.Body.String())
	}
	var rollbackPayload struct {
		Rollback cutoverv1.ApplicationCutoverRollback `json:"rollback"`
		Reused   bool                                 `json:"reused"`
	}
	_ = json.Unmarshal(rollbackResp.Body.Bytes(), &rollbackPayload)
	if rollbackPayload.Reused {
		t.Fatal("expected first rollback request to not be reused")
	}
	rollback := rollbackPayload.Rollback
	if rollback.Lifecycle != cutoverv1.RollbackDeploying {
		t.Fatalf("expected rollback lifecycle deploying, got %s", rollback.Lifecycle)
	}
	if len(rollback.Warnings) != 1 || rollback.Warnings[0] != cutoverv1.WarningTargetWritesMayNotBeOnSource {
		t.Fatalf("expected divergence warning in rollback response: %+v", rollback.Warnings)
	}

	// Verify application config is now pointing back to source binding
	cfg, err := server.Registry.GetServiceConfiguration(projectID, appID)
	if err != nil {
		t.Fatalf("failed to get config: %v", err)
	}
	if len(cfg.ResourceBindings) != 1 || cfg.ResourceBindings[0].BindingID != sourceBindingID {
		t.Fatalf("expected DATABASE -> %s after rollback, got %+v", sourceBindingID, cfg.ResourceBindings)
	}

	// 4. Replay Idempotency
	replayResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverPayload.Cutover.ID+"/rollbacks", "{}", "rollback-key-1")
	if replayResp.Code != http.StatusAccepted {
		t.Fatalf("replay rollback failed: %d %s", replayResp.Code, replayResp.Body.String())
	}
	var replayPayload struct {
		Rollback cutoverv1.ApplicationCutoverRollback `json:"rollback"`
		Reused   bool                                 `json:"reused"`
	}
	_ = json.Unmarshal(replayResp.Body.Bytes(), &replayPayload)
	if !replayPayload.Reused || replayPayload.Rollback.ID != rollback.ID {
		t.Fatalf("expected reused rollback with same ID, got %+v", replayPayload)
	}

	// 5. GET Rollback collections
	listResp := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverPayload.Cutover.ID+"/rollbacks", "", "")
	if listResp.Code != http.StatusOK {
		t.Fatalf("list rollbacks failed: %d %s", listResp.Code, listResp.Body.String())
	}

	getResp := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/application-cutover-rollbacks/"+rollback.ID, "", "")
	if getResp.Code != http.StatusOK {
		t.Fatalf("get rollback failed: %d %s", getResp.Code, getResp.Body.String())
	}

	// 6. Complete Rollback from Agent
	compRollback, _ := json.Marshal(cutoverv1.RollbackResult{
		Status: "succeeded",
		VerificationSummary: cutoverv1.RollbackVerificationSummary{
			SourceSQLPreflight:        "PASS",
			TargetSQLPreflight:        "PASS",
			SourceRoleAttributes:      "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:           true,
			WorkloadReady:             true,
			SourceDBConnected:         true,
			SourceMarkerPresent:       true,
			TargetMarkerAbsent:        true,
			PostRollbackSourceWritten: true,
			TargetAuthorityPreserved:  true,
		},
	})
	rbCompReq := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/cutover-rollbacks/"+rollback.ID+"/result?project_id="+projectID, strings.NewReader(string(compRollback)))
	rbCompReq.Header.Set("Authorization", "Bearer agent-secret")
	rbCompReq.Header.Set("Content-Type", "application/json")
	rbCompResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(rbCompResp, rbCompReq)
	if rbCompResp.Code != http.StatusOK {
		t.Fatalf("agent complete rollback failed: %d %s", rbCompResp.Code, rbCompResp.Body.String())
	}

	// 7. Verify Succeeded Rollback
	finalGetResp := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/application-cutover-rollbacks/"+rollback.ID, "", "")
	var finalPayload struct {
		Rollback cutoverv1.ApplicationCutoverRollback `json:"rollback"`
	}
	_ = json.Unmarshal(finalGetResp.Body.Bytes(), &finalPayload)
	if finalPayload.Rollback.Lifecycle != cutoverv1.RollbackSucceeded {
		t.Fatalf("expected succeeded rollback, got %+v", finalPayload.Rollback)
	}
	if err := finalPayload.Rollback.ValidateSucceeded(); err != nil {
		t.Fatalf("rollback validation failed: %v", err)
	}

	// 8. Verify TARGET authority is preserved (NOT deleted, NOT destroyed)
	tgtBinding, err := server.Resources.Store.GetBinding(context.Background(), projectID, targetBindingID)
	if err != nil || tgtBinding.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("target binding must remain ready: %+v err=%v", tgtBinding, err)
	}

	// 9. Verify Audits
	audits, _ := server.Registry.ListAudit(projectID)
	actions := map[string]bool{}
	for _, a := range audits {
		if a.ResourceID == rollback.ID {
			actions[a.Action] = true
		}
	}
	for _, expectedAction := range []string{"CUTOVER_ROLLBACK_REQUESTED", "CUTOVER_ROLLBACK_APPLY_STARTED", "CUTOVER_ROLLBACK_SUCCEEDED"} {
		if !actions[expectedAction] {
			t.Fatalf("missing expected audit action %s: %+v", expectedAction, audits)
		}
	}
}

func TestCutoverFinalizeAPIEndToEndAndAudit(t *testing.T) {
	server, projectID, appID, sourceBindingID, targetBindingID, nodeID, _ := setupCutoverAPITestServer(t)

	// 1. Create and complete Cutover Review
	reviewBody, _ := json.Marshal(cutoverv1.ReviewRequest{
		SourceBindingID: sourceBindingID,
		TargetBindingID: targetBindingID,
	})
	reviewResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutover-reviews", string(reviewBody), "rev-key-1")
	if reviewResp.Code != http.StatusAccepted {
		t.Fatalf("review failed: %d %s", reviewResp.Code, reviewResp.Body.String())
	}
	var revPayload struct {
		Review cutoverv1.ApplicationCutoverReview `json:"review"`
	}
	_ = json.Unmarshal(reviewResp.Body.Bytes(), &revPayload)

	// Agent claims and completes review
	agentReq := httptest.NewRequest(http.MethodGet, "/v1/agents/"+nodeID+"/webhooks/next?project_id="+projectID+"&wait=0s", nil)
	agentReq.Header.Set("Authorization", "Bearer agent-secret")
	agentResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(agentResp, agentReq)
	var leasePayload struct {
		LeaseToken string `json:"lease_token"`
	}
	_ = json.Unmarshal(agentResp.Body.Bytes(), &leasePayload)

	compReview, _ := json.Marshal(cutoverv1.ReviewResult{
		Status:               cutoverv1.ReviewSucceeded,
		LeaseToken:           leasePayload.LeaseToken,
		SourceSQLPreflight:   "PASS",
		TargetSQLPreflight:   "PASS",
		TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		ValidationSummary: cutoverv1.ValidationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			SourceBindingReady:   true,
			TargetBindingReady:   true,
			TargetRestoreReady:   true,
		},
	})
	revCompReq := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/cutover-reviews/"+revPayload.Review.ID+"/result?project_id="+projectID, strings.NewReader(string(compReview)))
	revCompReq.Header.Set("Authorization", "Bearer agent-secret")
	revCompReq.Header.Set("Content-Type", "application/json")
	revCompResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(revCompResp, revCompReq)
	if revCompResp.Code != http.StatusOK {
		t.Fatalf("complete review failed: %d %s", revCompResp.Code, revCompResp.Body.String())
	}

	// 2. Apply Cutover
	applyBody, _ := json.Marshal(cutoverv1.ApplyRequest{
		CutoverReviewID: revPayload.Review.ID,
	})
	applyResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers", string(applyBody), "apply-key-1")
	if applyResp.Code != http.StatusAccepted {
		t.Fatalf("apply cutover failed: %d %s", applyResp.Code, applyResp.Body.String())
	}
	var cutoverPayload struct {
		Cutover cutoverv1.ApplicationCutover `json:"cutover"`
	}
	_ = json.Unmarshal(applyResp.Body.Bytes(), &cutoverPayload)
	cutoverID := cutoverPayload.Cutover.ID

	// Complete Cutover via Agent Result
	compCutover, _ := json.Marshal(cutoverv1.CutoverApplyResult{
		Status: "succeeded",
		VerificationSummary: cutoverv1.CutoverVerificationSummary{
			SourceSQLPreflight:       "PASS",
			TargetSQLPreflight:       "PASS",
			TargetRoleAttributes:     "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:          true,
			WorkloadReady:            true,
			TargetDBConnected:        true,
			RestoredDataVerified:     true,
			TargetOnlyMarkerPresent:  true,
			SourceOnlyMarkerAbsent:   true,
			PostCutoverTargetWritten: true,
			SourceRollbackPreserved:  true,
		},
	})
	cutCompReq := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID+"/cutovers/"+cutoverID+"/result?project_id="+projectID, strings.NewReader(string(compCutover)))
	cutCompReq.Header.Set("Authorization", "Bearer agent-secret")
	cutCompReq.Header.Set("Content-Type", "application/json")
	cutCompResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(cutCompResp, cutCompReq)
	if cutCompResp.Code != http.StatusOK {
		t.Fatalf("agent complete cutover failed: %d %s", cutCompResp.Code, cutCompResp.Body.String())
	}

	// 3. Request Finalization
	fnResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverID+"/finalize", "", "fn-key-1")
	if fnResp.Code != http.StatusAccepted {
		t.Fatalf("finalize cutover failed: %d %s", fnResp.Code, fnResp.Body.String())
	}
	var fnPayload struct {
		Finalization cutoverv1.ApplicationCutoverFinalization `json:"finalization"`
		Reused       bool                                     `json:"reused"`
	}
	if err := json.Unmarshal(fnResp.Body.Bytes(), &fnPayload); err != nil {
		t.Fatalf("failed to unmarshal finalization: %v", err)
	}
	if fnPayload.Reused {
		t.Fatal("expected reused=false on initial finalize")
	}
	finalization := fnPayload.Finalization
	if finalization.Lifecycle != cutoverv1.FinalizationSucceeded {
		t.Fatalf("expected succeeded finalization, got %+v", finalization)
	}
	if err := finalization.ValidateSucceeded(); err != nil {
		t.Fatalf("finalization validation failed: %v", err)
	}

	// 4. Replay with same Idempotency-Key
	replayResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverID+"/finalize", "", "fn-key-1")
	if replayResp.Code != http.StatusAccepted {
		t.Fatalf("finalize replay failed: %d %s", replayResp.Code, replayResp.Body.String())
	}
	var replayPayload struct {
		Finalization cutoverv1.ApplicationCutoverFinalization `json:"finalization"`
		Reused       bool                                     `json:"reused"`
	}
	_ = json.Unmarshal(replayResp.Body.Bytes(), &replayPayload)
	if !replayPayload.Reused || replayPayload.Finalization.ID != finalization.ID {
		t.Fatalf("expected reused=true with same ID %s: %+v", finalization.ID, replayPayload)
	}

	// 5. Query GET routes
	get1 := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverID+"/finalizations", "", "")
	if get1.Code != http.StatusOK || !strings.Contains(get1.Body.String(), finalization.ID) {
		t.Fatalf("list finalizations failed: %d %s", get1.Code, get1.Body.String())
	}

	get2 := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverID+"/finalizations/"+finalization.ID, "", "")
	if get2.Code != http.StatusOK || !strings.Contains(get2.Body.String(), finalization.ID) {
		t.Fatalf("get single finalization failed: %d %s", get2.Code, get2.Body.String())
	}

	get3 := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/application-cutover-finalizations", "", "")
	if get3.Code != http.StatusOK || !strings.Contains(get3.Body.String(), finalization.ID) {
		t.Fatalf("list project finalizations failed: %d %s", get3.Code, get3.Body.String())
	}

	get4 := requestResourceAPI(t, server, http.MethodGet, "/api/projects/"+projectID+"/application-cutover-finalizations/"+finalization.ID, "", "")
	if get4.Code != http.StatusOK || !strings.Contains(get4.Body.String(), finalization.ID) {
		t.Fatalf("get single project finalization failed: %d %s", get4.Code, get4.Body.String())
	}

	// 6. Verify SOURCE binding was revoked (deleted or transitioned out of Ready)
	srcBinding, err := server.Resources.Store.GetBinding(context.Background(), projectID, sourceBindingID)
	if err == nil && srcBinding.Lifecycle == resourcev1.LifecycleReady {
		t.Fatalf("source binding must not be ready after finalization: %+v", srcBinding)
	}

	// 7. Verify TARGET binding remains Ready and unchanged
	tgtBinding, err := server.Resources.Store.GetBinding(context.Background(), projectID, targetBindingID)
	if err != nil || tgtBinding.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("target binding must remain ready: %+v err=%v", tgtBinding, err)
	}

	// 8. Verify Audits
	audits, _ := server.Registry.ListAudit(projectID)
	actions := map[string]bool{}
	for _, a := range audits {
		if a.ResourceID == finalization.ID {
			actions[a.Action] = true
		}
	}
	for _, expectedAction := range []string{"CUTOVER_FINALIZE_REQUESTED", "CUTOVER_FINALIZE_VALIDATED", "CUTOVER_SOURCE_BINDING_REVOKE_STARTED", "CUTOVER_FINALIZED"} {
		if !actions[expectedAction] {
			t.Fatalf("missing expected audit action %s: %+v", expectedAction, audits)
		}
	}

	// 9. Verify subsequent rollback attempt on finalized cutover is rejected
	rbResp := requestResourceAPI(t, server, http.MethodPost, "/api/projects/"+projectID+"/applications/"+appID+"/cutovers/"+cutoverID+"/rollbacks", "", "rb-post-fn")
	if rbResp.Code != http.StatusBadRequest || !strings.Contains(rbResp.Body.String(), cutoverv1.FailureCutoverFinalized) {
		t.Fatalf("expected rollback rejection with %q, got: %d %s", cutoverv1.FailureCutoverFinalized, rbResp.Code, rbResp.Body.String())
	}
}
