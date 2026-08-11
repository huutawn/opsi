//go:build buildkitintegration

package buildexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

func TestBuildpacksNodeGoJavaPythonRegistryEvidence(t *testing.T) {
	registryHost := os.Getenv("OPSI_TEST_REGISTRY_HOST")
	registryAPI := os.Getenv("OPSI_TEST_REGISTRY_API")
	username := os.Getenv("OPSI_TEST_REGISTRY_USERNAME")
	password := os.Getenv("OPSI_TEST_REGISTRY_PASSWORD")
	evidenceDir := os.Getenv("OPSI_BUILDPACK_EVIDENCE_DIR")
	if registryHost == "" || registryAPI == "" || username == "" || password == "" || evidenceDir == "" {
		t.Fatal("Buildpacks registry and evidence environment is required")
	}
	registry := buildjob.RegistryConfig{Host: registryHost, Namespace: "opsi", RepositoryPrefix: "buildpacks", Visibility: "private"}
	fixtures := []struct {
		runtime, root, expectedBuildpack string
	}{
		{runtime: "node", root: "apps/api", expectedBuildpack: "paketo-buildpacks/node-engine"},
		{runtime: "go", root: ".", expectedBuildpack: "paketo-buildpacks/go-build"},
		{runtime: "java", root: ".", expectedBuildpack: "paketo-buildpacks/maven"},
		{runtime: "python", root: ".", expectedBuildpack: "paketo-buildpacks/cpython"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.runtime, func(t *testing.T) {
			repository := newGitRepository(t)
			if err := os.CopyFS(repository, os.DirFS(filepath.Join("testdata", "buildpacks", fixture.runtime))); err != nil {
				t.Fatal(err)
			}
			sha := commitRepository(t, repository, fixture.runtime)
			jobID := "buildpack-" + fixture.runtime
			spec := buildjob.BuildSpec{
				BuildJobID: jobID, Repository: "owner/repository", RepositoryID: 20, RepositoryOwnerID: 30, GitHubInstallationID: 10,
				ResolvedCommitSHA: sha, ApplicationRoot: fixture.root, BuildContext: fixture.root, ResolvedBuildStrategy: buildjob.StrategyBuildpack,
				Publication: registry.Target("application-1", jobID),
			}
			workspace := integrationWorkspace(t)
			outputDir := filepath.Join(t.TempDir(), "output")
			sourceCredentialValue := []byte("opsi-p05c-source-token-71b94416a2714d65a4ab4f611f0d9396-" + fixture.runtime)
			sourceCredential := append([]byte(nil), sourceCredentialValue...)
			var log bytes.Buffer
			result, err := Execute(context.Background(), Request{
				Spec: spec, AttemptID: jobID + "-attempt", RemoteURL: repository, Credential: sourceCredential,
				Workspace: workspace, OutputDir: outputDir,
				Publisher: distributionPublisher{APIBase: registryAPI, Username: username, Password: password},
			}, &log)
			if err != nil {
				t.Fatalf("%v\n%s", err, log.String())
			}
			if result.Status != "succeeded" || result.Strategy != buildjob.StrategyBuildpack || result.ResolvedCommitSHA != sha || result.ImageDigest != result.BuildDescriptor.Digest || result.ImageDigest != result.Remote.Descriptor.Digest || result.RegistryReference != spec.Publication.DigestReference(result.ImageDigest) || result.Builder.PackVersion != PackVersion || result.Builder.BuilderImageDigest != BuildpackBuilderDigest || result.Builder.RunImageDigest != BuildpackRunImageDigest || result.Builder.LifecycleVersion != BuildpackLifecycleVersion || len(result.Builder.Processes) == 0 {
				t.Fatalf("result=%+v", result)
			}
			foundBuildpack := false
			for _, buildpack := range result.Builder.Buildpacks {
				foundBuildpack = foundBuildpack || buildpack.ID == fixture.expectedBuildpack
			}
			if !foundBuildpack {
				t.Fatalf("selected buildpacks=%+v", result.Builder.Buildpacks)
			}
			runnerResult := buildjob.RunnerResult{
				BuildJobID: result.BuildJobID, AttemptID: result.AttemptID, RegistryReference: result.RegistryReference, Digest: result.ImageDigest,
				Executor: buildjob.ExecutorResult{Strategy: result.Strategy, Platform: result.Platform, BuilderIdentity: result.BuilderIdentity, Builder: result.Builder, StartedAt: result.StartedAt, CompletedAt: result.CompletedAt, BuildDescriptor: result.BuildDescriptor, Remote: result.Remote},
			}
			encoded, err := json.Marshal(struct {
				Runtime           string                `json:"runtime"`
				ResolvedCommitSHA string                `json:"resolved_commit_sha"`
				ApplicationRoot   string                `json:"application_root"`
				Result            buildjob.RunnerResult `json:"result"`
			}{fixture.runtime, sha, fixture.root, runnerResult})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(evidenceDir, fixture.runtime+".json"), append(encoded, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, value := range [][]byte{[]byte(log.String()), encoded} {
				if bytes.Contains(value, sourceCredentialValue) || bytes.Contains(value, []byte(password)) {
					t.Fatal("credential leaked into Buildpacks evidence")
				}
			}
			if found, err := treeContains(workspace, sourceCredentialValue); err != nil || found || !bytes.Equal(sourceCredential, make([]byte, len(sourceCredential))) {
				t.Fatalf("source credential found=%v destroyed=%v err=%v", found, bytes.Equal(sourceCredential, make([]byte, len(sourceCredential))), err)
			}
			if found, err := distributionContains(context.Background(), registryAPI, spec.Publication, result.Remote.Manifest, []byte(password), username, password); err != nil || found {
				t.Fatalf("registry credential in OCI image=%v err=%v", found, err)
			}
			t.Logf("runtime=%s application_root=%s commit=%s image_digest=%s buildpacks=%s processes=%d", fixture.runtime, fixture.root, sha, result.ImageDigest, buildpackSummary(result.Builder.Buildpacks), len(result.Builder.Processes))
		})
	}
}

func buildpackSummary(buildpacks []buildrecordv1.Buildpack) string {
	values := make([]string, 0, len(buildpacks))
	for _, buildpack := range buildpacks {
		values = append(values, buildpack.ID+"@"+buildpack.Version)
	}
	return strings.Join(values, ",")
}
