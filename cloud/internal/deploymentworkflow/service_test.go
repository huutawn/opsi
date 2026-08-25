package deploymentworkflow

import (
	"context"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
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
	analysis := repositoryanalysis.Result{SchemaVersion: repositoryanalysis.SchemaVersion, RepositoryID: 1, Repository: "owner/repo", SelectedRef: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Applications: []repositoryanalysis.Application{{SourceKey: "api", Key: "api", Name: "api", Root: ".", Port: 8080, Build: repositoryanalysis.Build{Context: ".", DockerfilePath: "Dockerfile", Strategy: "dockerfile", Platform: "linux/amd64"}}}}
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

func TestPlanHashBindsAnalysisScopeCoverageAndTruncationReason(t *testing.T) {
	_, run, _ := fixture(t)
	base := run.Plan.Hash
	mutations := []func(*Plan){
		func(plan *Plan) { plan.AnalysisScope = repositoryanalysis.Scope{ApplicationRoots: []string{"api"}} },
		func(plan *Plan) { plan.AnalysisScopeHash = "scope-hash" },
		func(plan *Plan) { plan.EvidenceCoverage.FilesInspected++ },
		func(plan *Plan) { plan.TruncationReason = "deadline" },
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
	analysis := stale.Analysis
	analysis.CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	changed.SourceCommitSHA = analysis.CommitSHA
	reviewed, err := service.SetAnalysis(context.Background(), stale.ProjectID, stale.ID, analysis, changed, stale.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.State != StateAwaitingApproval || reviewed.Approval != nil || reviewed.Plan.Source.CommitSHA != analysis.CommitSHA || reviewed.Plan.Hash == stale.Plan.Hash {
		t.Fatalf("reanalyzed=%+v", reviewed)
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
func errorCode(err error) string {
	if value, ok := err.(Error); ok {
		return value.Code
	}
	return ""
}
