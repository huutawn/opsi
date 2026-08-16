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
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
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
		Kind            string                   `json:"kind"`
		LeaseToken      string                   `json:"lease_token"`
		Review          cutoverv1.ApplicationCutoverReview `json:"review"`
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
	if appConfig.Revision != 0 {
		t.Fatalf("application config mutated during review: %+v", appConfig)
	}
	srcBinding, _ := server.Resources.Store.GetBinding(context.Background(), projectID, sourceBindingID)
	if srcBinding.Target.ID != "res-source" || srcBinding.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("source binding mutated during review: %+v", srcBinding)
	}
	deployments, _ := server.Registry.ListDeployments(projectID)
	if len(deployments) != 0 {
		t.Fatalf("deployments created during review: %+v", deployments)
	}
}
