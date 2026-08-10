package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
)

type runnerAPIDispatcher struct{}

func (runnerAPIDispatcher) DispatchWorkflow(context.Context, buildjob.ExecutorConfig, string, string) (buildjob.DispatchFacts, error) {
	return buildjob.DispatchFacts{}, nil
}

type runnerAPIVerifier struct {
	identity githuboidc.VerifiedIdentity
	err      error
}

func (v runnerAPIVerifier) Verify(context.Context, string) (githuboidc.VerifiedIdentity, error) {
	return v.identity, v.err
}

func TestBuildRunnerClaimAndBuildSpecAPI(t *testing.T) {
	config := buildjob.ExecutorConfig{Owner: "opsi", Repository: "executor", Workflow: ".github/workflows/opsi-build-executor.yml", Ref: "refs/heads/main"}
	store := buildjob.NewMemoryStore()
	job := runnerAPIJob("job-1")
	if _, _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{BuildExecutor: config})
	server.BuildJobs = buildjob.Service{Store: store, Executor: config, Dispatcher: runnerAPIDispatcher{}, Now: func() time.Time { return time.Unix(200, 0).UTC() }}
	attempt, err := server.BuildJobs.Dispatch(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	server.RunnerOIDC = runnerAPIVerifier{identity: githuboidc.VerifiedIdentity{Repository: config.RepositoryFullName(), WorkflowRef: config.WorkflowRef(), Ref: config.Ref, EventName: "workflow_dispatch", RunID: 99, RunAttempt: 1}}
	server.runnerOIDCInitError = nil
	handler := server.Handler()

	claimBody, _ := json.Marshal(map[string]string{"build_job_id": job.ID, "attempt_id": attempt.AttemptID, "oidc_token": "signed.jwt.value"})
	claim := httptest.NewRequest(http.MethodPost, "/v1/build-runner/claim", bytes.NewReader(claimBody))
	claimResponse := httptest.NewRecorder()
	handler.ServeHTTP(claimResponse, claim)
	var lease buildjob.RunnerLease
	if claimResponse.Code != http.StatusOK || json.Unmarshal(claimResponse.Body.Bytes(), &lease) != nil || lease.Token == "" || lease.JobID != job.ID || claimResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("claim status=%d body=%s", claimResponse.Code, claimResponse.Body.String())
	}

	specRequest := httptest.NewRequest(http.MethodGet, "/v1/build-runner/build-spec?build_job_id="+job.ID, nil)
	specRequest.Header.Set("Authorization", "Bearer "+lease.Token)
	specResponse := httptest.NewRecorder()
	handler.ServeHTTP(specResponse, specRequest)
	var spec buildjob.BuildSpec
	if specResponse.Code != http.StatusOK || json.Unmarshal(specResponse.Body.Bytes(), &spec) != nil || spec.BuildJobID != job.ID || spec.Repository != job.Source.RepositoryFullName || spec.ResolvedCommitSHA != job.Source.ResolvedCommitSHA {
		t.Fatalf("spec status=%d body=%s", specResponse.Code, specResponse.Body.String())
	}
}

func TestBuildRunnerOIDCErrorsDoNotReflectCredentials(t *testing.T) {
	server := NewServer(Config{})
	server.RunnerOIDC = runnerAPIVerifier{err: errors.New("super-secret-jwt")}
	server.runnerOIDCInitError = nil
	handler := server.Handler()
	for name, test := range map[string]struct {
		body string
		code string
	}{
		"missing": {`{"build_job_id":"job-1","attempt_id":"attempt-1","oidc_token":""}`, "OIDC_MISSING"},
		"invalid": {`{"build_job_id":"job-1","attempt_id":"attempt-1","oidc_token":"super-secret-jwt"}`, "OIDC_INVALID"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/build-runner/claim", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), test.code) || strings.Contains(response.Body.String(), "super-secret-jwt") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func runnerAPIJob(id string) buildjob.Job {
	now := time.Unix(100, 0).UTC()
	return buildjob.Job{ID: id, ProjectID: "project-1", EnvironmentID: "environment-1", ApplicationID: "application-1", Source: buildjob.SourceSnapshot{BindingID: "binding-1", BindingUpdatedAt: time.Unix(50, 0).UTC(), InstallationID: 10, RepositoryID: 20, RepositoryOwnerID: 30, RepositoryFullName: "user/source", SelectedRef: "main", ResolvedCommitSHA: strings.Repeat("a", 40), ApplicationRoot: ".", BuildContext: "."}, RequestedBuildStrategy: buildjob.StrategyDockerfile, ResolvedBuildStrategy: buildjob.StrategyDockerfile, DockerfilePath: "Dockerfile", Status: buildjob.StatusReady, CreatedBy: "user-1", IdempotencyKey: id + "-key", CreatedAt: now, UpdatedAt: now}
}
