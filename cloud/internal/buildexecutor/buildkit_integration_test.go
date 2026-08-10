//go:build buildkitintegration

package buildexecutor

import (
	"archive/tar"
	"context"
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
			workspace := t.TempDir()
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
