//go:build buildkitintegration

package buildexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestBuildpackApplicationDependencyRealizationAndArtifactConsumption(t *testing.T) {
	registryHost := os.Getenv("OPSI_TEST_REGISTRY_HOST")
	registryAPI := os.Getenv("OPSI_TEST_REGISTRY_API")
	username := os.Getenv("OPSI_TEST_REGISTRY_USERNAME")
	password := os.Getenv("OPSI_TEST_REGISTRY_PASSWORD")
	if registryHost == "" || registryAPI == "" || username == "" || password == "" {
		t.Fatal("Buildpacks registry environment is required")
	}
	registry := buildjob.RegistryConfig{Host: registryHost, Namespace: "opsi", RepositoryPrefix: "buildpacks", Visibility: "private"}

	// Create repository with Node application that consumes PUBLIC_API_ORIGIN at build time via postinstall
	repository := newGitRepository(t)
	packageJSON := `{
  "name": "opsi-buildpack-web-dep",
  "version": "1.0.0",
  "private": true,
  "engines": {
    "node": "24.x"
  },
  "scripts": {
    "postinstall": "node build.js",
    "start": "node server.js"
  }
}
`
	buildJS := `const fs = require("node:fs");
const origin = process.env.PUBLIC_API_ORIGIN || "NOT_SET";
fs.writeFileSync("build-info.json", JSON.stringify({ public_api_origin: origin }));
`
	serverJS := `const http = require("node:http");
const fs = require("node:fs");

http.createServer((req, res) => {
  if (req.url === "/build-info" || req.url === "/") {
    let buildInfo = {};
    try {
      buildInfo = JSON.parse(fs.readFileSync("build-info.json", "utf8"));
    } catch (e) {}
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({
      build_time_origin: buildInfo.public_api_origin || "",
      runtime_origin: process.env.PUBLIC_API_ORIGIN || "",
    }));
    return;
  }
  res.statusCode = 404;
  res.end("Not Found");
}).listen(Number(process.env.PORT || 8080));
`
	writeRepositoryFile(t, repository, "package.json", packageJSON)
	writeRepositoryFile(t, repository, "build.js", buildJS)
	writeRepositoryFile(t, repository, "server.js", serverJS)
	sha := commitRepository(t, repository, "initial commit with build dependency consumer")

	urlA := "https://api-buildpack-a.example.test"
	urlB := "https://api-buildpack-b.example.test"

	// 1. Build A
	jobIDA := "buildpack-dep-a"
	specA := buildjob.BuildSpec{
		BuildJobID: jobIDA, Repository: "owner/repository", RepositoryID: 20, RepositoryOwnerID: 30, GitHubInstallationID: 10,
		ResolvedCommitSHA: sha, ApplicationRoot: ".", BuildContext: ".", ResolvedBuildStrategy: buildjob.StrategyBuildpack,
		Publication: registry.Target("application-web", jobIDA),
		BuildEnvironment: map[string]string{
			"PUBLIC_API_ORIGIN": urlA,
		},
	}
	workspaceA := integrationWorkspace(t)
	outputDirA := filepath.Join(t.TempDir(), "output-a")
	sourceCredentialValue := []byte("opsi-source-token-dep-realization-proof")
	sourceCredentialA := append([]byte(nil), sourceCredentialValue...)
	var logA bytes.Buffer
	resultA, err := Execute(context.Background(), Request{
		Spec: specA, AttemptID: jobIDA + "-attempt", RemoteURL: repository, Credential: sourceCredentialA,
		Workspace: workspaceA, OutputDir: outputDirA,
		Publisher: distributionPublisher{APIBase: registryAPI, Username: username, Password: password},
	}, &logA)
	if err != nil {
		t.Fatalf("Build A failed: %v\n%s", err, logA.String())
	}
	if resultA.Status != "succeeded" || resultA.Strategy != buildjob.StrategyBuildpack {
		t.Fatalf("unexpected Build A result: %+v", resultA)
	}

	// Run Artifact A container and verify observable build-time value
	containerA, portA := startDockerContainer(t, specA.Publication.TagReference())
	infoA := fetchBuildInfo(t, portA)
	if infoA["build_time_origin"] != urlA {
		t.Fatalf("Artifact A build_time_origin = %q, want %q", infoA["build_time_origin"], urlA)
	}
	if infoA["runtime_origin"] != "" {
		t.Fatalf("Artifact A runtime_origin = %q, expected empty string (no runtime fallback)", infoA["runtime_origin"])
	}
	t.Logf("Artifact A successfully ran in container %s (port %d), observed build_time_origin=%s", containerA, portA, infoA["build_time_origin"])

	// 2. Build B with updated dependency value URL-B
	jobIDB := "buildpack-dep-b"
	specB := buildjob.BuildSpec{
		BuildJobID: jobIDB, Repository: "owner/repository", RepositoryID: 20, RepositoryOwnerID: 30, GitHubInstallationID: 10,
		ResolvedCommitSHA: sha, ApplicationRoot: ".", BuildContext: ".", ResolvedBuildStrategy: buildjob.StrategyBuildpack,
		Publication: registry.Target("application-web", jobIDB),
		BuildEnvironment: map[string]string{
			"PUBLIC_API_ORIGIN": urlB,
		},
	}
	workspaceB := integrationWorkspace(t)
	outputDirB := filepath.Join(t.TempDir(), "output-b")
	sourceCredentialB := append([]byte(nil), sourceCredentialValue...)
	var logB bytes.Buffer
	resultB, err := Execute(context.Background(), Request{
		Spec: specB, AttemptID: jobIDB + "-attempt", RemoteURL: repository, Credential: sourceCredentialB,
		Workspace: workspaceB, OutputDir: outputDirB,
		Publisher: distributionPublisher{APIBase: registryAPI, Username: username, Password: password},
	}, &logB)
	if err != nil {
		t.Fatalf("Build B failed: %v\n%s", err, logB.String())
	}
	if resultB.Status != "succeeded" || resultB.Strategy != buildjob.StrategyBuildpack {
		t.Fatalf("unexpected Build B result: %+v", resultB)
	}

	// Run Artifact B container and verify observable build-time value
	containerB, portB := startDockerContainer(t, specB.Publication.TagReference())
	infoB := fetchBuildInfo(t, portB)
	if infoB["build_time_origin"] != urlB {
		t.Fatalf("Artifact B build_time_origin = %q, want %q", infoB["build_time_origin"], urlB)
	}
	if infoB["runtime_origin"] != "" {
		t.Fatalf("Artifact B runtime_origin = %q, expected empty string (no runtime fallback)", infoB["runtime_origin"])
	}
	t.Logf("Artifact B successfully ran in container %s (port %d), observed build_time_origin=%s", containerB, portB, infoB["build_time_origin"])

	// 3. Verify Freshness / Invariance
	if resultA.ImageDigest == resultB.ImageDigest {
		t.Fatalf("expected different image digests for URL-A vs URL-B: digestA=%s digestB=%s", resultA.ImageDigest, resultB.ImageDigest)
	}
	if resultA.BuildJobID == resultB.BuildJobID {
		t.Fatalf("expected distinct build job IDs")
	}

	// 4. Secret Safety Verification
	for _, log := range []string{logA.String(), logB.String()} {
		if strings.Contains(log, string(sourceCredentialValue)) || strings.Contains(log, password) {
			t.Fatal("credential leaked into build logs")
		}
	}
}

func startDockerContainer(t *testing.T, image string) (string, int) {
	t.Helper()
	port := freeLocalPort(t)
	containerName := fmt.Sprintf("opsi-bp-test-%d-%d", time.Now().UnixNano(), port)
	cmd := exec.Command("docker", "run", "-d", "--rm", "--name", containerName, "-p", fmt.Sprintf("127.0.0.1:%d:8080", port), image)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run %s failed: %v\n%s", image, err, string(out))
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})
	return containerName, port
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on local port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func fetchBuildInfo(t *testing.T, port int) map[string]string {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/build-info", port)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	var lastBody string
	for i := 0; i < 40; i++ {
		resp, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK {
				var res map[string]string
				if json.Unmarshal(body, &res) == nil && res["build_time_origin"] != "" {
					return res
				}
			}
			lastBody = string(body)
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("failed to get build info from %s: err=%v body=%s", url, lastErr, lastBody)
	return nil
}
