package buildexecutor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

func TestMaterializeExactCommitAfterBranchMovesAndCleansCredential(t *testing.T) {
	repository := newGitRepository(t)
	writeRepositoryFile(t, repository, "Dockerfile", "FROM scratch\nCOPY marker /marker\n")
	writeRepositoryFile(t, repository, "marker", "commit-a")
	commitA := commitRepository(t, repository, "commit A")
	writeRepositoryFile(t, repository, "marker", "commit-b")
	_ = commitRepository(t, repository, "commit B")

	workspace := t.TempDir()
	token := []byte("source-token-must-not-leak")
	var log bytes.Buffer
	sourceDir, err := Materialize(context.Background(), Request{Spec: testSpec(commitA, ".", "Dockerfile"), RemoteURL: repository, Credential: token, Workspace: workspace}, &log)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(sourceDir, "marker"))
	if err != nil || string(marker) != "commit-a" {
		t.Fatalf("marker=%q err=%v", marker, err)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git remains: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, "git-askpass-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("credential helpers=%v err=%v", matches, err)
	}
	if strings.Contains(log.String(), "source-token-must-not-leak") {
		t.Fatal("source token leaked into logs")
	}
	if found, err := treeContains(sourceDir, []byte("source-token-must-not-leak")); err != nil || found {
		t.Fatalf("token in source=%v err=%v", found, err)
	}
	if !bytes.Equal(token, make([]byte, len(token))) {
		t.Fatal("source token was not destroyed")
	}
}

func TestMaterializeRejectsUnsupportedGitFeatures(t *testing.T) {
	for name, file := range map[string]string{"submodules": ".gitmodules", "lfs": ".gitattributes"} {
		t.Run(name, func(t *testing.T) {
			repository := newGitRepository(t)
			content := "[submodule \"shared\"]\n\tpath = shared\n\turl = https://example.test/shared.git\n"
			want := "SOURCE_SUBMODULES_UNSUPPORTED"
			if name == "lfs" {
				content = "*.bin filter=lfs diff=lfs merge=lfs -text\n"
				want = "SOURCE_GIT_LFS_UNSUPPORTED"
			}
			writeRepositoryFile(t, repository, file, content)
			writeRepositoryFile(t, repository, "Dockerfile", "FROM scratch\n")
			sha := commitRepository(t, repository, name)
			_, err := Materialize(context.Background(), Request{Spec: testSpec(sha, ".", "Dockerfile"), RemoteURL: repository, Credential: []byte("token"), Workspace: t.TempDir()}, nil)
			var typed Error
			if !errors.As(err, &typed) || typed.Code != want {
				t.Fatalf("err=%v want=%s", err, want)
			}
		})
	}
}

func TestBuildFailureTaxonomy(t *testing.T) {
	tests := map[string]string{
		"dockerfile parse error on line 1":                           "DOCKERFILE_PARSE_FAILED",
		"process \"/bin/sh -c false\" did not complete successfully": "BUILD_COMMAND_FAILED",
		"rpc error: unavailable":                                     "BUILDKIT_EXECUTION_FAILED",
	}
	for output, want := range tests {
		var typed Error
		if !errors.As(classifyBuildFailure(output), &typed) || typed.Code != want {
			t.Fatalf("output=%q code=%s want=%s", output, typed.Code, want)
		}
	}
}

func TestBuildContractPathsFailClosedBeforeBuildKit(t *testing.T) {
	source := t.TempDir()
	writeRepositoryFile(t, source, "Dockerfile", "FROM scratch\n")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(source, "context")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		spec buildjob.BuildSpec
		code string
	}{
		{name: "missing context", spec: testSpec(strings.Repeat("a", 40), "missing", "Dockerfile"), code: "BUILD_CONTEXT_MISSING"},
		{name: "missing Dockerfile", spec: testSpec(strings.Repeat("a", 40), ".", "missing.Dockerfile"), code: "DOCKERFILE_MISSING"},
		{name: "context symlink escape", spec: testSpec(strings.Repeat("a", 40), "context", "Dockerfile"), code: "BUILD_PATH_MISMATCH"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Build(context.Background(), test.spec, source, t.TempDir(), filepath.Join(t.TempDir(), "output"), nil)
			var typed Error
			if !errors.As(err, &typed) || typed.Code != test.code {
				t.Fatalf("err=%v want=%s", err, test.code)
			}
		})
	}
}

func TestBuildEnvironmentDoesNotForwardRunnerCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv("OPSI_RUNNER_LEASE", "runner-secret")
	t.Setenv("OPSI_GIT_TOKEN", "source-secret")
	environment := strings.Join(dockerEnv(t.TempDir()), "\n")
	for _, secret := range []string{"github-secret", "runner-secret", "source-secret", "GITHUB_TOKEN=", "OPSI_RUNNER_LEASE=", "OPSI_GIT_TOKEN="} {
		if strings.Contains(environment, secret) {
			t.Fatalf("BuildKit environment contains %q: %s", secret, environment)
		}
	}
}

func testSpec(sha, contextPath, dockerfilePath string) buildjob.BuildSpec {
	return buildjob.BuildSpec{BuildJobID: "job-1", Repository: "owner/repository", RepositoryID: 20, RepositoryOwnerID: 30, GitHubInstallationID: 10, ResolvedCommitSHA: sha, ApplicationRoot: ".", BuildContext: contextPath, ResolvedBuildStrategy: buildjob.StrategyDockerfile, DockerfilePath: dockerfilePath}
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	runTestCommand(t, directory, "git", "init", "--quiet", "--initial-branch=main")
	runTestCommand(t, directory, "git", "config", "user.email", "executor@example.test")
	runTestCommand(t, directory, "git", "config", "user.name", "Executor Test")
	return directory
}

func writeRepositoryFile(t *testing.T, repository, name, content string) {
	t.Helper()
	path := filepath.Join(repository, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitRepository(t *testing.T, repository, message string) string {
	t.Helper()
	runTestCommand(t, repository, "git", "add", ".")
	runTestCommand(t, repository, "git", "commit", "--quiet", "-m", message)
	return strings.TrimSpace(runTestCommand(t, repository, "git", "rev-parse", "HEAD"))
}

func runTestCommand(t *testing.T, directory, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}
