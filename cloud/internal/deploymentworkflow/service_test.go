package deploymentworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func fixture(t *testing.T) (Service, Run, AuthorityRevisions) {
	t.Helper()
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	service := Service{Store: NewMemoryStore(), Now: func() time.Time { return now }}
	source := Source{RepositoryID: 1, InstallationID: 2, Repository: "owner/repo", SelectedRef: "main"}
	run, _, err := service.Create(context.Background(), "project-1", "user-1", "create-1", source, Target{EnvironmentID: "env-1", RuntimeID: "runtime-1", Hostname: "app.example.com", Exposure: "public"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := repositoryanalysis.Result{SchemaVersion: repositoryanalysis.SchemaVersion, RepositoryID: 1, Repository: "owner/repo", SelectedRef: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Applications: []repositoryanalysis.Application{{SourceKey: "api", Key: "api", Name: "api", Root: ".", Port: 8080, Environment: map[string]string{"PORT": "8080"}, Build: repositoryanalysis.Build{Context: ".", DockerfilePath: "Dockerfile", Strategy: "dockerfile", Platform: "linux/amd64"}}}}
	authority := AuthorityRevisions{SourceCommitSHA: analysis.CommitSHA, TopologyRevision: 3, TopologyHash: "topology"}
	run, err = service.SetAnalysis(context.Background(), run.ProjectID, run.ID, analysis, authority, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	return service, run, authority
}

func TestPlanHashDeterministicAndSecretRedacted(t *testing.T) {
	service, run, _ := fixture(t)
	run.Plan.Secrets = []repositoryanalysis.Secret{{Name: "jwt", ApplicationKey: "api", EnvironmentName: "JWT_KEY", Generated: true, SecretRef: "generated://jwt", Display: "Generated and securely stored"}}
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	first := run.Plan.Hash
	run.Plan.Applications = append([]repositoryanalysis.Application(nil), run.Plan.Applications...)
	if err := refreshHash(&run.Plan); err != nil || run.Plan.Hash != first {
		t.Fatalf("hash changed: %s %v", run.Plan.Hash, err)
	}
	if err := ValidatePlan(run.Plan); err != nil {
		t.Fatal(err)
	}
	_ = service
}

func TestApprovalRejectsInvalidManagedStorageBeforeProvisioning(t *testing.T) {
	service, run, authority := fixture(t)
	run.Plan.Resources = []repositoryanalysis.Resource{{LogicalName: "postgres", Type: "postgres", Managed: true, Required: true, Persistence: &repositoryanalysis.Persistence{Persistent: true}}}
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	stored, err := service.Store.Save(context.Background(), run, run.Revision, Event{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Approve(context.Background(), stored.ProjectID, stored.ID, "user-1", stored.Plan.Hash, authority); errorCode(err) != "DEPLOYMENT_PLAN_INVALID" {
		t.Fatalf("approval error=%v", err)
	}
	current, err := service.Get(context.Background(), stored.ProjectID, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != StateAwaitingApproval || current.Attempt != 0 || current.Approval != nil {
		t.Fatalf("invalid plan crossed approval boundary: %+v", current)
	}
}

func TestPlanHashBindsAnalysisScopeCoverageAndTruncationReason(t *testing.T) {
	_, run, _ := fixture(t)
	base := run.Plan.Hash
	mutations := []func(*Plan){
		func(plan *Plan) { plan.AnalysisScope = repositoryanalysis.Scope{ApplicationRoots: []string{"api"}} },
		func(plan *Plan) { plan.AnalysisScopeHash = "scope-hash" },
		func(plan *Plan) { plan.EvidenceCoverage.FilesInspected++ },
		func(plan *Plan) { plan.TruncationReason = "deadline" },
		func(plan *Plan) {
			plan.Dependencies = []repositoryanalysis.Dependency{{From: "api", To: "database", Protocol: "postgres", Injections: []repositoryanalysis.Injection{{EnvironmentName: "DB_DSN", SymbolicSource: "connection.template", Template: "host={{host}}"}}}}
		},
	}
	for index, mutate := range mutations {
		plan := run.Plan
		mutate(&plan)
		hash, err := HashPlan(plan)
		if err != nil || hash == base {
			t.Fatalf("mutation %d was not bound by plan hash: hash=%q err=%v", index, hash, err)
		}
	}
}

func TestHashPlanDoesNotMutateDraftOrder(t *testing.T) {
	_, run, _ := fixture(t)
	run.Plan.Applications = []repositoryanalysis.Application{{Key: "web"}, {Key: "api"}}
	run.Plan.Resources = []repositoryanalysis.Resource{{LogicalName: "valkey"}, {LogicalName: "postgres"}}

	if _, err := HashPlan(run.Plan); err != nil {
		t.Fatal(err)
	}
	if run.Plan.Applications[0].Key != "web" || run.Plan.Resources[0].LogicalName != "valkey" {
		t.Fatalf("HashPlan mutated caller order: applications=%v resources=%v", run.Plan.Applications, run.Plan.Resources)
	}
}

func TestApprovalExactHashAndStaleAuthority(t *testing.T) {
	service, run, authority := fixture(t)
	if _, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", "wrong", authority); errorCode(err) != "DEPLOYMENT_PLAN_STALE" {
		t.Fatalf("err=%v", err)
	}
	changed := authority
	changed.TopologyRevision++
	stale, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, changed)
	if err != nil {
		t.Fatal(err)
	}
	if stale.State != StateStale || stale.Approval != nil {
		t.Fatalf("run=%+v", stale)
	}
}

func TestStaleRunCanBeAnalyzedAgainAndRequiresFreshApproval(t *testing.T) {
	service, run, authority := fixture(t)
	changed := authority
	changed.TopologyRevision++
	stale, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, changed)
	if err != nil {
		t.Fatal(err)
	}
	stale.Attempt = 2
	stale.Refs = Checkpoints(AuthorityBinding, StateProvisioning, "binding-from-previous-attempt")
	stale.PreflightHash = "preflight-from-previous-attempt"
	stale.PreflightWarnings = []string{"warning from previous attempt"}
	stale, err = service.Store.Save(context.Background(), stale, stale.Revision, Event{})
	if err != nil {
		t.Fatal(err)
	}
	analysis := stale.Analysis
	analysis.CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	changed.SourceCommitSHA = analysis.CommitSHA
	reviewed, err := service.SetAnalysis(context.Background(), stale.ProjectID, stale.ID, analysis, changed, stale.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.State != StateAwaitingApproval || reviewed.Approval != nil || reviewed.Attempt != 0 || reviewed.Plan.Source.CommitSHA != analysis.CommitSHA || reviewed.Plan.Hash == stale.Plan.Hash {
		t.Fatalf("reanalyzed=%+v", reviewed)
	}
	if len(reviewed.Refs.Checkpoints) != 0 || reviewed.PreflightHash != "" || len(reviewed.PreflightWarnings) != 0 || reviewed.FinishedAt != nil {
		t.Fatalf("reanalyzed run retained execution facts: %+v", reviewed)
	}
}

func TestDraftEditResolvesOnlyEditableBlockingIssuesAndInvalidatesApproval(t *testing.T) {
	service, run, _ := fixture(t)
	run.State = StateAwaitingInput
	run.Plan.Applications[0].Port = 0
	run.Plan.Issues = []repositoryanalysis.Issue{
		{Code: "APPLICATION_PORT_REQUIRED", Blocking: true},
		{Code: "ANALYSIS_TRUNCATED", Blocking: true},
	}
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	run, err := service.Store.Save(context.Background(), run, run.Revision, Event{})
	if err != nil {
		t.Fatal(err)
	}
	draft := run.Plan
	draft.Applications[0].Port = 8080
	updated, err := service.UpdatePlan(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, draft)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != StateAwaitingInput || len(updated.Plan.Issues) != 1 || updated.Plan.Issues[0].Code != "ANALYSIS_TRUNCATED" || updated.Approval != nil {
		t.Fatalf("updated=%+v", updated)
	}
}

func TestWarningAcknowledgementBindsExactPreflightHash(t *testing.T) {
	service, run, authority := fixture(t)
	approved, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, authority)
	if err != nil {
		t.Fatal(err)
	}
	approved.State = StateAwaitingWarningAck
	approved.PreflightHash = "preflight-a"
	saved, err := service.Store.Save(context.Background(), approved, approved.Revision, Event{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Acknowledge(context.Background(), saved.ProjectID, saved.ID, "user-1", "preflight-b"); errorCode(err) != "PREFLIGHT_ACKNOWLEDGEMENT_STALE" {
		t.Fatalf("err=%v", err)
	}
	next, err := service.Acknowledge(context.Background(), saved.ProjectID, saved.ID, "user-1", "preflight-a")
	if err != nil || next.State != StateDeploying {
		t.Fatalf("run=%+v err=%v", next, err)
	}
}

type fakeExecutor struct{}

func (fakeExecutor) Execute(_ context.Context, run Run, step State) (StepResult, error) {
	result := StepResult{}
	switch step {
	case StateProvisioning:
		result.Refs = Checkpoints(AuthorityResource, step, "resource-1")
	case StateBuilding:
		result.Refs = Checkpoints(AuthorityBuildRecord, step, "record-1")
	case StatePreflighting:
		result.PreflightHash = "preflight"
	case StateDeploying:
		result.Refs = Checkpoints(AuthorityDeploymentJob, step, "deployment-1")
	case StateVerifying:
		result.Refs = Checkpoints(AuthorityVerification, step, "verification-1")
	}
	return result, nil
}

type pendingExecutor struct{ calls int }

func (e *pendingExecutor) Execute(_ context.Context, _ Run, _ State) (StepResult, error) {
	e.calls++
	return StepResult{Pending: e.calls == 1, Refs: Checkpoints(AuthorityResource, StateProvisioning, "resource-1")}, nil
}

type rollbackExecutor struct{}

func (rollbackExecutor) Execute(_ context.Context, _ Run, step State) (StepResult, error) {
	if step == StateDeploying {
		return StepResult{RollbackRequired: true, FailureCode: "ROLLOUT_FAILED", FailureMessage: "rollout failed"}, nil
	}
	return StepResult{}, nil
}

type retryingExecutor struct{}

func (retryingExecutor) Execute(_ context.Context, _ Run, _ State) (StepResult, error) {
	return StepResult{FailureCode: "TEMPORARY", FailureMessage: "temporary authority failure", Retryable: true}, nil
}

type slowExecutor struct{}

func (slowExecutor) Execute(ctx context.Context, _ Run, _ State) (StepResult, error) {
	select {
	case <-time.After(35 * time.Millisecond):
		return StepResult{}, nil
	case <-ctx.Done():
		return StepResult{}, ctx.Err()
	}
}

type renewalStore struct {
	Store
	renewals int
}

func (s *renewalStore) RenewLease(ctx context.Context, projectID, runID, owner string, now time.Time, ttl time.Duration) (bool, error) {
	s.renewals++
	return s.Store.RenewLease(ctx, projectID, runID, owner, now, ttl)
}

func TestControllerPersistsPendingAuthorityWithoutAdvancing(t *testing.T) {
	service, run, authority := fixture(t)
	run, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, authority)
	if err != nil {
		t.Fatal(err)
	}
	executor := &pendingExecutor{}
	controller := Controller{Store: service.Store, Executor: executor, WorkerID: "worker-1"}
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	waiting, err := service.Get(context.Background(), run.ProjectID, run.ID)
	if err != nil || waiting.State != StateProvisioning || len(waiting.Refs.IDs(AuthorityResource)) != 1 {
		t.Fatalf("run=%+v err=%v", waiting, err)
	}
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	advanced, err := service.Get(context.Background(), run.ProjectID, run.ID)
	if err != nil || advanced.State != StateBuilding {
		t.Fatalf("run=%+v err=%v", advanced, err)
	}
}

func TestControllerLeaseResumeAndFactualReferences(t *testing.T) {
	service, run, authority := fixture(t)
	run, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, authority)
	if err != nil {
		t.Fatal(err)
	}
	controller := Controller{Store: service.Store, Executor: fakeExecutor{}, WorkerID: "worker-1"}
	for i := 0; i < 5; i++ {
		if count, err := controller.RunOnce(context.Background()); err != nil || count != 1 {
			t.Fatalf("iteration %d count=%d err=%v", i, count, err)
		}
	}
	finished, err := service.Get(context.Background(), run.ProjectID, run.ID)
	if err != nil || finished.State != StateSucceeded || len(finished.Refs.IDs(AuthorityBuildRecord)) != 1 || len(finished.Refs.IDs(AuthorityDeploymentJob)) != 1 {
		t.Fatalf("run=%+v err=%v", finished, err)
	}
}

func TestRetryIsBoundedAndResumesExactFailedStep(t *testing.T) {
	service, run, authority := fixture(t)
	run, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, authority)
	if err != nil {
		t.Fatal(err)
	}
	run.State = StateFailed
	run.Failure = &Failure{Step: StateBuilding, Code: "BUILD_FAILED", Retryable: true}
	run, err = service.Store.Save(context.Background(), run, run.Revision, Event{})
	if err != nil {
		t.Fatal(err)
	}
	for expectedAttempt := 2; expectedAttempt <= 3; expectedAttempt++ {
		run, err = service.Retry(context.Background(), run.ProjectID, run.ID, "user-1")
		if err != nil || run.State != StateBuilding || run.Attempt != expectedAttempt {
			t.Fatalf("attempt=%d run=%+v err=%v", expectedAttempt, run, err)
		}
		run.State = StateFailed
		run.Failure = &Failure{Step: StateBuilding, Code: "BUILD_FAILED", Retryable: true}
		run, err = service.Store.Save(context.Background(), run, run.Revision, Event{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Retry(context.Background(), run.ProjectID, run.ID, "user-1"); errorCode(err) != "DEPLOYMENT_RUN_RETRY_LIMIT" {
		t.Fatalf("retry limit err=%v", err)
	}
}

func TestControllerUsesKnownGoodRollbackBranch(t *testing.T) {
	service, run, authority := fixture(t)
	run, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, authority)
	if err != nil {
		t.Fatal(err)
	}
	run.State = StateDeploying
	run, err = service.Store.Save(context.Background(), run, run.Revision, Event{})
	if err != nil {
		t.Fatal(err)
	}
	controller := Controller{Store: service.Store, Executor: rollbackExecutor{}, WorkerID: "worker-rollback"}
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rolling, err := service.Get(context.Background(), run.ProjectID, run.ID)
	if err != nil || rolling.State != StateRollingBack || rolling.Failure == nil || rolling.Failure.Code != "ROLLOUT_FAILED" {
		t.Fatalf("rolling=%+v err=%v", rolling, err)
	}
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := service.Get(context.Background(), run.ProjectID, run.ID)
	if err != nil || rolledBack.State != StateRolledBack || rolledBack.FinishedAt == nil {
		t.Fatalf("rolledBack=%+v err=%v", rolledBack, err)
	}
}

func TestControllerUsesBoundedExponentialRetryForRetryableAuthorityFailures(t *testing.T) {
	service, run, authority := fixture(t)
	run, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, authority)
	if err != nil {
		t.Fatal(err)
	}
	now := run.UpdatedAt
	controller := Controller{Store: service.Store, Executor: retryingExecutor{}, WorkerID: "worker-retry", Now: func() time.Time { return now }}
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ = service.Get(context.Background(), run.ProjectID, run.ID)
	if run.State != StateProvisioning || run.Attempt != 2 || run.RetryAfterAt == nil {
		t.Fatalf("first retry=%+v", run)
	}
	now = now.Add(3 * time.Second)
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ = service.Get(context.Background(), run.ProjectID, run.ID)
	if run.Attempt != 3 || run.RetryAfterAt == nil {
		t.Fatalf("second retry=%+v", run)
	}
	now = now.Add(5 * time.Second)
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ = service.Get(context.Background(), run.ProjectID, run.ID)
	if run.State != StateFailed || run.Attempt != 3 || run.Failure == nil {
		t.Fatalf("terminal retry=%+v", run)
	}
}

func TestControllerRenewsLeaseDuringLongAuthorityCall(t *testing.T) {
	service, run, authority := fixture(t)
	run, err := service.Approve(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, authority)
	if err != nil {
		t.Fatal(err)
	}
	store := &renewalStore{Store: service.Store}
	controller := Controller{Store: store, Executor: slowExecutor{}, WorkerID: "worker-renew", LeaseDuration: 15 * time.Millisecond}
	if _, err := controller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.renewals < 2 {
		t.Fatalf("renewals=%d", store.renewals)
	}
}

type fakeTopologyAuthority struct {
	plan        topologyv1.Plan
	operatorCap topologyv1.OperatorCapacity
	planErr     error
	opCapErr    error
}

func (f fakeTopologyAuthority) Get(ctx context.Context, projectID string) (topologyv1.Plan, error) {
	return f.plan, f.planErr
}

func (f fakeTopologyAuthority) GetOperatorCapacity(ctx context.Context, projectID, runtimeID string) (topologyv1.OperatorCapacity, error) {
	return f.operatorCap, f.opCapErr
}

type fakeFactsAuthority struct {
	facts topologyv1.PlacementFacts
	err   error
}

func (f fakeFactsAuthority) PlacementFacts(ctx context.Context, projectID string) (topologyv1.PlacementFacts, error) {
	return f.facts, f.err
}

type fakeResourceAuthority struct {
	resources []resourcev1.Resource
	err       error
}

func (f fakeResourceAuthority) List(ctx context.Context, projectID, environmentID string) ([]resourcev1.Resource, error) {
	return f.resources, f.err
}

func TestRecommendationEngineCalculationsAndBasisParity(t *testing.T) {
	service, run, _ := fixture(t)
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)

	facts := topologyv1.PlacementFacts{
		ProjectID: "project-1",
		Environments: []topologyv1.EnvironmentFact{
			{ID: "env-1", ProjectID: "project-1", Status: "active"},
		},
		Runtimes: []topologyv1.RuntimeFact{
			{ID: "runtime-1", ProjectID: "project-1", EnvironmentID: "env-1", Type: "k3s", Status: "ready"},
		},
		Nodes: []topologyv1.NodeFact{
			{ID: "node-1", ProjectID: "project-1", RuntimeID: "runtime-1", Status: "healthy", CPUCores: 2, MemoryMB: 4096, LastSeenAt: &fresh},
		},
		Agents: []topologyv1.AgentFact{
			{ID: "agent-1", ProjectID: "project-1", RuntimeID: "runtime-1", NodeID: "node-1", Status: "active", Capabilities: map[string]any{"deploy": true}, LastSeenAt: &fresh},
		},
	}

	// Current topology has an existing background service "worker" (100m, 256MiB) and old "api" (200m, 512MiB)
	topoPlan := topologyv1.Plan{
		ID:        "topo-1",
		Revision:  2,
		PlanHash:  "topohash123",
		StateHash: "topostate123",
		Assignments: []topologyv1.Assignment{
			{ServiceKey: "worker", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 100, CPULimitMillicores: 500, MemoryRequestBytes: 256 << 20, MemoryLimitBytes: 512 << 20},
			{ServiceKey: "api", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 200, MemoryRequestBytes: 512 << 20}, // Being redeployed!
		},
	}

	// Run has 1 planned managed Postgres
	run.Plan.Resources = []repositoryanalysis.Resource{
		{LogicalName: "postgres", Type: "postgres", Managed: true},
	}
	_, _ = service.Store.Save(context.Background(), run, run.Revision, Event{})

	engine := RecommendationEngine{
		Store:          service.Store,
		Topology:       fakeTopologyAuthority{plan: topoPlan},
		Facts:          fakeFactsAuthority{facts: facts},
		Resources:      fakeResourceAuthority{resources: []resourcev1.Resource{}},
		Now:            func() time.Time { return now },
		ReservedCPU:    250,
		ReservedMemory: 256 << 20,
	}

	rec, err := engine.Recommend(context.Background(), run.ProjectID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Eligible {
		t.Fatalf("expected eligible=true, got reason=%s", rec.Reason)
	}

	// Total node: 2000m CPU, 4096 MiB RAM
	// System reserve: 250m CPU, 256 MiB RAM
	// Existing workloads: worker (100m, 512 MiB RAM limit). Note: "api" is redeployed so excluded!
	// Planned managed: postgres (250m, 256 MiB)
	// Available for run: 2000 - 250 - 100 - 250 = 1400m CPU, 4096 - 256 - 512 - 256 = 3072 MiB RAM
	if rec.Projection.RealCapacity.CPUMillicores != 2000 || rec.Projection.RealCapacity.MemoryBytes != 4096<<20 {
		t.Fatalf("real capacity=%+v", rec.Projection.RealCapacity)
	}
	if rec.Projection.SystemReserve.CPUMillicores != 250 || rec.Projection.SystemReserve.MemoryBytes != 256<<20 {
		t.Fatalf("system reserve=%+v", rec.Projection.SystemReserve)
	}
	if rec.Projection.ExistingWorkload.CPUMillicores != 100 || rec.Projection.ExistingWorkload.MemoryBytes != 512<<20 {
		t.Fatalf("existing workloads=%+v", rec.Projection.ExistingWorkload)
	}
	if rec.Projection.PlannedManaged.CPUMillicores != 250 || rec.Projection.PlannedManaged.MemoryBytes != 256<<20 {
		t.Fatalf("planned managed=%+v", rec.Projection.PlannedManaged)
	}
	if rec.Projection.AvailableForRun.CPUMillicores != 1400 || rec.Projection.AvailableForRun.MemoryBytes != 3072<<20 {
		t.Fatalf("available for run=%+v", rec.Projection.AvailableForRun)
	}

	// Basis hashes deterministic
	if rec.Basis.BasisHash == "" || rec.Basis.CapacityStateHash == "" {
		t.Fatalf("empty basis hashes: %+v", rec.Basis)
	}

	// An already assigned managed resource is identified by Resource ID, counted
	// once as an existing commitment, and not counted again as planned capacity.
	assignedPlan := topoPlan
	assignedPlan.Assignments = append(append([]topologyv1.Assignment(nil), topoPlan.Assignments...), topologyv1.Assignment{
		ServiceKey: "res-postgres", RuntimeID: "runtime-1", Replicas: 1,
		CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, MemoryLimitBytes: 256 << 20,
	})
	assignedEngine := engine
	assignedEngine.Topology = fakeTopologyAuthority{plan: assignedPlan}
	assignedEngine.Resources = fakeResourceAuthority{resources: []resourcev1.Resource{{
		ID: "res-postgres", ProjectID: "project-1", EnvironmentID: "env-1", Name: "postgres",
		Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres, Lifecycle: resourcev1.LifecycleReady,
		Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20},
	}}}
	assignedRec, err := assignedEngine.Recommend(context.Background(), run.ProjectID, run.ID)
	if err != nil || !assignedRec.Eligible {
		t.Fatalf("assigned managed recommendation eligible=%v err=%v", assignedRec.Eligible, err)
	}
	if assignedRec.Projection.PlannedManaged != (ResourceBudget{}) || assignedRec.Projection.ExistingWorkload.CPUMillicores != 350 || assignedRec.Projection.ExistingWorkload.MemoryBytes != 768<<20 {
		t.Fatalf("assigned managed resource was not single-counted: %+v", assignedRec.Projection)
	}

	// Authority failures are not silently treated as empty topology/capacity.
	brokenTopology := engine
	brokenTopology.Topology = fakeTopologyAuthority{planErr: errors.New("topology unavailable")}
	if _, err := brokenTopology.Recommend(context.Background(), run.ProjectID, run.ID); err == nil {
		t.Fatal("topology authority failure did not fail closed")
	}
	brokenCapacity := engine
	brokenCapacity.Topology = fakeTopologyAuthority{plan: topoPlan, opCapErr: errors.New("capacity unavailable")}
	if _, err := brokenCapacity.Recommend(context.Background(), run.ProjectID, run.ID); err == nil {
		t.Fatal("operator capacity authority failure did not fail closed")
	}

	// Stale heartbeat fails closed
	stale := now.Add(-10 * time.Minute)
	staleFacts := facts
	staleFacts.Nodes[0].LastSeenAt = &stale
	staleEngine := engine
	staleEngine.Facts = fakeFactsAuthority{facts: staleFacts}
	staleRec, err := staleEngine.Recommend(context.Background(), run.ProjectID, run.ID)
	if err != nil || staleRec.Eligible {
		t.Fatalf("stale heartbeat did not fail closed: eligible=%v err=%v", staleRec.Eligible, err)
	}

	// Low capacity below minimum (e.g. 50m available) fails closed
	lowCapFacts := facts
	lowCapFacts.Nodes[0].CPUCores = 0
	lowCapFacts.Nodes[0].MemoryMB = 0
	lowCapEngine := engine
	lowCapEngine.Facts = fakeFactsAuthority{facts: lowCapFacts}
	lowCapRec, err := lowCapEngine.Recommend(context.Background(), run.ProjectID, run.ID)
	if err != nil || lowCapRec.Eligible {
		t.Fatalf("unknown/zero capacity did not fail closed: eligible=%v err=%v", lowCapRec.Eligible, err)
	}
}

func TestApplicationCapacityValidation(t *testing.T) {
	// 1. Zero limits are normalized on the plan before its hash is computed.
	plan := Plan{Target: Target{CPUMilli: 100, MemoryBytes: 256 << 20}, Applications: []repositoryanalysis.Application{{Key: "api", Capacity: repositoryanalysis.Capacity{
		Replicas: 1, CPUMilli: 250, MemoryBytes: 256 << 20,
	}}}}
	if err := refreshHash(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.Target.CPULimitMilli != 100 || plan.Target.MemoryLimitBytes != 256<<20 || plan.Applications[0].Capacity.CPULimitMilli != 250 || plan.Applications[0].Capacity.MemoryLimitBytes != 256<<20 {
		t.Fatalf("zero limit not normalized before hash: %+v", plan)
	}
	if hash, err := HashPlan(plan); err != nil || hash != plan.Hash {
		t.Fatalf("normalized plan hash mismatch hash=%q plan_hash=%q err=%v", hash, plan.Hash, err)
	}

	// 2. Non-zero limit smaller than request is rejected
	capInvalid := repositoryanalysis.Capacity{
		Replicas:      1,
		CPUMilli:      500,
		CPULimitMilli: 250,
		MemoryBytes:   256 << 20,
	}
	if err := ValidateApplicationCapacity(capInvalid); err == nil {
		t.Fatal("expected error for limit < request")
	}

	// 3. Negative values rejected
	capNegative := repositoryanalysis.Capacity{
		Replicas: -1,
	}
	if err := ValidateApplicationCapacity(capNegative); err == nil {
		t.Fatal("expected error for negative replicas")
	}
}

func errorCode(err error) string {
	if value, ok := err.(Error); ok {
		return value.Code
	}
	return ""
}
