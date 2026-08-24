package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func TestDecodeStrictDeploymentJSONRejectsRawKubernetesAndUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"opsi.deployment_job/v1","build_record_id":"br-1","environment_id":"env-1","raw_yaml":"apiVersion: v1","workload":{}}`,
		`{"schema_version":"opsi.deployment_job/v1","build_record_id":"br-1","environment_id":"env-1","workload":{"schema_version":"opsi.workload_spec/v1","service_key":"api","replicas":1,"application_container_name":"app","container_port":8080,"resources":{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"cpu":"500m","memory":"512Mi"}},"termination_grace_period_seconds":30,"exposure":{"mode":"internal"},"hostNetwork":true}}`,
	} {
		request := httptest.NewRequest("POST", "/api/projects/proj/deployments/preview", bytes.NewBufferString(body))
		response := httptest.NewRecorder()
		var value deploymentv1.CreateRequest
		if decodeStrictDeploymentJSON(response, request, &value) {
			t.Fatalf("accepted unsafe deployment body: %s", body)
		}
		if response.Code != 400 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestDeploymentPayloadHashNormalizesEnvironmentOrder(t *testing.T) {
	first := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "api", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Environment: []deploymentv1.EnvironmentVariable{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	second := first
	second.Environment = []deploymentv1.EnvironmentVariable{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	snapshot := func(workload deploymentv1.WorkloadSpec) deploymentv1.JobSnapshot {
		return deploymentv1.JobSnapshot{Workload: workload, Authority: deploymentv1.AuthoritySnapshot{BuildRecord: buildrecordv1.Record{ID: "br-1"}, EnvironmentID: "env-1", TopologyRevision: 1, TopologyHash: strings.Repeat("1", 64), ServiceConfigurationStateHash: strings.Repeat("2", 64), DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("3", 64), RoutingDecisionHash: strings.Repeat("4", 64)}}
	}
	if hashDeploymentPayload(snapshot(first)) != hashDeploymentPayload(snapshot(second)) {
		t.Fatal("normalized replay payload hashes differ")
	}
	second.ContainerPort++
	if hashDeploymentPayload(snapshot(first)) == hashDeploymentPayload(snapshot(second)) {
		t.Fatal("conflicting replay payload hashes match")
	}
}

func TestDeploymentIdempotencyKeyIsBoundedAndWhitespaceFree(t *testing.T) {
	for _, value := range []string{"", "has space", "line\nbreak", string(make([]byte, 129))} {
		if validDeploymentIdempotencyKey(value) {
			t.Fatalf("accepted invalid idempotency key %q", value)
		}
	}
	if !validDeploymentIdempotencyKey("r5-010:api:immutable-001") {
		t.Fatal("rejected valid bounded idempotency key")
	}
}

func TestDeploymentAssignmentUsesServiceAndEnvironmentAuthority(t *testing.T) {
	assignments := []topologyv1.Assignment{
		{ServiceKey: "api", EnvironmentID: "env-staging", RuntimeID: "runtime-staging"},
		{ServiceKey: "api", EnvironmentID: "env-production", RuntimeID: "runtime-production"},
	}
	assignment, ok := deploymentAssignment(assignments, "api", "env-production")
	if !ok || assignment.RuntimeID != "runtime-production" {
		t.Fatalf("assignment=%+v ok=%v", assignment, ok)
	}
	if _, ok := deploymentAssignment(assignments, "api", ""); ok {
		t.Fatal("missing environment selected an assignment")
	}
}

func TestResolvedDeploymentCompilesCanonicalSnapshotAndRejectsStaleOrClientSpec(t *testing.T) {
	server, projectID, service, plan, policy := deploymentResolutionFixture(t)
	configuration, err := server.Registry.GetServiceConfiguration(projectID, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := deploymentv1.CreateRequest{SchemaVersion: deploymentv1.JobSchemaVersion, BuildRecordID: "br-resolved", EnvironmentID: plan.Assignments[0].EnvironmentID, ExpectedTopologyRevision: plan.Revision, ExpectedTopologyHash: plan.PlanHash, ExpectedConfigurationRevision: configuration.Revision, ExpectedConfigurationStateHash: configuration.StateHash}
	preview, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible || preview.Snapshot.Authority.ServiceConfigurationRevision != configuration.Revision || preview.Snapshot.Authority.ServiceConfigurationStateHash != configuration.StateHash {
		t.Fatalf("configuration authority missing from snapshot: %+v", preview.Snapshot.Authority)
	}
	workload := preview.Snapshot.Workload
	if workload.Replicas != plan.Assignments[0].Replicas || workload.Resources.Requests.CPU != "250m" || workload.Resources.Limits.Memory != "256Mi" || workload.ReadinessProbe == nil || workload.LivenessProbe == nil || workload.ReadinessProbe.Path != service.HealthPath {
		t.Fatalf("canonical workload=%+v", workload)
	}
	client := request
	client.ExpectedTopologyRevision, client.ExpectedTopologyHash, client.ExpectedConfigurationStateHash = 0, "", ""
	client.Workload = &workload
	client.Workload.Replicas++
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", client); deploymentAPIErrorCode(err) != "WORKLOAD_CANONICAL_MISMATCH" {
		t.Fatalf("client mismatch err=%v", err)
	}
	client = request
	omittedProbeWorkload := preview.Snapshot.Workload
	client.Workload = &omittedProbeWorkload
	client.Workload.ReadinessProbe = nil
	client.Workload.LivenessProbe = nil
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", client); err != nil {
		t.Fatalf("client with omitted platform probes err=%v", err)
	}
	_, err = server.Registry.ApplyServiceConfiguration(projectID, service.ID, "owner", "config-change", registry.ServiceConfigurationApplyRequest{Draft: registry.ServiceConfigurationDraft{Environment: []deploymentv1.EnvironmentVariable{{Name: "LOG_LEVEL", Value: "debug"}}}, ExpectedRevision: configuration.Revision, ExpectedStateHash: configuration.StateHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request); deploymentAPIErrorCode(err) != "CONFIGURATION_REVIEW_STALE" {
		t.Fatalf("stale configuration err=%v", err)
	}
	request.ExpectedConfigurationRevision++
	updated, _ := server.Registry.GetServiceConfiguration(projectID, service.ID)
	request.ExpectedConfigurationStateHash = updated.StateHash
	request.ExpectedDeploymentPolicyRevision, request.ExpectedDeploymentPolicyHash = policy.Revision, policy.PolicyHash
	draft := policy.Draft
	draft.AllowUnknownCapacity = true
	if _, err := server.Policies.Apply(context.Background(), projectID, "owner", "policy-change", deploymentpolicyv1.ApplyRequest{PolicyID: policy.ID, Draft: draft, ExpectedRevision: policy.Revision, ExpectedStateHash: policy.StateHash}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request); deploymentAPIErrorCode(err) != "POLICY_REVIEW_STALE" {
		t.Fatalf("stale policy err=%v", err)
	}
}

func TestR5017Run2PublicTopologyWithValidRouteCompilesInternalWorkload(t *testing.T) {
	server, projectID, service, plan, policy := deploymentResolutionFixture(t)
	configuration, err := server.Registry.GetServiceConfiguration(projectID, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: projectID, Assignments: append([]topologyv1.Assignment(nil), plan.Assignments...)}
	draft.Assignments[0].Exposure.Mode = "public"
	topologyResult, err := server.Topology.Apply(context.Background(), projectID, "owner", "public-topology", topologyv1.ApplyRequest{Draft: draft, ExpectedRevision: plan.Revision, ExpectedStateHash: plan.StateHash, PolicyID: policy.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	request := deploymentv1.CreateRequest{SchemaVersion: deploymentv1.JobSchemaVersion, BuildRecordID: "br-resolved", EnvironmentID: draft.Assignments[0].EnvironmentID, ExpectedTopologyRevision: topologyResult.Plan.Revision, ExpectedTopologyHash: topologyResult.Plan.PlanHash, ExpectedConfigurationRevision: configuration.Revision, ExpectedConfigurationStateHash: configuration.StateHash}
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request); deploymentAPIErrorCode(err) != "PUBLIC_ROUTE_REQUIRED" {
		t.Fatalf("public preview without route err=%v", err)
	}
	configured, err := server.Registry.ApplyServiceConfiguration(projectID, service.ID, "owner", "public-route", registry.ServiceConfigurationApplyRequest{Draft: registry.ServiceConfigurationDraft{PublicRoute: &registry.PublicRouteIntent{Hostname: "apps.example.com", Path: "/api"}}, ExpectedRevision: configuration.Revision, ExpectedStateHash: configuration.StateHash})
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedConfigurationRevision = configured.Configuration.Revision
	request.ExpectedConfigurationStateHash = configured.Configuration.StateHash
	preview, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request)
	if deploymentAPIErrorCode(err) == "PUBLIC_EXPOSURE_UNSUPPORTED" {
		t.Fatalf("Run 2 regression: valid public topology was rejected: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible || preview.Snapshot.Workload.Exposure.Mode != "internal" || topologyResult.Plan.Assignments[0].Exposure.Mode != "public" || configured.Configuration.PublicRoute == nil || configured.Configuration.PublicRoute.Hostname != "apps.example.com" || configured.Configuration.PublicRoute.Path != "/api" {
		t.Fatalf("preview=%+v topology=%+v configuration=%+v", preview, topologyResult.Plan.Assignments[0], configured.Configuration)
	}
	legacy := request
	legacy.Workload = &preview.Snapshot.Workload
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", legacy); err != nil {
		t.Fatalf("canonical internal legacy workload rejected: %v", err)
	}
	publicWorkload := preview.Snapshot.Workload
	publicWorkload.Exposure.Mode = "public"
	legacy.Workload = &publicWorkload
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", legacy); deploymentAPIErrorCode(err) != "WORKLOAD_SPEC_INVALID" {
		t.Fatalf("legacy public workload err=%v", err)
	}
}

func TestResolvedDeploymentRejectsTopologyChangedAfterReview(t *testing.T) {
	server, projectID, service, plan, policy := deploymentResolutionFixture(t)
	configuration, err := server.Registry.GetServiceConfiguration(projectID, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := deploymentv1.CreateRequest{SchemaVersion: deploymentv1.JobSchemaVersion, BuildRecordID: "br-resolved", EnvironmentID: plan.Assignments[0].EnvironmentID, ExpectedTopologyRevision: plan.Revision, ExpectedTopologyHash: plan.PlanHash, ExpectedConfigurationRevision: configuration.Revision, ExpectedConfigurationStateHash: configuration.StateHash}
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: projectID, Assignments: append([]topologyv1.Assignment(nil), plan.Assignments...)}
	draft.Assignments[0].Replicas++
	if _, err := server.Topology.Apply(context.Background(), projectID, "owner", "topology-change", topologyv1.ApplyRequest{Draft: draft, ExpectedRevision: plan.Revision, ExpectedStateHash: plan.StateHash, PolicyID: policy.ID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request); deploymentAPIErrorCode(err) != "TOPOLOGY_REVIEW_STALE" {
		t.Fatalf("stale topology err=%v", err)
	}
}

func deploymentResolutionFixture(t *testing.T) (*Server, string, registry.ServiceRecord, topologyv1.Plan, deploymentpolicyv1.Policy) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	store := registry.NewService()
	project, err := store.CreateProject("org-1", "Resolved", "resolved", "owner", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.UpsertNode(project.ID, "node-1", "server", registry.NodeHealthy, "203.0.113.20", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.RegisterAgent(project.ID, node.ID, "sha256:agent", "hash", "v1", "agent-key", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordAgentHeartbeat(project.ID, node.ID, registry.AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	service, err := store.CreateService(project.ID, registry.ServiceDraft{Name: "api", Type: "application", SourceType: "git", RepoURL: "https://example.test/api.git", Branch: "main", GitSHA: strings.Repeat("a", 40), BuildContext: ".", Dockerfile: "Dockerfile", ManifestPath: "deploy/api.yaml", ContainerPort: 8080, HealthPath: "/healthz", Replicas: 9, ResourceRequests: map[string]string{"cpu": "900m", "memory": "900Mi"}}, "service-key")
	if err != nil {
		t.Fatal(err)
	}
	facts := topology.Facts{
		ProjectID:    project.ID,
		Environments: []topology.EnvironmentFact{{ID: node.EnvironmentID, ProjectID: project.ID, Status: "active"}},
		Runtimes:     []topology.RuntimeFact{{ID: node.RuntimeID, ProjectID: project.ID, EnvironmentID: node.EnvironmentID, Type: "k3s", Status: "ready"}},
		Services:     []topology.ServiceFact{{ID: service.ID, ProjectID: project.ID, Key: service.Name}},
		Nodes:        []topology.NodeFact{{ID: node.ID, ProjectID: project.ID, RuntimeID: node.RuntimeID, Status: "healthy", CPUCores: 2, MemoryMB: 2048, LastSeenAt: &now}},
		Agents:       []topology.AgentFact{{ID: agent.ID, ProjectID: project.ID, RuntimeID: node.RuntimeID, NodeID: node.ID, Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &now}},
	}
	topologyService := topology.Service{Store: topology.NewMemoryStore(), Facts: placementAPIFacts{facts}, Now: func() time.Time { return now }}
	topologyDraft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: project.ID, Assignments: []topologyv1.Assignment{{ServiceKey: service.Name, EnvironmentID: node.EnvironmentID, RuntimeID: node.RuntimeID, Replicas: 2, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}}}
	if validation, err := topologyService.Validate(ctx, project.ID, topologyDraft, true); err != nil || !validation.Valid {
		t.Fatalf("topology validation=%+v err=%v", validation, err)
	}
	topologyResult, err := topologyService.Apply(ctx, project.ID, "owner", "topology-key", topologyv1.ApplyRequest{Draft: topologyDraft}, true)
	if err != nil {
		t.Fatal(err)
	}
	record := buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-resolved", ProjectID: project.ID, RepositoryID: 7, RepositoryOwnerID: 8, ActiveBindingID: "binding-1", ServiceID: service.ID, ServiceKey: service.Name, CreatedAt: now, Workload: buildrecordv1.WorkloadIdentity{RepositoryID: 7, RepositoryOwnerID: 8, Ref: "refs/heads/main", EventName: "push", WorkflowRef: "o/r/.github/workflows/cd.yml@refs/heads/main", RunID: 1, RunAttempt: 1}, Build: buildrecordv1.BuildMetadata{ConfigHash: strings.Repeat("a", 64), PlanHash: strings.Repeat("b", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/o/r/api", OCIDigest: "sha256:" + strings.Repeat("c", 64), Status: "succeeded"}}
	recordStore := buildrecord.NewMemoryStore()
	if _, _, err := recordStore.Create(ctx, "payload", record); err != nil {
		t.Fatal(err)
	}
	binding := placementAPIBindings{buildrecord.Binding{ProjectID: project.ID, BindingID: record.ActiveBindingID, ServiceID: service.ID, ServiceKey: service.Name, RepositoryID: record.RepositoryID, RepositoryOwnerID: record.RepositoryOwnerID}}
	policyService := deploymentpolicy.Service{Store: deploymentpolicy.NewMemoryStore(), BuildRecords: recordStore, Bindings: binding, Topology: topologyService, Now: func() time.Time { return now }}
	policyResult, err := policyService.Apply(ctx, project.ID, "owner", "policy-key", deploymentpolicyv1.ApplyRequest{Draft: deploymentpolicyv1.Draft{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: project.ID, RepositoryID: record.RepositoryID, ServiceKeys: []string{service.Name}, WorkflowRefs: []string{record.Workload.WorkflowRef}, AllowedEvents: []string{record.Workload.EventName}, AllowedGitRefs: []string{record.Workload.Ref}, EnvironmentID: node.EnvironmentID, AllowedRuntimeIDs: []string{node.RuntimeID}, AllowedOCIRepositories: []string{record.Build.OCIRepository}, AllowedPlatforms: []string{record.Build.Platform}, AllowedConfigHashes: []string{record.Build.ConfigHash}, AllowedBuildPlanHashes: []string{record.Build.PlanHash}, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{})
	server.Registry = store
	server.BuildRecords = buildrecord.Service{Store: recordStore}
	server.Topology = topologyService
	server.Policies = policyService
	return server, project.ID, service, topologyResult.Plan, policyResult.Policy
}

func deploymentAPIErrorCode(err error) string {
	if value, ok := err.(registry.APIError); ok {
		return value.Code
	}
	return ""
}

func TestExposureAPIIsProjectScopedStrictIdempotentAndSanitized(t *testing.T) {
	server := NewServer(Config{})
	store := server.Registry.(*registry.Service)
	project, err := store.CreateProject("org-1", "Exposure", "exposure", "owner", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.UpsertNode(project.ID, "node-1", "server", registry.NodeHealthy, "203.0.113.10", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.RegisterAgent(project.ID, node.ID, "sha256:test", "hash", "v1", "agent-key", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordAgentHeartbeat(project.ID, node.ID, registry.AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	service, err := store.CreateService(project.ID, registry.ServiceDraft{Name: "api", Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: strings.Repeat("a", 40), BuildContext: "services/api", Dockerfile: "Dockerfile", ManifestPath: "deploy/api.yaml"}, "service-key")
	if err != nil {
		t.Fatal(err)
	}
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: service.Name, Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	workloadHash, _ := workload.Hash()
	image, _ := deploymentv1.NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	snapshot := deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: project.ID, Image: image, Workload: workload, SpecHash: workloadHash, PayloadHash: "base-payload", Authority: deploymentv1.AuthoritySnapshot{BuildRecord: buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-1", ProjectID: project.ID, ServiceID: service.ID, ServiceKey: service.Name, ActiveBindingID: "binding-1", Build: buildrecordv1.BuildMetadata{OCIRepository: image.Repository, OCIDigest: image.Digest, Status: "succeeded"}}, TopologyPlanID: "topology-1", TopologyRevision: 1, TopologyHash: strings.Repeat("1", 64), ServiceConfigurationRevision: service.Configuration.Revision, ServiceConfigurationStateHash: service.Configuration.StateHash, DeploymentPolicyID: "policy-1", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("2", 64), RoutingDecisionHash: strings.Repeat("3", 64), EnvironmentID: service.EnvironmentID, RuntimeID: service.RuntimeID, NodeID: node.ID, AgentID: agent.ID}}
	baseJob, _, err := store.StartImmutableDeployment(snapshot, "owner", "base-key", "base-request")
	if err != nil {
		t.Fatal(err)
	}
	baseLease, ok, err := store.LeaseDeployment(project.ID, node.ID)
	if err != nil || !ok {
		t.Fatalf("base lease ok=%v err=%v", ok, err)
	}
	for index, state := range []string{deploymentv1.RolloutStateApplying, deploymentv1.RolloutStateWaiting, deploymentv1.RolloutStateSucceeded} {
		currentDigest := ""
		if state == deploymentv1.RolloutStateSucceeded {
			currentDigest = image.Digest
		}
		progress := deploymentv1.Progress{SchemaVersion: deploymentv1.EventSchemaVersion, LeaseToken: baseLease.LeaseToken, State: state, RolloutID: baseJob.RolloutIntent.RolloutID, IntentHash: baseJob.IntentHash, StateHash: strings.Repeat(string(rune('a'+index)), 64), WorkloadSpecHash: baseJob.RolloutIntent.Desired.WorkloadSpecHash, ExposureSpecHash: baseJob.RolloutIntent.Desired.ExposureSpecHash, DesiredDigest: image.Digest, CurrentDigest: currentDigest, PreviousDigest: baseJob.PreviousDigest, Attempt: baseJob.RolloutIntent.Attempt}
		if _, err := store.ProgressImmutableDeployment(project.ID, node.ID, baseJob.ID, "base-progress", progress); err != nil {
			t.Fatal(err)
		}
	}
	baseResult := &deploymentv1.AgentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: deploymentv1.RolloutStateSucceeded, RolloutState: deploymentv1.RolloutStateSucceeded, RolloutID: baseJob.RolloutIntent.RolloutID, IntentHash: baseJob.IntentHash, StateHash: strings.Repeat("c", 64), SpecHash: snapshot.SpecHash, WorkloadSpecHash: baseJob.RolloutIntent.Desired.WorkloadSpecHash, ExposureSpecHash: baseJob.RolloutIntent.Desired.ExposureSpecHash, DesiredDigest: image.Digest, CurrentDigest: image.Digest, KnownGoodID: baseJob.RolloutIntent.RolloutID, KnownGoodHash: strings.Repeat("d", 64), ReadinessEvidenceHash: strings.Repeat("e", 64), Attempt: baseJob.RolloutIntent.Attempt, Resources: []deploymentv1.ResourceIdentity{{Kind: "Deployment", Name: "api", UID: "uid", ResourceVersion: "1", FunctionalHash: strings.Repeat("f", 64)}}}
	base, err := store.CompleteDeployment(project.ID, node.ID, baseJob.ID, "base-result", registry.DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: deploymentv1.RolloutStateSucceeded, LeaseToken: baseLease.LeaseToken, IntentHash: baseJob.IntentHash, RolloutResult: baseResult})
	if err != nil {
		t.Fatal(err)
	}
	exposure, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: project.ID, EnvironmentID: base.EnvironmentID, RuntimeID: base.RuntimeID, ServiceKey: workload.ServiceKey, DeploymentJobID: "dep-exposure", Hostname: "api.example.com", Path: "/", ServicePort: workload.ContainerPort, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}, Metadata: &exposurev1.Metadata{DisplayName: "Public API", Rationale: "request-body-secret-marker"}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	mutation := deploymentv1.ExposureMutationRequest{SchemaVersion: deploymentv1.ExposureMutationVersion, BaseDeploymentJobID: base.ID, Exposure: exposure}
	body, _ := json.Marshal(mutation)
	ownerHash, _ := auth.HashPAT("owner-pat")
	viewerHash, _ := auth.HashPAT("viewer-pat")
	foreignHash, _ := auth.HashPAT("foreign-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "Owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{UserID: "viewer", OrgID: "org-1", ProjectID: project.ID, Role: "Viewer", Hash: viewerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{UserID: "foreign", OrgID: "org-2", ProjectID: "foreign-project", Role: "Owner", Hash: foreignHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	handler := server.Handler()

	call := func(method, path, token, key string, requestBody []byte) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewReader(requestBody))
		request.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
			request.Header.Set("X-Request-ID", "request-1")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	preview := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures/preview", "viewer-pat", "", body)
	if preview.Code != http.StatusOK || !bytes.Contains(preview.Body.Bytes(), []byte(`"eligible":true`)) {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	viewerApply := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "viewer-pat", "viewer-key", body)
	if viewerApply.Code != http.StatusForbidden {
		t.Fatalf("viewer apply status=%d body=%s", viewerApply.Code, viewerApply.Body.String())
	}
	missingKey := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "", body)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing key status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}
	created := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "exposure-key", body)
	if created.Code != http.StatusAccepted || bytes.Contains(created.Body.Bytes(), []byte("owner-pat")) || bytes.Contains(created.Body.Bytes(), []byte("lease_token")) || bytes.Contains(created.Body.Bytes(), []byte("raw_manifest")) {
		t.Fatalf("created status=%d body=%s", created.Code, created.Body.String())
	}
	countCreatedAudits := func() int {
		audits, err := store.ListAudit(project.ID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, audit := range audits {
			if audit.Action == "EXPOSURE_ROLLOUT_CREATED" && audit.ResourceID == exposure.DeploymentJobID {
				count++
			}
		}
		return count
	}
	if count := countCreatedAudits(); count != 1 {
		t.Fatalf("initial created audit count=%d", count)
	}
	var createdJob registry.DeploymentJob
	if err := json.Unmarshal(created.Body.Bytes(), &createdJob); err != nil || createdJob.ID != exposure.DeploymentJobID || createdJob.Reused {
		t.Fatalf("created job=%+v err=%v", createdJob, err)
	}
	audits, err := store.ListAudit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits {
		if audit.Action != "EXPOSURE_ROLLOUT_CREATED" || audit.ResourceID != exposure.DeploymentJobID {
			continue
		}
		metadata, _ := json.Marshal(audit.MetadataRedacted)
		if len(audit.MetadataRedacted) != 5 || bytes.Contains(metadata, body) || bytes.Contains(metadata, []byte("owner-pat")) || bytes.Contains(metadata, []byte("request-body-secret-marker")) {
			t.Fatalf("unsafe creation audit metadata=%s", metadata)
		}
	}
	events, err := store.DeploymentEvents(project.ID, exposure.DeploymentJobID)
	if err != nil {
		t.Fatal(err)
	}
	replay := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "exposure-key", body)
	if replay.Code != http.StatusAccepted || !bytes.Contains(replay.Body.Bytes(), []byte(`"reused":true`)) {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayJob registry.DeploymentJob
	if err := json.Unmarshal(replay.Body.Bytes(), &replayJob); err != nil || replayJob.ID != createdJob.ID || !replayJob.Reused {
		t.Fatalf("replay job=%+v err=%v", replayJob, err)
	}
	if count := countCreatedAudits(); count != 1 {
		t.Fatalf("replay created audit count=%d", count)
	}

	const concurrentReplays = 8
	responses := make(chan *httptest.ResponseRecorder, concurrentReplays)
	var wait sync.WaitGroup
	for range concurrentReplays {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "exposure-key", body)
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		var job registry.DeploymentJob
		if err := json.Unmarshal(response.Body.Bytes(), &job); response.Code != http.StatusAccepted || err != nil || job.ID != createdJob.ID || !job.Reused {
			t.Fatalf("concurrent replay status=%d job=%+v err=%v", response.Code, job, err)
		}
	}
	if count := countCreatedAudits(); count != 1 {
		t.Fatalf("concurrent replay created audit count=%d", count)
	}

	conflicting := mutation
	conflicting.Exposure.Hostname = "other.example.com"
	conflicting.Exposure.SpecHash = ""
	conflicting.Exposure, err = conflicting.Exposure.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	conflictingBody, _ := json.Marshal(conflicting)
	conflict := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "exposure-key", conflictingBody)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if count := countCreatedAudits(); count != 1 {
		t.Fatalf("conflict created audit count=%d", count)
	}
	afterEvents, err := store.DeploymentEvents(project.ID, exposure.DeploymentJobID)
	if err != nil || len(afterEvents) != len(events) {
		t.Fatalf("replay events=%d initial=%d err=%v", len(afterEvents), len(events), err)
	}
	detail := call(http.MethodGet, "/api/projects/"+project.ID+"/exposures/"+exposure.DeploymentJobID, "viewer-pat", "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	foreign := call(http.MethodGet, "/api/projects/"+project.ID+"/exposures/"+exposure.DeploymentJobID, "foreign-pat", "", nil)
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign detail status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	unsafe := append(bytes.TrimSuffix(body, []byte("}")), []byte(`,"raw_manifest":"apiVersion: v1"}`)...)
	strict := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures/preview", "owner-pat", "", unsafe)
	if strict.Code != http.StatusBadRequest {
		t.Fatalf("unsafe status=%d body=%s", strict.Code, strict.Body.String())
	}
}

func TestResolvedDeploymentRejectsStaleBuildPhaseDependency(t *testing.T) {
	server, projectID, service, plan, _ := deploymentResolutionFixture(t)
	// Add an API backend service that the service depends on at build time
	apiService, err := server.Registry.CreateService(projectID, registry.ServiceDraft{Name: "backend-api", ContainerPort: 8080}, "api-key")
	if err != nil {
		t.Fatal(err)
	}
	apiCfg, err := server.Registry.GetServiceConfiguration(projectID, apiService.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Give API service a public route
	if _, err := server.Registry.ApplyServiceConfiguration(projectID, apiService.ID, "owner", "api-key", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			PublicRoute: &registry.PublicRouteIntent{Hostname: "api.example.com", Path: "/v1"},
		},
		ExpectedRevision:  apiCfg.Revision,
		ExpectedStateHash: apiCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	// Configure build-phase public_http dependency on service
	svcCfg, err := server.Registry.GetServiceConfiguration(projectID, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	dep := serviceconfigurationv1.PublicHTTPPreset("backend", apiService.ID, serviceconfigurationv1.AccessContextBrowser, "BACKEND_URL", true)
	dep.InjectionPhase = serviceconfigurationv1.InjectionPhaseBuild
	appliedSvcCfg, err := server.Registry.ApplyServiceConfiguration(projectID, service.ID, "owner", "svc-key", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{dep},
		},
		ExpectedRevision:  svcCfg.Revision,
		ExpectedStateHash: svcCfg.StateHash,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Compute expected build record with matching build dependency state
	services, _ := server.Registry.ListServices(projectID)
	buildDepState := registry.ComputeBuildDependencyState(appliedSvcCfg.Configuration, services)
	configHash := registry.ComputeBuildConfigHash(strings.Repeat("a", 40), "", service.Dockerfile, service.BuildContext, "ghcr.io/o/r/api", buildDepState)

	record := buildrecordv1.Record{
		SchemaVersion:     buildrecordv1.SchemaVersion,
		ID:                "br-build-dep-fresh",
		ProjectID:         projectID,
		RepositoryID:      7,
		RepositoryOwnerID: 8,
		ActiveBindingID:   "binding-1",
		ServiceID:         service.ID,
		ServiceKey:        service.Name,
		CreatedAt:         time.Now().UTC(),
		Workload: buildrecordv1.WorkloadIdentity{
			RepositoryID:      7,
			RepositoryOwnerID: 8,
			Ref:               "refs/heads/main",
			SHA:               strings.Repeat("a", 40),
			EventName:         "push",
			WorkflowRef:       "o/r/.github/workflows/cd.yml@refs/heads/main",
		},
		Build: buildrecordv1.BuildMetadata{
			ConfigHash:    configHash,
			PlanHash:      strings.Repeat("b", 64),
			BuildJobID:    "job-1",
			Platform:      "linux/amd64",
			OCIRepository: "ghcr.io/o/r/api",
			OCIDigest:     "sha256:" + strings.Repeat("c", 64),
			Status:        "succeeded",
		},
	}
	if _, _, err := server.BuildRecords.Store.Create(context.Background(), "payload", record); err != nil {
		t.Fatal(err)
	}

	// Update policy to allow the new config hash
	if _, err := server.Policies.Apply(context.Background(), projectID, "owner", "policy-key-2", deploymentpolicyv1.ApplyRequest{
		Draft: deploymentpolicyv1.Draft{
			SchemaVersion:          deploymentpolicyv1.SchemaVersion,
			ProjectID:              projectID,
			RepositoryID:           record.RepositoryID,
			ServiceKeys:            []string{service.Name},
			WorkflowRefs:           []string{record.Workload.WorkflowRef},
			AllowedEvents:          []string{record.Workload.EventName},
			AllowedGitRefs:         []string{record.Workload.Ref},
			EnvironmentID:          plan.Assignments[0].EnvironmentID,
			AllowedRuntimeIDs:      []string{plan.Assignments[0].RuntimeID},
			AllowedOCIRepositories: []string{record.Build.OCIRepository},
			AllowedPlatforms:       []string{"linux/amd64"},
			AllowedConfigHashes:    []string{record.Build.ConfigHash},
			AllowedBuildPlanHashes: []string{record.Build.PlanHash},
			Enabled:                true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Fresh build record passes preview
	request := deploymentv1.CreateRequest{
		SchemaVersion:                  deploymentv1.JobSchemaVersion,
		BuildRecordID:                  record.ID,
		EnvironmentID:                  plan.Assignments[0].EnvironmentID,
		ExpectedTopologyRevision:       plan.Revision,
		ExpectedTopologyHash:           plan.PlanHash,
		ExpectedConfigurationRevision:  appliedSvcCfg.Configuration.Revision,
		ExpectedConfigurationStateHash: appliedSvcCfg.Configuration.StateHash,
	}
	preview, err := server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request)
	if err != nil {
		t.Fatalf("expected fresh build record preview to pass, got err: %v", err)
	}
	if !preview.Eligible {
		t.Fatalf("expected eligible preview")
	}

	// 2. Change target API public route -> causes build dependency state to change
	updatedAPICfg, err := server.Registry.GetServiceConfiguration(projectID, apiService.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.ApplyServiceConfiguration(projectID, apiService.ID, "owner", "api-key-2", registry.ServiceConfigurationApplyRequest{
		Draft: registry.ServiceConfigurationDraft{
			PublicRoute: &registry.PublicRouteIntent{Hostname: "api.example.com", Path: "/v2"},
		},
		ExpectedRevision:  updatedAPICfg.Revision,
		ExpectedStateHash: updatedAPICfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Deployment preview now must fail with BUILD_DEPENDENCY_STALE
	_, err = server.resolveDeploymentPreview(httptest.NewRequest(http.MethodPost, "/deployments/preview", nil), projectID, "owner", request)
	if deploymentAPIErrorCode(err) != "BUILD_DEPENDENCY_STALE" {
		t.Fatalf("expected BUILD_DEPENDENCY_STALE after target route change, got err: %v", err)
	}
}
