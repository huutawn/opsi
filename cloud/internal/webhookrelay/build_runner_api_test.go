package webhookrelay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type runnerAPISource struct{ source buildjob.ApplicationSource }

func (s runnerAPISource) ResolveBuildJobSource(context.Context, string, string) (buildjob.ApplicationSource, error) {
	return s.source, nil
}

func (v runnerAPIVerifier) Verify(context.Context, string) (githuboidc.VerifiedIdentity, error) {
	return v.identity, v.err
}

type runnerAPIStore struct {
	*buildjob.MemoryStore
	completion buildjob.CompletionResult
}

func (s *runnerAPIStore) CompleteRunner(_ context.Context, completion buildjob.Completion, _ buildjob.RegistryConfig, _ buildjob.ExecutorConfig) (buildjob.CompletionResult, error) {
	if s.completion.BuildRecordID != "" {
		if s.completion.Digest != completion.Result.Digest {
			return buildjob.CompletionResult{}, buildjob.Error{Code: "BUILD_RESULT_CONFLICT", Status: http.StatusConflict, Message: "BuildJob already completed with a different digest.", Cause: "build_result"}
		}
		result := s.completion
		result.Reused = true
		return result, nil
	}
	s.completion = buildjob.CompletionResult{BuildRecordID: "br-runner-api", Digest: completion.Result.Digest, BuildJobState: buildjob.StatusSucceeded}
	return s.completion, nil
}

func TestBuildRunnerClaimAndBuildSpecAPI(t *testing.T) {
	config := buildjob.ExecutorConfig{Owner: "opsi", Repository: "executor", Workflow: ".github/workflows/opsi-build-executor.yml", Ref: "refs/heads/main"}
	registryConfig := buildjob.RegistryConfig{Host: "ghcr.io", Namespace: "opsi", RepositoryPrefix: "builds", Visibility: "private"}
	store := &runnerAPIStore{MemoryStore: buildjob.NewMemoryStore()}
	job := runnerAPIJob("job-1")
	if _, _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{BuildExecutor: config, BuildRegistry: registryConfig})
	server.BuildJobs = buildjob.Service{Store: store, Sources: runnerAPISource{source: runnerAPISourceForJob(job)}, Executor: config, Registry: registryConfig, Dispatcher: runnerAPIDispatcher{}, Now: func() time.Time { return time.Unix(200, 0).UTC() }}
	githubNow := time.Unix(200, 0).UTC()
	server.githubAppClient, _ = newGitHubAppTestClient(t, githubAppRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			RepositoryIDs []int64           `json:"repository_ids"`
			Permissions   map[string]string `json:"permissions"`
		}
		if json.NewDecoder(request.Body).Decode(&body) != nil || len(body.RepositoryIDs) != 1 || body.RepositoryIDs[0] != job.Source.RepositoryID || len(body.Permissions) != 1 || body.Permissions["contents"] != "read" {
			t.Fatalf("source token request=%+v", body)
		}
		return githubAppResponse(http.StatusCreated, `{"token":"source-installation-token","expires_at":"`+githubNow.Add(time.Hour).Format(time.RFC3339)+`"}`), nil
	}), githubNow)
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

	sourceBody, _ := json.Marshal(map[string]any{"build_job_id": job.ID, "attempt_id": attempt.AttemptID, "github_run_id": uint64(99), "github_run_attempt": uint32(1)})
	sourceRequest := httptest.NewRequest(http.MethodPost, "/v1/build-runner/source-access", bytes.NewReader(sourceBody))
	sourceRequest.Header.Set("Authorization", "Bearer "+lease.Token)
	sourceResponse := httptest.NewRecorder()
	handler.ServeHTTP(sourceResponse, sourceRequest)
	var sourceAccess struct {
		BuildJobID        string    `json:"build_job_id"`
		Repository        string    `json:"repository"`
		ResolvedCommitSHA string    `json:"resolved_commit_sha"`
		AccessToken       string    `json:"access_token"`
		ExpiresAt         time.Time `json:"expires_at"`
	}
	if sourceResponse.Code != http.StatusOK || json.Unmarshal(sourceResponse.Body.Bytes(), &sourceAccess) != nil || sourceAccess.BuildJobID != job.ID || sourceAccess.Repository != job.Source.RepositoryFullName || sourceAccess.ResolvedCommitSHA != job.Source.ResolvedCommitSHA || sourceAccess.AccessToken != "source-installation-token" || !sourceAccess.ExpiresAt.Equal(githubNow.Add(time.Hour)) || sourceResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("source access status=%d body=%s", sourceResponse.Code, sourceResponse.Body.String())
	}

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2},"layers":[]}`)
	digestHash := sha256.Sum256(manifest)
	digest := "sha256:" + hex.EncodeToString(digestHash[:])
	descriptor := buildjob.ImageDescriptor{Digest: digest, MediaType: "application/vnd.oci.image.manifest.v1+json", Size: int64(len(manifest))}
	result := buildjob.RunnerResult{BuildJobID: job.ID, AttemptID: attempt.AttemptID, RegistryReference: registryConfig.Target(job.ApplicationID, job.ID).DigestReference(digest), Digest: digest, Executor: buildjob.ExecutorResult{Platform: "linux/amd64", BuildKitVersion: "v0.32.2", BuildxVersion: "v0.36.1", BuilderIdentity: "moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8", StartedAt: time.Unix(201, 0).UTC(), CompletedAt: time.Unix(202, 0).UTC(), BuildDescriptor: descriptor, Remote: buildjob.RemoteRegistryEvidence{Descriptor: descriptor, Platform: "linux/amd64", Manifest: manifest, Private: true}}}
	resultBody, _ := json.Marshal(result)
	resultRequest := httptest.NewRequest(http.MethodPost, "/v1/build-runner/result", bytes.NewReader(resultBody))
	resultRequest.Header.Set("Authorization", "Bearer "+lease.Token)
	resultResponse := httptest.NewRecorder()
	handler.ServeHTTP(resultResponse, resultRequest)
	var completion buildjob.CompletionResult
	if resultResponse.Code != http.StatusOK || json.Unmarshal(resultResponse.Body.Bytes(), &completion) != nil || completion.BuildRecordID == "" || completion.Digest != digest || completion.BuildJobState != buildjob.StatusSucceeded {
		t.Fatalf("result status=%d body=%s", resultResponse.Code, resultResponse.Body.String())
	}
	resultRequest = httptest.NewRequest(http.MethodPost, "/v1/build-runner/result", bytes.NewReader(resultBody))
	resultRequest.Header.Set("Authorization", "Bearer "+lease.Token)
	resultResponse = httptest.NewRecorder()
	handler.ServeHTTP(resultResponse, resultRequest)
	if resultResponse.Code != http.StatusOK || json.Unmarshal(resultResponse.Body.Bytes(), &completion) != nil || !completion.Reused {
		t.Fatalf("replay status=%d body=%s", resultResponse.Code, resultResponse.Body.String())
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

func runnerAPISourceForJob(job buildjob.Job) buildjob.ApplicationSource {
	return buildjob.ApplicationSource{ProjectID: job.ProjectID, EnvironmentID: job.EnvironmentID, ApplicationID: job.ApplicationID, BindingID: job.Source.BindingID, BindingUpdatedAt: job.Source.BindingUpdatedAt, InstallationID: job.Source.InstallationID, RepositoryID: job.Source.RepositoryID, RepositoryOwnerID: job.Source.RepositoryOwnerID, RepositoryFullName: job.Source.RepositoryFullName, SelectedRef: job.Source.SelectedRef, ApplicationRoot: job.Source.ApplicationRoot, BuildContext: job.Source.BuildContext, BuildStrategy: job.RequestedBuildStrategy, DockerfilePath: job.DockerfilePath}
}
