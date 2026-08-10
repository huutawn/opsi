package buildjob

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type testSources struct {
	source ApplicationSource
	err    error
}

func (s testSources) ResolveBuildJobSource(context.Context, string, string) (ApplicationSource, error) {
	return s.source, s.err
}

type testRepository struct {
	sha          string
	files        map[string]bool
	resolveErr   error
	fileErr      error
	resolveCalls int
}

func (r *testRepository) ResolveCommit(context.Context, int64, string, string) (string, error) {
	r.resolveCalls++
	return r.sha, r.resolveErr
}

func (r *testRepository) RepositoryFileExists(_ context.Context, _ int64, _ string, _ string, file string) (bool, error) {
	if r.fileErr != nil {
		return false, r.fileErr
	}
	return r.files[file], nil
}

func TestBuildJobDockerfileResolution(t *testing.T) {
	tests := []struct {
		name, root, buildContext, strategy, explicit, expectedPath, expectedStatus, expectedFailure string
		files                                                                                       map[string]bool
	}{
		{name: "root repository Dockerfile", root: ".", buildContext: ".", strategy: StrategyAuto, files: map[string]bool{"Dockerfile": true}, expectedPath: "Dockerfile", expectedStatus: StatusReady},
		{name: "monorepo application root", root: "apps/api", buildContext: "apps/api", strategy: StrategyAuto, files: map[string]bool{"apps/api/Dockerfile": true}, expectedPath: "apps/api/Dockerfile", expectedStatus: StatusReady},
		{name: "repository build context with nested application", root: "apps/api", buildContext: ".", strategy: StrategyAuto, files: map[string]bool{"apps/api/Dockerfile": true}, expectedPath: "apps/api/Dockerfile", expectedStatus: StatusReady},
		{name: "explicit Dockerfile", root: "apps/api", buildContext: ".", strategy: StrategyDockerfile, explicit: "containers/api.Dockerfile", files: map[string]bool{"containers/api.Dockerfile": true}, expectedPath: "containers/api.Dockerfile", expectedStatus: StatusReady},
		{name: "auto requires buildpack", root: "apps/api", buildContext: ".", strategy: StrategyAuto, files: map[string]bool{}, expectedStatus: StatusFailed, expectedFailure: "BUILDPACK_REQUIRED"},
		{name: "explicit buildpack is factual", root: "apps/api", buildContext: ".", strategy: StrategyBuildpack, files: map[string]bool{}, expectedStatus: StatusFailed, expectedFailure: "BUILD_STRATEGY_NOT_IMPLEMENTED"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &testRepository{sha: strings.Repeat("a", 40), files: test.files}
			service := testService(testSource(test.root, test.buildContext, test.strategy, test.explicit), repository, fmt.Sprintf("job-%d", index))
			job, reused, err := service.Create(context.Background(), "project-1", "application-1", "user-1", fmt.Sprintf("key-%d", index))
			if err != nil || reused {
				t.Fatalf("job=%+v reused=%v err=%v", job, reused, err)
			}
			if job.Source.ResolvedCommitSHA != repository.sha || job.DockerfilePath != test.expectedPath || job.Status != test.expectedStatus || job.FailureCode != test.expectedFailure {
				t.Fatalf("unexpected job: %+v", job)
			}
			if job.Status == StatusFailed && (job.FailureMessageRedacted == "" || job.FailureCause == "") {
				t.Fatalf("failed job has no factual failure: %+v", job)
			}
			if job.Source.ApplicationRoot != test.root || job.Source.BuildContext != test.buildContext {
				t.Fatalf("source paths were rewritten: %+v", job.Source)
			}
		})
	}
}

func TestBuildJobFailsClosedForDockerfileAndSourceFailures(t *testing.T) {
	tests := []struct {
		name       string
		source     ApplicationSource
		repository *testRepository
		sourceErr  error
		code       string
	}{
		{name: "ambiguous Dockerfiles", source: testSource("apps/api", ".", StrategyAuto, ""), repository: &testRepository{sha: strings.Repeat("a", 40), files: map[string]bool{"apps/api/Dockerfile": true, "Dockerfile": true}}, code: "DOCKERFILE_AMBIGUOUS"},
		{name: "nonexistent explicit Dockerfile", source: testSource("apps/api", ".", StrategyDockerfile, "apps/api/Dockerfile"), repository: &testRepository{sha: strings.Repeat("a", 40), files: map[string]bool{}}, code: "DOCKERFILE_NOT_FOUND"},
		{name: "invalid source scope", source: testSource("apps/api", ".", StrategyAuto, ""), sourceErr: Error{Code: "BUILD_SOURCE_INVALID_SCOPE", Status: 409, Message: "invalid", Cause: "source_scope"}, repository: &testRepository{}, code: "BUILD_SOURCE_INVALID_SCOPE"},
		{name: "repository unavailable", source: testSource("apps/api", ".", StrategyAuto, ""), repository: &testRepository{resolveErr: Error{Code: "GITHUB_REPOSITORY_UNAVAILABLE", Status: 409, Message: "unavailable", Cause: "github_repository"}}, code: "GITHUB_REPOSITORY_UNAVAILABLE"},
		{name: "ref unavailable", source: testSource("apps/api", ".", StrategyAuto, ""), repository: &testRepository{resolveErr: Error{Code: "GITHUB_REF_NOT_FOUND", Status: 409, Message: "missing", Cause: "github_ref"}}, code: "GITHUB_REF_NOT_FOUND"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := Service{Store: NewMemoryStore(), Sources: testSources{source: test.source, err: test.sourceErr}, Repository: test.repository, NewID: func() (string, error) { return fmt.Sprintf("job-fail-%d", index), nil }}
			_, _, err := service.Create(context.Background(), "project-1", "application-1", "user-1", fmt.Sprintf("fail-%d", index))
			if Code(err) != test.code {
				t.Fatalf("code=%q err=%v", Code(err), err)
			}
			jobs, listErr := service.List(context.Background(), "project-1", "application-1", "", 50)
			if listErr != nil || len(jobs) != 0 {
				t.Fatalf("invalid request persisted jobs=%+v err=%v", jobs, listErr)
			}
		})
	}
}

func TestBuildJobSnapshotsBranchAndIdempotency(t *testing.T) {
	repository := &testRepository{sha: strings.Repeat("a", 40), files: map[string]bool{"Dockerfile": true}}
	ids := 0
	service := Service{Store: NewMemoryStore(), Sources: testSources{source: testSource(".", ".", StrategyAuto, "")}, Repository: repository, Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewID: func() (string, error) { ids++; return fmt.Sprintf("job-%d", ids), nil }}
	first, reused, err := service.Create(context.Background(), "project-1", "application-1", "user-1", "same-key")
	if err != nil || reused {
		t.Fatal(err)
	}
	repository.sha = strings.Repeat("b", 40)
	replayed, reused, err := service.Create(context.Background(), "project-1", "application-1", "user-1", "same-key")
	if err != nil || !reused || replayed.ID != first.ID || replayed.Source.ResolvedCommitSHA != strings.Repeat("a", 40) || repository.resolveCalls != 1 {
		t.Fatalf("replayed=%+v reused=%v calls=%d err=%v", replayed, reused, repository.resolveCalls, err)
	}
	second, reused, err := service.Create(context.Background(), "project-1", "application-1", "user-1", "different-key")
	if err != nil || reused || second.ID == first.ID || second.Source.ResolvedCommitSHA != strings.Repeat("b", 40) {
		t.Fatalf("second=%+v reused=%v err=%v", second, reused, err)
	}
	persisted, err := service.Get(context.Background(), "project-1", "application-1", first.ID)
	if err != nil || persisted.Source.ResolvedCommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
}

func TestBuildJobLifecycleAndSerializationContainNoCredential(t *testing.T) {
	for _, status := range []string{StatusPending, StatusReady, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled} {
		if !validStatus(status) {
			t.Fatalf("status %q is not valid", status)
		}
	}
	job := Job{ID: "job-1", IdempotencyKey: "secret-idempotency-key", Source: SourceSnapshot{ResolvedCommitSHA: strings.Repeat("a", 40)}}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"token", "password", "private_key", "secret-idempotency-key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("serialized BuildJob leaked %q: %s", forbidden, text)
		}
	}
}

func testService(source ApplicationSource, repository *testRepository, id string) Service {
	return Service{Store: NewMemoryStore(), Sources: testSources{source: source}, Repository: repository, Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewID: func() (string, error) { return id, nil }}
}

func testSource(root, buildContext, strategy, dockerfile string) ApplicationSource {
	return ApplicationSource{ProjectID: "project-1", EnvironmentID: "environment-1", ApplicationID: "application-1", BindingID: "binding-1", BindingUpdatedAt: time.Unix(50, 0).UTC(), InstallationID: 10, RepositoryID: 20, RepositoryOwnerID: 30, RepositoryFullName: "owner/repository", SelectedRef: "main", ApplicationRoot: root, BuildContext: buildContext, BuildStrategy: strategy, DockerfilePath: dockerfile}
}
