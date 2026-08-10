package buildjob

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type testDispatcher struct {
	facts DispatchFacts
	err   error
}

func (d testDispatcher) DispatchWorkflow(context.Context, ExecutorConfig, string, string) (DispatchFacts, error) {
	return d.facts, d.err
}

func TestBuildExecutorDispatchClaimLeaseAndReplayContract(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	store := NewMemoryStore()
	job := executorTestJob("job-1")
	if _, _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store, Sources: testSources{source: executorTestSource(job)}, Executor: executorTestConfig(), Dispatcher: testDispatcher{}, Now: func() time.Time { return now }}
	attempt, err := service.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil || attempt.LastState != DispatchStateDispatched || attempt.RunID != 0 {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	if dispatched, err := store.Get(context.Background(), job.ProjectID, job.ApplicationID, job.ID); err != nil || dispatched.Status != StatusReady {
		t.Fatalf("dispatch changed BuildJob: job=%+v err=%v", dispatched, err)
	}
	if _, err := service.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID); Code(err) != "DUPLICATE_ACTIVE_DISPATCH" {
		t.Fatalf("duplicate dispatch err=%v", err)
	}

	identity := executorTestIdentity(99)
	var wait sync.WaitGroup
	results := make(chan RunnerLease, 8)
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			lease, claimErr := service.Claim(context.Background(), job.ID, attempt.AttemptID, identity)
			results <- lease
			errs <- claimErr
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	winners := 0
	var lease RunnerLease
	for result := range results {
		if result.Token != "" {
			winners++
			lease = result
		}
	}
	for claimErr := range errs {
		if claimErr != nil && Code(claimErr) != "RUNNER_CLAIM_CONSUMED" {
			t.Fatalf("claim err=%v", claimErr)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	if _, err := service.Claim(context.Background(), job.ID, attempt.AttemptID, identity); Code(err) != "RUNNER_CLAIM_CONSUMED" {
		t.Fatalf("replay err=%v", err)
	}
	stored, err := store.Get(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil || stored.Status != StatusRunning {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	spec, err := service.BuildSpec(context.Background(), job.ID, lease.Token)
	if err != nil || spec.BuildJobID != job.ID || spec.ResolvedCommitSHA != job.Source.ResolvedCommitSHA || spec.Repository != job.Source.RepositoryFullName {
		t.Fatalf("spec=%+v err=%v", spec, err)
	}
	grant, err := service.SourceGrant(context.Background(), job.ID, attempt.AttemptID, identity.RunID, identity.RunAttempt, lease.Token)
	if err != nil || grant.RepositoryID != job.Source.RepositoryID || grant.ResolvedCommitSHA != job.Source.ResolvedCommitSHA {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
	if _, err := service.SourceGrant(context.Background(), job.ID, "attempt-wrong", identity.RunID, identity.RunAttempt, lease.Token); Code(err) != "RUNNER_LEASE_SCOPE_MISMATCH" {
		t.Fatalf("attempt scope err=%v", err)
	}
	if _, err := service.SourceGrant(context.Background(), job.ID, attempt.AttemptID, identity.RunID+1, identity.RunAttempt, lease.Token); Code(err) != "RUNNER_LEASE_SCOPE_MISMATCH" {
		t.Fatalf("run scope err=%v", err)
	}
	if _, err := service.SourceGrant(context.Background(), job.ID, attempt.AttemptID, identity.RunID, identity.RunAttempt+1, lease.Token); Code(err) != "RUNNER_LEASE_SCOPE_MISMATCH" {
		t.Fatalf("run attempt scope err=%v", err)
	}
	changedSource := executorTestSource(job)
	changedSource.BindingUpdatedAt = changedSource.BindingUpdatedAt.Add(time.Second)
	service.Sources = testSources{source: changedSource}
	if _, err := service.SourceGrant(context.Background(), job.ID, attempt.AttemptID, identity.RunID, identity.RunAttempt, lease.Token); Code(err) != "SOURCE_BINDING_MISMATCH" {
		t.Fatalf("binding mismatch err=%v", err)
	}
	service.Sources = testSources{source: executorTestSource(job)}
	other := executorTestJob("job-2")
	other.IdempotencyKey = "job-2-key"
	if _, _, err := store.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BuildSpec(context.Background(), other.ID, lease.Token); Code(err) != "RUNNER_LEASE_SCOPE_MISMATCH" {
		t.Fatalf("scope err=%v", err)
	}
	now = lease.ExpiresAt
	if _, err := service.BuildSpec(context.Background(), job.ID, lease.Token); Code(err) != "RUNNER_LEASE_EXPIRED" {
		t.Fatalf("expiry err=%v", err)
	}

	attemptJSON, _ := json.Marshal(store.attempts[attempt.AttemptID])
	jobJSON, _ := json.Marshal(stored)
	if strings.Contains(string(attemptJSON), lease.Token) || strings.Contains(string(jobJSON), lease.Token) || len(store.attempts[attempt.AttemptID].LeaseHash) != 32 {
		t.Fatal("runner lease credential leaked into factual metadata")
	}
}

func TestBuildExecutorClaimFailsClosedForTrustMismatchCancellationAndWrongJob(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunnerIdentity, *MemoryStore, string)
		code   string
	}{
		{"wrong repository", func(i *RunnerIdentity, _ *MemoryStore, _ string) { i.Repository = "evil/repository" }, "OIDC_WRONG_REPOSITORY"},
		{"wrong workflow", func(i *RunnerIdentity, _ *MemoryStore, _ string) {
			i.WorkflowRef = "opsi/executor/.github/workflows/evil.yml@refs/heads/main"
		}, "OIDC_WRONG_WORKFLOW_REF"},
		{"wrong ref", func(i *RunnerIdentity, _ *MemoryStore, _ string) { i.Ref = "refs/heads/other" }, "OIDC_WRONG_WORKFLOW_REF"},
		{"wrong job", func(_ *RunnerIdentity, _ *MemoryStore, _ string) {}, "EXECUTOR_RUN_MISMATCH"},
		{"cancelled", func(_ *RunnerIdentity, store *MemoryStore, jobID string) {
			job := store.byID[jobID]
			job.Status = StatusCancelled
			store.byID[jobID] = job
		}, "BUILD_JOB_NOT_READY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			job := executorTestJob("job-1")
			_, _, _ = store.Create(context.Background(), job)
			service := Service{Store: store, Executor: executorTestConfig(), Dispatcher: testDispatcher{}}
			attempt, err := service.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			identity := executorTestIdentity(99)
			test.mutate(&identity, store, job.ID)
			claimJobID := job.ID
			if test.name == "wrong job" {
				claimJobID = "job-other"
			}
			if _, err := service.Claim(context.Background(), claimJobID, attempt.AttemptID, identity); Code(err) != test.code {
				t.Fatalf("err=%v want=%s", err, test.code)
			}
		})
	}
}

func TestBuildExecutorRejectsRunMismatchAndNonDockerfile(t *testing.T) {
	store := NewMemoryStore()
	job := executorTestJob("job-1")
	_, _, _ = store.Create(context.Background(), job)
	service := Service{Store: store, Executor: executorTestConfig(), Dispatcher: testDispatcher{facts: DispatchFacts{RunID: 77, RunAttempt: 1}}}
	attempt, err := service.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(context.Background(), job.ID, attempt.AttemptID, executorTestIdentity(78)); Code(err) != "EXECUTOR_RUN_MISMATCH" {
		t.Fatalf("run mismatch err=%v", err)
	}

	buildpackStore := NewMemoryStore()
	buildpack := executorTestJob("job-buildpack")
	buildpack.ResolvedBuildStrategy = StrategyBuildpackRequired
	buildpack.DockerfilePath = ""
	_, _, _ = buildpackStore.Create(context.Background(), buildpack)
	service.Store = buildpackStore
	if _, err := service.Dispatch(context.Background(), buildpack.ProjectID, buildpack.ApplicationID, buildpack.ID); Code(err) != "BUILD_STRATEGY_NOT_DISPATCHABLE" {
		t.Fatalf("buildpack dispatch err=%v", err)
	}
}

func TestBuildExecutorDispatchFailureDoesNotBecomeBuildFailure(t *testing.T) {
	store := NewMemoryStore()
	job := executorTestJob("job-1")
	_, _, _ = store.Create(context.Background(), job)
	service := Service{Store: store, Executor: executorTestConfig()}
	if _, err := service.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID); Code(err) != "EXECUTOR_CONFIG_UNAVAILABLE" {
		t.Fatalf("unavailable err=%v", err)
	}
	service.Dispatcher = testDispatcher{err: errors.New("secret dispatch response")}
	if _, err := service.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID); Code(err) != "EXECUTOR_DISPATCH_REJECTED" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("dispatch err=%v", err)
	}
	stored, err := store.Get(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil || stored.Status != StatusReady || stored.FailureCode != "" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	service.Dispatcher = testDispatcher{}
	if attempt, err := service.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID); err != nil || attempt.LastState != DispatchStateDispatched {
		t.Fatalf("retry attempt=%+v err=%v", attempt, err)
	}
}

func executorTestConfig() ExecutorConfig {
	return ExecutorConfig{Owner: "opsi", Repository: "executor", Workflow: ".github/workflows/opsi-build-executor.yml", Ref: "refs/heads/main"}
}

func executorTestIdentity(runID uint64) RunnerIdentity {
	config := executorTestConfig()
	return RunnerIdentity{Repository: config.RepositoryFullName(), WorkflowRef: config.WorkflowRef(), Ref: config.Ref, EventName: "workflow_dispatch", RunID: runID, RunAttempt: 1}
}

func executorTestJob(id string) Job {
	now := time.Unix(100, 0).UTC()
	return Job{ID: id, ProjectID: "project-1", EnvironmentID: "environment-1", ApplicationID: "application-1", Source: SourceSnapshot{BindingID: "binding-1", BindingUpdatedAt: time.Unix(50, 0).UTC(), InstallationID: 10, RepositoryID: 20, RepositoryOwnerID: 30, RepositoryFullName: "user/source", SelectedRef: "main", ResolvedCommitSHA: strings.Repeat("a", 40), ApplicationRoot: ".", BuildContext: "."}, RequestedBuildStrategy: StrategyDockerfile, ResolvedBuildStrategy: StrategyDockerfile, DockerfilePath: "Dockerfile", Status: StatusReady, CreatedBy: "user-1", IdempotencyKey: id + "-key", CreatedAt: now, UpdatedAt: now}
}

func executorTestSource(job Job) ApplicationSource {
	return ApplicationSource{ProjectID: job.ProjectID, EnvironmentID: job.EnvironmentID, ApplicationID: job.ApplicationID, BindingID: job.Source.BindingID, BindingUpdatedAt: job.Source.BindingUpdatedAt, InstallationID: job.Source.InstallationID, RepositoryID: job.Source.RepositoryID, RepositoryOwnerID: job.Source.RepositoryOwnerID, RepositoryFullName: job.Source.RepositoryFullName, SelectedRef: job.Source.SelectedRef, ApplicationRoot: job.Source.ApplicationRoot, BuildContext: job.Source.BuildContext, BuildStrategy: job.RequestedBuildStrategy, DockerfilePath: job.DockerfilePath}
}
