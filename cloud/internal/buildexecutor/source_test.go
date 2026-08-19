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
		"dockerfile parse error on line 1":                           "USER_BUILD_FAILED",
		"process \"/bin/sh -c false\" did not complete successfully": "USER_BUILD_FAILED",
		"rpc error: unavailable":                                     "EXECUTOR_INFRASTRUCTURE_FAILED",
		"failed to load LLB: network.host is not allowed":            "USER_BUILD_FAILED",
		"failed to load LLB: security.insecure is not allowed":       "USER_BUILD_FAILED",
	}
	for output, want := range tests {
		var typed Error
		if !errors.As(classifyBuildFailure(output), &typed) || typed.Code != want {
			t.Fatalf("output=%q code=%s want=%s", output, typed.Code, want)
		}
	}
}

func TestBuildpackFailureTaxonomy(t *testing.T) {
	tests := map[string]string{
		"ERROR: No buildpack groups passed detection.": "BUILDPACK_DETECTION_FAILED",
		"ERROR: failed to detect: buildpack failed":    "BUILDPACK_DETECTION_FAILED",
		"ERROR: failed to build application":           "BUILDPACK_BUILD_FAILED",
		"[builder] failed to compile application":      "BUILDPACK_BUILD_FAILED",
		"run image not found":                          "BUILDPACK_RUN_IMAGE_UNAVAILABLE",
		"failed to fetch run image":                    "BUILDPACK_RUN_IMAGE_UNAVAILABLE",
		"builder image unavailable":                    "BUILDPACK_BUILDER_UNAVAILABLE",
		"failed to fetch builder image":                "BUILDPACK_BUILDER_UNAVAILABLE",
	}
	for output, want := range tests {
		var typed Error
		if !errors.As(classifyBuildpackFailure(output), &typed) || typed.Code != want {
			t.Fatalf("output=%q code=%s want=%s", output, typed.Code, want)
		}
	}
}

func TestBuildpackRejectsSharedMonorepoBeforeExecution(t *testing.T) {
	spec := testSpec(strings.Repeat("a", 40), ".", "Dockerfile")
	spec.ResolvedBuildStrategy = buildjob.StrategyBuildpack
	spec.DockerfilePath = ""
	spec.ApplicationRoot = "apps/api"
	_, err := Buildpack(context.Background(), spec, t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "output"), nil, nil)
	var typed Error
	if !errors.As(err, &typed) || typed.Code != "BUILDPACK_MONOREPO_UNSUPPORTED" {
		t.Fatalf("err=%v", err)
	}
}

func TestCanonicalBuildArgumentsDoNotGrantPrivileges(t *testing.T) {
	args := canonicalBuildArgs("/source/Dockerfile", "/output/metadata.json", "type=oci,dest=/output/image.oci.tar", "/source")
	want := "buildx build --builder opsi --progress=plain --platform linux/amd64 --file /source/Dockerfile --metadata-file /output/metadata.json --provenance=false --output type=oci,dest=/output/image.oci.tar /source"
	if got := strings.Join(args, " "); got != want {
		t.Fatalf("args=%q want=%q", got, want)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, forbidden := range []string{" --allow ", " --allow=", " --ssh ", " --ssh=", " --secret ", " --secret=", " --network host ", " --network=host ", " --push ", " --load "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("canonical build arguments contain %q: %s", forbidden, joined)
		}
	}
}

func TestCanonicalRegistryBuildArgumentsPushOnlyOpsiTarget(t *testing.T) {
	args := canonicalBuildArgs("/source/Dockerfile", "/output/metadata.json", "type=registry,name=ghcr.io/opsi/builds/app-abc:job-def,push=true", "/source")
	want := "buildx build --builder opsi --progress=plain --platform linux/amd64 --file /source/Dockerfile --metadata-file /output/metadata.json --provenance=false --output type=registry,name=ghcr.io/opsi/builds/app-abc:job-def,push=true /source"
	if got := strings.Join(args, " "); got != want || strings.Contains(got, "--load") || strings.Contains(got, "latest") {
		t.Fatalf("args=%q want=%q", got, want)
	}
}

func TestCanonicalBuildArgumentsInjectsBuildEnvironment(t *testing.T) {
	env := map[string]string{
		"PUBLIC_API_ORIGIN": "https://api.example.com",
		"API_BASE_PATH":     "/v1",
	}
	args := canonicalBuildArgs("/source/Dockerfile", "/output/metadata.json", "type=oci,dest=/output/image.oci.tar", "/source", env)
	want := "buildx build --builder opsi --progress=plain --platform linux/amd64 --build-arg API_BASE_PATH=/v1 --build-arg PUBLIC_API_ORIGIN=https://api.example.com --file /source/Dockerfile --metadata-file /output/metadata.json --provenance=false --output type=oci,dest=/output/image.oci.tar /source"
	if got := strings.Join(args, " "); got != want {
		t.Fatalf("args=%q want=%q", got, want)
	}
}

func TestCanonicalExecutorWorkflowPinsRestrictedBuilder(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "opsi-build-executor.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	inJobEnv := false
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "    env:") {
			inJobEnv = true
		} else if inJobEnv && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, "      ") {
			inJobEnv = false
		}
		if inJobEnv && strings.Contains(line, "${{ runner.") {
			t.Fatalf("job-level env uses unavailable runner context: %q", line)
		}
	}
	for _, required := range []string{
		"- name: Initialize executor paths",
		"workspace=\"$RUNNER_TEMP/opsi-build-work\"",
		"docker_config=\"$workspace/docker-config\"",
		"output=\"$RUNNER_TEMP/opsi-build-output\"",
		"bin=\"$RUNNER_TEMP/opsi-bin\"",
		`} >> "$GITHUB_ENV"`,
		"# docker/setup-buildx-action v4.2.0",
		"docker/setup-buildx-action@bb05f3f5519dd87d3ba754cc423b652a5edd6d2c",
		"version: v0.36.1",
		"name: opsi",
		"driver: docker-container",
		"image=" + BuildKitImage,
		"network=bridge",
		"buildkitd-flags: " + BuildKitDaemonFlag,
		"contents: read",
		"id-token: write",
		"packages: write",
		"GITHUB_TOKEN: ${{ github.token }}",
		"- name: Install pinned pack CLI",
		"pack-v" + PackVersion + "-linux.tgz",
		PackArchiveSHA256,
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{"docker/setup-buildx-action@v4", "security.insecure", "--allow ", "--allow=", "--ssh", "--secret", "docker build ", "driver: docker\n", "push: true", "contents: write", "actions: write", "pull-requests: write", "administration: write", ":latest"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("workflow contains forbidden %q", forbidden)
		}
	}
	if strings.Contains(workflow, "ubuntu-latest") {
		t.Fatal("executor runner image must be pinned")
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

func TestGHCRPublisherUsesPasswordStdinAndCanonicalHost(t *testing.T) {
	directory := t.TempDir()
	argsPath := filepath.Join(directory, "args")
	script := filepath.Join(directory, "docker")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\n/bin/cat >/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("ARGS_FILE", argsPath)
	credential := []byte("ghcr-token-must-use-stdin")
	publisher := GHCRRegistryPublisher{Username: "opsi-bot", Credential: credential}
	target := buildjob.PublicationTarget{Host: "ghcr.io", Repository: "ghcr.io/opsi/builds/app-test", Tag: "job-test"}
	if err := publisher.Prepare(context.Background(), target, directory, commandEnv(map[string]string{"ARGS_FILE": argsPath})); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil || string(args) != "login\nghcr.io\n--username\nopsi-bot\n--password-stdin\n" || bytes.Contains(args, credential) {
		t.Fatalf("args=%q err=%v", args, err)
	}
	if err := publisher.Prepare(context.Background(), buildjob.PublicationTarget{Host: "registry.example", Repository: "registry.example/opsi/builds/app-test", Tag: "job-test"}, directory, commandEnv(nil)); err == nil {
		t.Fatal("GHCR publisher accepted an external registry host")
	}
}

func TestBuildRejectsDockerConfigOutsideWorkspace(t *testing.T) {
	source := t.TempDir()
	writeRepositoryFile(t, source, "Dockerfile", "FROM scratch\n")
	t.Setenv("DOCKER_CONFIG", filepath.Join(t.TempDir(), "docker-config"))
	_, err := Build(context.Background(), testSpec(strings.Repeat("a", 40), ".", "Dockerfile"), source, t.TempDir(), filepath.Join(t.TempDir(), "output"), nil)
	var typed Error
	if !errors.As(err, &typed) || typed.Code != "RUNNER_ENVIRONMENT_INVALID" || typed.Phase != "infrastructure" {
		t.Fatalf("err=%v", err)
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
