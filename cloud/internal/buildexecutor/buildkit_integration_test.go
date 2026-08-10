//go:build buildkitintegration

package buildexecutor

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildKitOCIForRootMonorepoAndNestedContext(t *testing.T) {
	fixtures := []struct {
		name            string
		applicationRoot string
		buildContext    string
		dockerfile      string
		files           map[string]string
	}{
		{name: "root", applicationRoot: ".", buildContext: ".", dockerfile: "Dockerfile", files: map[string]string{"Dockerfile": "FROM scratch\nCOPY marker /marker\n", "marker": "root"}},
		{name: "monorepo", applicationRoot: "apps/api", buildContext: ".", dockerfile: "apps/api/Dockerfile", files: map[string]string{"apps/api/Dockerfile": "FROM scratch\nCOPY packages/shared/value.txt /shared.txt\n", "packages/shared/value.txt": "shared"}},
		{name: "nested", applicationRoot: "services/api", buildContext: "services", dockerfile: "services/api/Dockerfile", files: map[string]string{"services/api/Dockerfile": "FROM scratch\nCOPY api/app.txt /app.txt\n", "services/api/app.txt": "nested"}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			repository := newGitRepository(t)
			for name, content := range fixture.files {
				writeRepositoryFile(t, repository, name, content)
			}
			sha := commitRepository(t, repository, fixture.name)
			spec := testSpec(sha, fixture.buildContext, fixture.dockerfile)
			spec.ApplicationRoot = fixture.applicationRoot
			workspace := integrationWorkspace(t)
			outputDir := filepath.Join(t.TempDir(), "output")
			result, err := Execute(context.Background(), Request{Spec: spec, AttemptID: "attempt-1", RemoteURL: repository, Credential: []byte("fixture-source-token"), Workspace: workspace, OutputDir: outputDir}, testLogWriter{t})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "succeeded" || !digestPattern.MatchString(result.ImageDigest) || result.OCIArtifactSHA256 == "" {
				t.Fatalf("result=%+v", result)
			}
			verifyOCITar(t, result.OCIArtifactPath)
			t.Logf("fixture=%s image_digest=%s oci_sha256=%s", fixture.name, result.ImageDigest, result.OCIArtifactSHA256)
		})
	}
}

func TestBuildKitExactCommitAndCredentialCleanup(t *testing.T) {
	repository := newGitRepository(t)
	writeRepositoryFile(t, repository, "Dockerfile", "FROM scratch\nCOPY marker /marker\n")
	writeRepositoryFile(t, repository, "marker", "commit-a")
	commitA := commitRepository(t, repository, "commit A")
	writeRepositoryFile(t, repository, "marker", "commit-b")
	_ = commitRepository(t, repository, "commit B")
	workspace := integrationWorkspace(t)
	token := []byte("integration-source-token-must-not-leak")
	result, err := Execute(context.Background(), Request{Spec: testSpec(commitA, ".", "Dockerfile"), AttemptID: "attempt-exact", RemoteURL: repository, Credential: token, Workspace: workspace, OutputDir: filepath.Join(t.TempDir(), "output")}, testLogWriter{t})
	if err != nil {
		t.Fatal(err)
	}
	marker, readErr := os.ReadFile(filepath.Join(workspace, "source", "marker"))
	if readErr != nil || string(marker) != "commit-a" || result.ResolvedCommitSHA != commitA {
		t.Fatalf("marker=%q result_sha=%s err=%v", marker, result.ResolvedCommitSHA, readErr)
	}
	if _, err := os.Stat(filepath.Join(workspace, "source", ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git remains: %v", err)
	}
	if found, err := treeContains(workspace, []byte("integration-source-token-must-not-leak")); err != nil || found || !bytes.Equal(token, make([]byte, len(token))) {
		t.Fatalf("credential found=%v destroyed=%v err=%v", found, bytes.Equal(token, make([]byte, len(token))), err)
	}
	verifyOCITar(t, result.OCIArtifactPath)
	t.Logf("exact_commit=%s image_digest=%s credential_cleanup=pass", commitA, result.ImageDigest)
}

func TestBuildKitRejectsUngrantedEntitlements(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		dockerfile string
	}{
		{name: "network.host", dockerfile: "FROM alpine:3.22\nRUN --network=host true\n"},
		{name: "security.insecure", dockerfile: "FROM alpine:3.22\nRUN --security=insecure true\n"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			repository := newGitRepository(t)
			writeRepositoryFile(t, repository, "Dockerfile", fixture.dockerfile)
			sha := commitRepository(t, repository, fixture.name)
			result, err := Execute(context.Background(), Request{Spec: testSpec(sha, ".", "Dockerfile"), AttemptID: "attempt-entitlement", RemoteURL: repository, Credential: []byte("fixture-source-token"), Workspace: integrationWorkspace(t), OutputDir: filepath.Join(t.TempDir(), "output")}, testLogWriter{t})
			var typed Error
			if !errors.As(err, &typed) || typed.Code != "BUILD_ENTITLEMENT_DENIED" || typed.Phase != "build" || result.Status != "failed" || result.FailureCode != "BUILD_ENTITLEMENT_DENIED" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			t.Logf("fixture=%s expected=BUILD FAILED code=%s phase=%s", fixture.name, typed.Code, typed.Phase)
		})
	}
}

func integrationWorkspace(t *testing.T) string {
	t.Helper()
	dockerConfig := os.Getenv("DOCKER_CONFIG")
	if dockerConfig == "" || filepath.Base(dockerConfig) != "docker-config" {
		t.Fatal("DOCKER_CONFIG must be the canonical workspace/docker-config path")
	}
	workspace := filepath.Dir(dockerConfig)
	for _, name := range []string{"source", "git-home"} {
		if err := os.RemoveAll(filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
	}
	return workspace
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(data []byte) (int, error) {
	w.t.Log(string(data))
	return len(data), nil
}

func verifyOCITar(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	found := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found[header.Name] = true
	}
	if !found["oci-layout"] || !found["index.json"] {
		t.Fatalf("invalid OCI archive entries=%v", found)
	}
}
