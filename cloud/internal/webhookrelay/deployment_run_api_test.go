package webhookrelay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type existingBuildRepository struct{}

type existingBuildSource struct{}

func (existingBuildSource) ResolveBuildJobSource(context.Context, string, string) (buildjob.ApplicationSource, error) {
	return buildjob.ApplicationSource{}, nil
}

func (existingBuildRepository) ResolveCommit(context.Context, int64, string, string) (string, error) {
	return strings.Repeat("a", 40), nil
}

func (existingBuildRepository) RepositoryFileExists(context.Context, int64, string, string, string) (bool, error) {
	return true, nil
}

type existingBuildDispatcher struct{}

func (existingBuildDispatcher) DispatchWorkflow(context.Context, buildjob.ExecutorConfig, string, string) (buildjob.DispatchFacts, error) {
	return buildjob.DispatchFacts{RunID: 42, RunAttempt: 1}, nil
}

func TestDeploymentRunAPIViewerReadOnlyAndProjectScoped(t *testing.T) {
	server := NewServer(Config{})
	projectA, err := server.Registry.CreateProject("org-1", "A", "project-a", "owner", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := server.Registry.CreateProject("org-1", "B", "project-b", "owner", "project-b")
	if err != nil {
		t.Fatal(err)
	}
	viewerHash, _ := auth.HashPAT("viewer-pat")
	ownerHash, _ := auth.HashPAT("owner-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{ID: "viewer", UserID: "viewer", OrgID: "org-1", ProjectID: projectA.ID, Role: "viewer", Hash: viewerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "owner", UserID: "owner", OrgID: "org-1", ProjectID: projectA.ID, Role: "owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	run, _, err := server.DeploymentRuns.Create(context.Background(), projectA.ID, "owner", "create-run", deploymentworkflow.Source{RepositoryID: 1, InstallationID: 2, Repository: "owner/repo", SelectedRef: "main"}, deploymentworkflow.Target{EnvironmentID: "env-1", RuntimeID: "runtime-1", Exposure: "public"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "request-key")
		req.Header.Set("X-Request-ID", "request-id")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}

	if response := request(http.MethodGet, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID, "viewer-pat"); response.Code != http.StatusOK {
		t.Fatalf("viewer read status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID+"/cancel", "viewer-pat"); response.Code != http.StatusForbidden {
		t.Fatalf("viewer cancel status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/projects/"+projectB.ID+"/deployment-runs/"+run.ID, "owner-pat"); response.Code == http.StatusOK {
		t.Fatalf("cross-project run was exposed: %s", response.Body.String())
	}
	if response := request(http.MethodGet, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID+"/events", "viewer-pat"); response.Code != http.StatusOK {
		t.Fatalf("viewer events status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/projects/"+projectA.ID+"/deployment-runs/"+run.ID+"/result", "viewer-pat"); response.Code != http.StatusOK {
		t.Fatalf("viewer result status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkflowHostnameIsDeterministicAndProjectScoped(t *testing.T) {
	first := workflowHostname("owner/identity-service", "project-a", "apps.example.test")
	if first != workflowHostname("owner/identity-service", "project-a", "apps.example.test") {
		t.Fatal("workflow hostname changed for identical authority")
	}
	if first == workflowHostname("owner/identity-service", "project-b", "apps.example.test") {
		t.Fatal("workflow hostname was not project scoped")
	}
	if want := ".apps.example.test"; len(first) <= len(want) || first[len(first)-len(want):] != want {
		t.Fatalf("hostname=%q", first)
	}
}

func TestWorkflowTargetUsesConservativeZeroConfigCapacity(t *testing.T) {
	server := NewServer(Config{})
	target := workflowTarget(context.Background(), server.Registry, "project-missing", deploymentworkflow.Target{})
	if target.CPUMilli != 100 || target.MemoryBytes != 256<<20 {
		t.Fatalf("target=%+v", target)
	}
}

func TestWorkflowPublishesApplicationProxyAndKeepsBackendInternal(t *testing.T) {
	run := deploymentworkflow.Run{Plan: deploymentworkflow.Plan{
		Applications: []repositoryanalysis.Application{{Key: "api"}, {Key: "web"}},
		Dependencies: []repositoryanalysis.Dependency{{From: "web", To: "api", Protocol: "http", Strategy: "internal_http", Injections: []repositoryanalysis.Injection{{EnvironmentName: "BACKEND_URL", SymbolicSource: "application.internal_url"}}}},
		Target:       deploymentworkflow.Target{Exposure: "public"},
	}}
	if exposure := applicationExposure(run, "web"); exposure != "public" {
		t.Fatalf("web exposure=%q", exposure)
	}
	if exposure := applicationExposure(run, "api"); exposure != "internal" {
		t.Fatalf("api exposure=%q", exposure)
	}
}

func TestProvisioningCheckpointSwitchesFromPlanAuthorityToExactFacts(t *testing.T) {
	if provisioningAuthorityEstablished(deploymentworkflow.AuthorityRefs{}) {
		t.Fatal("empty refs must still require the approved plan authority")
	}
	refs := deploymentworkflow.AuthorityRefs{Checkpoints: []deploymentworkflow.AuthorityCheckpoint{
		deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityResource, "res-1", 0, "", deploymentworkflow.StateProvisioning),
	}}
	if !provisioningAuthorityEstablished(refs) {
		t.Fatal("run-owned resource checkpoint did not establish provisioning authority")
	}
}

func TestWorkflowExecutionKeyChangesOnlyForFreshApproval(t *testing.T) {
	run := deploymentworkflow.Run{ID: "run-1", Plan: deploymentworkflow.Plan{Hash: "plan-hash"}}
	withoutApproval := workflowExecutionKey(run, "config", "service-1")
	run.Approval = &deploymentworkflow.Approval{ApprovedAt: time.Unix(100, 0).UTC()}
	first := workflowExecutionKey(run, "config", "service-1")
	if first == withoutApproval || workflowExecutionKey(run, "config", "service-1") != first {
		t.Fatalf("execution key is not stable within one approval: fallback=%q first=%q", withoutApproval, first)
	}
	run.Approval.ApprovedAt = time.Unix(101, 0).UTC()
	if second := workflowExecutionKey(run, "config", "service-1"); second == first {
		t.Fatalf("fresh approval reused prior execution key: %q", second)
	}
}

func TestProvisioningUsesCanonicalApplicationKeyAndRecoversMatchingUnboundService(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Provision", "provision", "owner", "provision-project")
	if err != nil {
		t.Fatal(err)
	}
	installation := registry.GitHubInstallation{InstallationID: 42, AccountID: 5, AccountLogin: "owner", AccountType: "User", Status: registry.GitHubInstallationActive}
	repository := registry.GitHubRepository{RepositoryID: 77, InstallationID: installation.InstallationID, OwnerID: 5, OwnerLogin: "owner", Name: "repo", FullName: "owner/repo", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	if _, err = server.Registry.UpsertGitHubInstallation(installation); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.UpsertGitHubRepository(repository); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubInstallation(project.ID, installation.InstallationID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubRepository(project.ID, repository.RepositoryID, "owner"); err != nil {
		t.Fatal(err)
	}
	application := repositoryanalysis.Application{SourceKey: "be", Key: "owner-repo-be", Name: "be", Root: "be", Port: 8080, Build: repositoryanalysis.Build{Context: "be", DockerfilePath: "be/Dockerfile", Strategy: registry.BuildStrategyDockerfile, Platform: "linux/amd64"}}
	run := deploymentworkflow.Run{ID: "run-provision", ProjectID: project.ID, CreatedBy: "owner", Plan: deploymentworkflow.Plan{Source: deploymentworkflow.Source{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: strings.Repeat("a", 40)}, Applications: []repositoryanalysis.Application{application}, Target: deploymentworkflow.Target{CPUMilli: 250, MemoryBytes: 256 << 20}}}
	orphan, err := server.Registry.CreateService(project.ID, applicationServiceDraft(run, application), workflowKey(run.ID, "app", application.Key))
	if err != nil {
		t.Fatal(err)
	}

	applications, err := (deploymentWorkflowExecutor{server: server}).ensureApplications(run)
	if err != nil {
		t.Fatal(err)
	}
	if applications[application.Key].ID != orphan.ID {
		t.Fatalf("applications=%+v orphan=%+v", applications, orphan)
	}
	bindings, err := server.Registry.ListGitHubServiceBindings(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].ServiceID != orphan.ID || bindings[0].RepositoryID != repository.RepositoryID || bindings[0].ServiceKey != application.Key {
		t.Fatalf("bindings=%+v", bindings)
	}
	if _, err = (deploymentWorkflowExecutor{server: server}).ensureApplications(run); err != nil {
		t.Fatalf("retry was not idempotent: %v", err)
	}

	foreign := application
	foreign.SourceKey, foreign.Key, foreign.Name = "foreign", "foreign-application", "foreign"
	foreignRun := run
	foreignRun.ID = "run-foreign"
	foreignRun.Plan.Applications = []repositoryanalysis.Application{foreign}
	foreignDraft := applicationServiceDraft(foreignRun, foreign)
	foreignDraft.RepoURL = "https://github.com/another/source.git"
	if _, err = server.Registry.CreateService(project.ID, foreignDraft, "manual-foreign-service"); err != nil {
		t.Fatal(err)
	}
	if _, err = (deploymentWorkflowExecutor{server: server}).ensureApplications(foreignRun); err == nil || !strings.Contains(err.Error(), "owned by another source") {
		t.Fatalf("foreign unbound service was adopted: %v", err)
	}
}

func TestProvisioningDefersApplicationCheckpointUntilConfigurationIsApplied(t *testing.T) {
	resource := resourcev1.Resource{ID: "res-1", Runtime: &resourcev1.ManagedResourceRuntime{Spec: resourcev1.ManagedResourceSpec{SpecHash: strings.Repeat("a", 64)}}}
	refs := provisionedResourceRefs(map[string]resourcev1.Resource{"postgres": resource}, []string{resource.ID})
	if len(refs.Checkpoints) != 1 || refs.Checkpoints[0].Kind != deploymentworkflow.AuthorityResource {
		t.Fatalf("initial provisioning refs must contain only resource facts: %+v", refs)
	}
}

func TestProvisioningWaitsForStableResourceBindings(t *testing.T) {
	bindings := []resourcev1.Binding{
		{ID: "rbind-ready", LogicalName: "redis", Lifecycle: resourcev1.LifecycleReady},
		{ID: "rbind-provisioning", LogicalName: "postgres", Lifecycle: resourcev1.LifecycleProvisioning},
	}
	checkpoints, pending, err := readyBindingCheckpoints(bindings)
	if err != nil || !pending || len(checkpoints) != 0 {
		t.Fatalf("checkpoints=%+v pending=%v err=%v", checkpoints, pending, err)
	}

	bindings[1].Lifecycle = resourcev1.LifecycleReady
	checkpoints, pending, err = readyBindingCheckpoints(bindings)
	if err != nil || pending || len(checkpoints) != 2 {
		t.Fatalf("checkpoints=%+v pending=%v err=%v", checkpoints, pending, err)
	}
	if checkpoints[0].ID != "rbind-provisioning" || checkpoints[1].ID != "rbind-ready" {
		t.Fatalf("binding checkpoints are not deterministic: %+v", checkpoints)
	}

	bindings[1].Lifecycle = resourcev1.LifecycleFailed
	bindings[1].FailureCode = "ROLE_CREATE_FAILED"
	checkpoints, pending, err = readyBindingCheckpoints(bindings)
	if err == nil || pending || len(checkpoints) != 0 || !strings.Contains(err.Error(), "ROLE_CREATE_FAILED") {
		t.Fatalf("checkpoints=%+v pending=%v err=%v", checkpoints, pending, err)
	}
}

func TestDuplicateActiveBuildDispatchRemainsPending(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Build", "build-project", "owner", "build-project")
	if err != nil {
		t.Fatal(err)
	}
	run := deploymentworkflow.Run{
		ID: "run-build-poll", ProjectID: project.ID, CreatedBy: "owner", Attempt: 1,
		Plan: deploymentworkflow.Plan{
			Source:       deploymentworkflow.Source{Repository: "owner/repo", SelectedRef: "main", CommitSHA: strings.Repeat("a", 40)},
			Applications: []repositoryanalysis.Application{{Key: "owner-repo-api", Name: "api", Root: "api", Port: 8080, Build: repositoryanalysis.Build{Context: "api", DockerfilePath: "api/Dockerfile", Strategy: buildjob.StrategyDockerfile, Platform: "linux/amd64"}}},
			Target:       deploymentworkflow.Target{EnvironmentID: "env-build", RuntimeID: "runtime-build", CPUMilli: 100, MemoryBytes: 256 << 20},
		},
	}
	service, err := server.Registry.CreateService(project.ID, applicationServiceDraft(run, run.Plan.Applications[0]), "build-service")
	if err != nil {
		t.Fatal(err)
	}
	run.Refs = deploymentworkflow.AuthorityRefs{Checkpoints: []deploymentworkflow.AuthorityCheckpoint{
		deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityApplication, service.ID, 0, "", deploymentworkflow.StateProvisioning),
	}}
	now := time.Unix(200, 0).UTC()
	store := buildjob.NewMemoryStore()
	job := buildjob.Job{
		ID: "job-active", ProjectID: project.ID, EnvironmentID: "env-build", ApplicationID: service.ID,
		Source:                 buildjob.SourceSnapshot{BindingID: "binding-build", BindingUpdatedAt: now, InstallationID: 1, RepositoryID: 2, RepositoryOwnerID: 3, RepositoryFullName: "owner/repo", SelectedRef: "main", ResolvedCommitSHA: strings.Repeat("a", 40), ApplicationRoot: "api", BuildContext: "api"},
		RequestedBuildStrategy: buildjob.StrategyDockerfile, ResolvedBuildStrategy: buildjob.StrategyDockerfile, DockerfilePath: "api/Dockerfile",
		Status: buildjob.StatusReady, CreatedBy: "owner", CreatedAt: now, UpdatedAt: now,
		IdempotencyKey: workflowExecutionKey(run, "build", service.ID+"-attempt-1"),
	}
	if _, _, err = store.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	attempt := buildjob.DispatchAttempt{Provider: buildjob.ExecutorProviderGitHubActions, AttemptID: "attempt-active", BuildJobID: job.ID, Workflow: ".github/workflows/build.yml", WorkflowRef: "opsi/executor/.github/workflows/build.yml@refs/heads/main", ExecutorRef: "refs/heads/main", DispatchedAt: now, LastState: buildjob.DispatchStateDispatched}
	if err = store.ReserveDispatch(t.Context(), project.ID, service.ID, attempt); err != nil {
		t.Fatal(err)
	}
	server.BuildJobs = buildjob.Service{
		Store: store, Sources: existingBuildSource{}, Repository: existingBuildRepository{}, Dispatcher: existingBuildDispatcher{},
		Executor: buildjob.ExecutorConfig{Owner: "opsi", Repository: "executor", Workflow: ".github/workflows/build.yml", Ref: "refs/heads/main"},
	}
	result, err := (deploymentWorkflowExecutor{server: server}).build(t.Context(), run)
	if err != nil || !result.Pending || result.FailureCode != "" || len(result.Refs.IDs(deploymentworkflow.AuthorityBuildJob)) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReusableResourceBindingPrefersReadyExactMatch(t *testing.T) {
	request := resourcev1.CreateBindingRequest{
		EnvironmentID: "env-1",
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "svc-1"},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: "res-1"},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   "postgres",
	}
	bindings := []resourcev1.Binding{
		{ID: "rbind-provisioning", EnvironmentID: request.EnvironmentID, Source: request.Source, Target: request.Target, Protocol: request.Protocol, LogicalName: request.LogicalName, Lifecycle: resourcev1.LifecycleProvisioning},
		{ID: "rbind-failed", EnvironmentID: request.EnvironmentID, Source: request.Source, Target: request.Target, Protocol: request.Protocol, LogicalName: request.LogicalName, Lifecycle: resourcev1.LifecycleFailed},
		{ID: "rbind-ready", EnvironmentID: request.EnvironmentID, Source: request.Source, Target: request.Target, Protocol: request.Protocol, LogicalName: request.LogicalName, Lifecycle: resourcev1.LifecycleReady},
	}
	reused, ok := reusableResourceBinding(bindings, request)
	if !ok || reused.ID != "rbind-ready" {
		t.Fatalf("reused=%+v ok=%v", reused, ok)
	}
	request.Target.ID = "res-other"
	if reused, ok = reusableResourceBinding(bindings, request); ok {
		t.Fatalf("mismatched binding was reused: %+v", reused)
	}
}

func TestWorkflowTopologyReusesExactPlanAcrossRuns(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Topology", "workflow-topology", "owner", "workflow-topology-project")
	if err != nil {
		t.Fatal(err)
	}
	services := server.Registry.(*registry.Service)
	node, err := services.UpsertNode(project.ID, "server", "server", registry.NodeHealthy, "127.0.0.1", "", "workflow-topology-node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.RegisterAgent(project.ID, node.ID, "sha256:workflow-topology", "hash", "test", "workflow-topology-agent", map[string]any{"deploy": true, "managed_resources": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := services.RecordAgentHeartbeat(project.ID, node.ID, registry.AgentHeartbeat{Version: "test", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true, "managed_resources": true}, Capacity: registry.NodeCapacity{CPUCores: 2, MemoryMB: 2048}}); err != nil {
		t.Fatal(err)
	}
	managed, _, err := server.Resources.Create(t.Context(), project.ID, "owner", "workflow-topology-resource", resourcev1.CreateRequest{EnvironmentID: node.EnvironmentID, Name: "redis", Kind: resourcev1.KindManagedService, Type: resourcev1.TypeRedis, Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypeRedis, Version: resourcev1.ValkeyVersion, Profile: "single-node-experimental", Replicas: 1, CPUMillicores: 100, MemoryBytes: 256 << 20, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}}})
	if err != nil {
		t.Fatal(err)
	}
	resources := map[string]resourcev1.Resource{"redis": managed}
	run := deploymentworkflow.Run{ID: "run-topology-first", ProjectID: project.ID, CreatedBy: "owner", Plan: deploymentworkflow.Plan{Target: deploymentworkflow.Target{EnvironmentID: node.EnvironmentID, RuntimeID: node.RuntimeID, Exposure: "internal", CPUMilli: 100, MemoryBytes: 256 << 20}}}
	executor := deploymentWorkflowExecutor{server: server}
	first, err := executor.ensureTopology(t.Context(), run, resources)
	if err != nil || first.Revision != 1 {
		facts, _ := services.PlacementFacts(t.Context(), project.ID)
		t.Fatalf("first=%+v facts=%+v err=%v", first, facts, err)
	}
	run.ID = "run-topology-reuse"
	reused, err := executor.ensureTopology(t.Context(), run, resources)
	if err != nil || reused.Revision != first.Revision || reused.StateHash != first.StateHash {
		t.Fatalf("reused=%+v first=%+v err=%v", reused, first, err)
	}
}

func TestReusableWorkflowPolicyKeepsOneEquivalentAuthority(t *testing.T) {
	policies := []deploymentpolicyv1.Policy{
		{ID: "pol-z", PolicyHash: "different", Draft: deploymentpolicyv1.Draft{Enabled: true}},
		{ID: "pol-b", Revision: 2, PolicyHash: "same", Draft: deploymentpolicyv1.Draft{Enabled: true}},
		{ID: "pol-disabled", PolicyHash: "same", Draft: deploymentpolicyv1.Draft{Enabled: false}},
		{ID: "pol-a", Revision: 1, PolicyHash: "same", Draft: deploymentpolicyv1.Draft{Enabled: true}},
	}
	policy, duplicates, ok := reusableWorkflowPolicy(policies, "same")
	if !ok || policy.ID != "pol-a" || len(duplicates) != 1 || duplicates[0].ID != "pol-b" {
		t.Fatalf("policy=%+v duplicates=%+v ok=%v", policy, duplicates, ok)
	}
	if _, _, ok := reusableWorkflowPolicy(policies, "missing"); ok {
		t.Fatal("unrelated policy was reused")
	}
}

func TestEnsurePoliciesDisablesEquivalentWorkflowDuplicates(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Policies", "workflow-policies", "owner", "workflow-policies-project")
	if err != nil {
		t.Fatal(err)
	}
	node, err := server.Registry.(*registry.Service).UpsertNode(project.ID, "server", "server", registry.NodeHealthy, "127.0.0.1", "", "workflow-policies-node")
	if err != nil {
		t.Fatal(err)
	}
	installation := registry.GitHubInstallation{InstallationID: 42, AccountID: 5, AccountLogin: "owner", AccountType: "User", Status: registry.GitHubInstallationActive}
	repository := registry.GitHubRepository{RepositoryID: 77, InstallationID: installation.InstallationID, OwnerID: 5, OwnerLogin: "owner", Name: "repo", FullName: "owner/repo", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	if _, err = server.Registry.UpsertGitHubInstallation(installation); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.UpsertGitHubRepository(repository); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubInstallation(project.ID, installation.InstallationID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubRepository(project.ID, repository.RepositoryID, "owner"); err != nil {
		t.Fatal(err)
	}
	application := repositoryanalysis.Application{SourceKey: "api", Key: "owner-repo-api", Name: "api", Root: "api", Port: 8080, Build: repositoryanalysis.Build{Context: "api", DockerfilePath: "api/Dockerfile", Strategy: registry.BuildStrategyDockerfile, Platform: "linux/amd64"}}
	run := deploymentworkflow.Run{ID: "run-policy-first", ProjectID: project.ID, CreatedBy: "owner", Plan: deploymentworkflow.Plan{
		Source:       deploymentworkflow.Source{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: strings.Repeat("a", 40)},
		Applications: []repositoryanalysis.Application{application},
		Target:       deploymentworkflow.Target{EnvironmentID: node.EnvironmentID, RuntimeID: node.RuntimeID},
	}}
	applications, err := (deploymentWorkflowExecutor{server: server}).ensureApplications(run)
	if err != nil {
		t.Fatal(err)
	}
	record := buildrecordv1.Record{
		SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-policy", ProjectID: project.ID, RepositoryID: uint64(repository.RepositoryID), RepositoryOwnerID: uint64(repository.OwnerID), ServiceID: applications[application.Key].ID, ServiceKey: application.Key,
		Workload: buildrecordv1.WorkloadIdentity{RepositoryID: uint64(repository.RepositoryID), RepositoryOwnerID: uint64(repository.OwnerID), Ref: "refs/heads/main", EventName: "push", WorkflowRef: "owner/repo/.github/workflows/build.yml@refs/heads/main"},
		Build:    buildrecordv1.BuildMetadata{ConfigHash: strings.Repeat("b", 64), PlanHash: strings.Repeat("c", 64), Platform: "linux/amd64", OCIRepository: "ghcr.io/owner/repo/api", OCIDigest: "sha256:" + strings.Repeat("d", 64), Status: "succeeded"},
	}
	if _, _, err = server.BuildRecords.Store.Create(t.Context(), "workflow-policy-record", record); err != nil {
		t.Fatal(err)
	}
	run.Refs = deploymentworkflow.AuthorityRefs{Checkpoints: []deploymentworkflow.AuthorityCheckpoint{deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityBuildRecord, record.ID, 1, "", deploymentworkflow.StateBuilding)}}
	executor := deploymentWorkflowExecutor{server: server}
	if _, err = executor.ensurePolicies(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	policies, err := server.Policies.List(t.Context(), project.ID)
	if err != nil || len(policies) != 1 {
		t.Fatalf("policies=%+v err=%v", policies, err)
	}
	if _, err = server.Policies.Apply(t.Context(), project.ID, "owner", "manual-equivalent-policy", deploymentpolicyv1.ApplyRequest{Draft: policies[0].Draft}); err != nil {
		t.Fatal(err)
	}
	run.ID = "run-policy-next"
	refs, err := executor.ensurePolicies(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	policies, err = server.Policies.List(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	for _, policy := range policies {
		if policy.Draft.Enabled {
			enabled++
		}
	}
	if len(policies) != 2 || enabled != 1 || len(refs) != 1 {
		t.Fatalf("policies=%+v refs=%+v", policies, refs)
	}
}

func TestScopedReanalysisKeepsExactSHAAndPersistsScope(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "Scoped", "scoped-project", "owner", "scoped-project")
	if err != nil {
		t.Fatal(err)
	}
	ownerHash, _ := auth.HashPAT("scope-owner-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{ID: "owner", UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)}}}}
	installation := registry.GitHubInstallation{InstallationID: 42, AccountID: 5, AccountLogin: "owner", AccountType: "User", Status: registry.GitHubInstallationActive}
	repository := registry.GitHubRepository{RepositoryID: 77, InstallationID: 42, OwnerID: 5, OwnerLogin: "owner", Name: "repo", FullName: "owner/repo", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	if _, err = server.Registry.UpsertGitHubInstallation(installation); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.UpsertGitHubRepository(repository); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubInstallation(project.ID, installation.InstallationID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Registry.ClaimGitHubRepository(project.ID, repository.RepositoryID, "owner"); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/commits/") {
			t.Fatal("scoped reanalysis resolved a new ref instead of retaining the exact SHA")
		}
		switch {
		case strings.Contains(request.URL.Path, "/git/trees/"):
			return githubAppResponse(http.StatusOK, `{"truncated":false,"tree":[{"path":"api/Dockerfile","mode":"100644","type":"blob","size":25},{"path":"web/Dockerfile","mode":"100644","type":"blob","size":25}]}`), nil
		case request.URL.Path == "/repos/owner/repo/contents/api/Dockerfile":
			content := base64.StdEncoding.EncodeToString([]byte("FROM scratch\nEXPOSE 8080\n"))
			return githubAppResponse(http.StatusOK, `{"type":"file","path":"api/Dockerfile","encoding":"base64","size":25,"content":"`+content+`"}`), nil
		default:
			t.Fatalf("unexpected GitHub request %s", request.URL.String())
			return nil, nil
		}
	}), time.Now().UTC())
	client.tokens[installation.InstallationID] = installationToken{Token: "read-token", ExpiresAt: time.Now().Add(time.Hour)}
	server.githubAppClient = client
	server.RepositoryAnalyzer.Repository = client

	run, _, err := server.DeploymentRuns.Create(context.Background(), project.ID, "owner", "scope-create", deploymentworkflow.Source{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: sha}, deploymentworkflow.Target{Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	initial := repositoryanalysis.Result{SchemaVersion: repositoryanalysis.SchemaVersion, RepositoryID: repository.RepositoryID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: sha, Applications: []repositoryanalysis.Application{{SourceKey: "old", Key: "repo-old", Root: ".", Port: 8080, Build: repositoryanalysis.Build{Context: ".", DockerfilePath: "Dockerfile", Strategy: "dockerfile", Platform: "linux/amd64"}}}}
	run, err = server.DeploymentRuns.SetAnalysis(context.Background(), project.ID, run.ID, initial, deploymentworkflow.AuthorityRevisions{SourceCommitSHA: sha}, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"scope":{"application_roots":["api"],"exclude_paths":[]}}`
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/deployment-runs/"+run.ID+"/analyze", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer scope-owner-pat")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "scope-analysis")
	request.Header.Set("X-Request-ID", "scope-request")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var analyzed deploymentworkflow.Run
	if json.Unmarshal(response.Body.Bytes(), &analyzed) != nil || analyzed.Plan.Source.CommitSHA != sha || len(analyzed.Plan.AnalysisScope.ApplicationRoots) != 1 || analyzed.Plan.AnalysisScope.ApplicationRoots[0] != "api" || analyzed.Plan.AnalysisScopeHash == "" || len(analyzed.Plan.Applications) != 1 || analyzed.Plan.Applications[0].Root != "api" {
		t.Fatalf("run=%+v", analyzed)
	}
}

func TestRepositoryExportAPIRolesAndProjectBoundary(t *testing.T) {
	server := NewServer(Config{})
	projectA, err := server.Registry.CreateProject("org-1", "Export A", "export-a", "owner-a", "export-a")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := server.Registry.CreateProject("org-1", "Export B", "export-b", "owner-b", "export-b")
	if err != nil {
		t.Fatal(err)
	}
	viewerHash, _ := auth.HashPAT("export-viewer-pat")
	ownerBHash, _ := auth.HashPAT("export-owner-b-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{ID: "viewer", UserID: "viewer", OrgID: "org-1", ProjectID: projectA.ID, Role: "viewer", Hash: viewerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "owner-b", UserID: "owner-b", OrgID: "org-1", ProjectID: projectB.ID, Role: "owner", Hash: ownerBHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	installation := registry.GitHubInstallation{InstallationID: 52, AccountID: 5, AccountLogin: "owner", AccountType: "User", Status: registry.GitHubInstallationActive}
	repository := registry.GitHubRepository{RepositoryID: 87, InstallationID: 52, OwnerID: 5, OwnerLogin: "owner", Name: "repo", FullName: "owner/repo", DefaultBranch: "main", Status: registry.GitHubRepositoryActive}
	_, _ = server.Registry.UpsertGitHubInstallation(installation)
	_, _ = server.Registry.UpsertGitHubRepository(repository)
	_, _ = server.Registry.ClaimGitHubInstallation(projectA.ID, installation.InstallationID, "owner-a")
	_, _ = server.Registry.ClaimGitHubRepository(projectA.ID, repository.RepositoryID, "owner-a")

	now := time.Now().UTC()
	client, _ := newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Path == "/repos/owner/repo/contents/.opsi/opsi-cd.yaml":
			return githubAppResponse(http.StatusNotFound, `{}`), nil
		case request.URL.Path == "/app/installations/52/access_tokens":
			return githubAppResponse(http.StatusCreated, `{"token":"write-token","expires_at":"`+now.Add(time.Hour).Format(time.RFC3339)+`"}`), nil
		default:
			t.Fatalf("unexpected GitHub request %s", request.URL.String())
			return nil, nil
		}
	}), now)
	client.tokens[installation.InstallationID] = installationToken{Token: "read-token", ExpiresAt: now.Add(time.Hour)}
	server.githubAppClient = client

	sha := strings.Repeat("a", 40)
	run, _, err := server.DeploymentRuns.Create(context.Background(), projectA.ID, "owner-a", "export-create", deploymentworkflow.Source{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, Repository: repository.FullName, SelectedRef: "main"}, deploymentworkflow.Target{Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := repositoryanalysis.Result{SchemaVersion: repositoryanalysis.SchemaVersion, RepositoryID: repository.RepositoryID, Repository: repository.FullName, SelectedRef: "main", CommitSHA: sha, Applications: []repositoryanalysis.Application{{SourceKey: "api", Key: "repo-api", Root: ".", Port: 8080, Build: repositoryanalysis.Build{Context: ".", DockerfilePath: "Dockerfile", Strategy: "dockerfile", Platform: "linux/amd64"}}}}
	run, err = server.DeploymentRuns.SetAnalysis(context.Background(), projectA.ID, run.ID, analysis, deploymentworkflow.AuthorityRevisions{SourceCommitSHA: sha}, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	request := func(projectID, token, path, body string, write bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if write {
			req.Header.Set("Idempotency-Key", "export-request")
			req.Header.Set("X-Request-ID", "export-request")
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	preview := request(projectA.ID, "export-viewer-pat", "/repository-export/preview", `{"run_id":"`+run.ID+`"}`, false)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"export_enabled":true`) || !strings.Contains(preview.Body.String(), `"preview_hash"`) {
		t.Fatalf("viewer preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	viewerCreate := request(projectA.ID, "export-viewer-pat", "/repository-export", `{}`, true)
	if viewerCreate.Code != http.StatusForbidden {
		t.Fatalf("viewer create status=%d body=%s", viewerCreate.Code, viewerCreate.Body.String())
	}
	crossProject := request(projectB.ID, "export-owner-b-pat", "/repository-export/preview", `{"run_id":"`+run.ID+`"}`, false)
	if crossProject.Code != http.StatusNotFound {
		t.Fatalf("cross-project preview status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}
}

func TestDeploymentPlanUpdateRequiresExactRevisionAndReplaysSemantically(t *testing.T) {
	server := NewServer(Config{})
	project, err := server.Registry.CreateProject("org-1", "A", "plan-project", "owner", "plan-project")
	if err != nil {
		t.Fatal(err)
	}
	ownerHash, _ := auth.HashPAT("plan-owner-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{{
		ID: "owner", UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour),
	}}}}
	run, _, err := server.DeploymentRuns.Create(context.Background(), project.ID, "owner", "plan-create", deploymentworkflow.Source{
		RepositoryID: 1, InstallationID: 2, Repository: "owner/repo", SelectedRef: "main",
	}, deploymentworkflow.Target{EnvironmentID: "env-1", RuntimeID: "runtime-1", Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := repositoryanalysis.Result{
		SchemaVersion: repositoryanalysis.SchemaVersion,
		RepositoryID:  1,
		Repository:    "owner/repo",
		SelectedRef:   "main",
		CommitSHA:     strings.Repeat("a", 40),
		Applications: []repositoryanalysis.Application{{
			SourceKey: "api", Key: "repo-api", Name: "api", Root: ".", Port: 8080,
			Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", DockerfilePath: "Dockerfile", Platform: "linux/amd64"},
		}},
	}
	run, err = server.DeploymentRuns.SetAnalysis(context.Background(), project.ID, run.ID, analysis, deploymentworkflow.AuthorityRevisions{SourceCommitSHA: analysis.CommitSHA}, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	draft := run.Plan
	draft.Target.Hostname = "api.apps.example.test"
	body, err := json.Marshal(map[string]any{"expected_plan_hash": run.Plan.Hash, "plan": draft})
	if err != nil {
		t.Fatal(err)
	}
	request := func(ifMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project.ID+"/deployment-runs/"+run.ID+"/plan", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer plan-owner-pat")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "plan-update")
		req.Header.Set("X-Request-ID", "plan-request")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	if response := request(""); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("1"); response.Code != http.StatusConflict {
		t.Fatalf("stale If-Match status=%d body=%s", response.Code, response.Body.String())
	}
	updated := request(strconv.FormatUint(run.Revision, 10))
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "api.apps.example.test") {
		t.Fatalf("exact update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if replay := request(strconv.FormatUint(run.Revision, 10)); replay.Code != http.StatusOK || replay.Body.String() != updated.Body.String() {
		t.Fatalf("semantic replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}
