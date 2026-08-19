//go:build buildkitintegration

package buildexecutor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

func TestBuildKitRegistryPushRemoteDigestAndCredentialBoundary(t *testing.T) {
	apiBase := os.Getenv("OPSI_TEST_REGISTRY_API")
	if apiBase == "" {
		t.Fatal("OPSI_TEST_REGISTRY_API is required")
	}
	repository := newGitRepository(t)
	writeRepositoryFile(t, repository, "Dockerfile", "FROM scratch\nCOPY marker /marker\n")
	writeRepositoryFile(t, repository, "marker", "registry")
	sha := commitRepository(t, repository, "registry")
	spec := testSpec(sha, ".", "Dockerfile")
	spec.Publication = buildjob.PublicationTarget{Host: "localhost:5000", Repository: "localhost:5000/opsi/builds/integration", Tag: "job-registry"}
	workspace := integrationWorkspace(t)
	const sourceSecret = "source-credential-registry-integration"
	const registrySecret = "registry-credential-must-not-reach-buildkit"
	sourceCredential := []byte(sourceSecret)
	registryCredential := []byte(registrySecret)
	t.Setenv("GITHUB_TOKEN", string(registryCredential))
	var log bytes.Buffer
	outputDir := filepath.Join(t.TempDir(), "output")
	result, err := Execute(context.Background(), Request{Spec: spec, AttemptID: "attempt-registry", RemoteURL: repository, Credential: sourceCredential, Workspace: workspace, OutputDir: outputDir, Publisher: distributionPublisher{APIBase: apiBase}}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if result.Status != "succeeded" || result.RegistryReference != spec.Publication.DigestReference(result.ImageDigest) || result.ImageDigest != result.BuildDescriptor.Digest || result.ImageDigest != result.Remote.Descriptor.Digest || result.Remote.Platform != Platform || result.OCIArtifactPath != "" {
		t.Fatalf("result=%+v", result)
	}
	encoded, _ := json.Marshal(result)
	for _, value := range [][]byte{[]byte(log.String()), encoded} {
		if bytes.Contains(value, []byte(sourceSecret)) || bytes.Contains(value, []byte(registrySecret)) {
			t.Fatal("credential leaked into build output")
		}
	}
	if found, err := treeContains(workspace, []byte(sourceSecret)); err != nil || found || !bytes.Equal(sourceCredential, make([]byte, len(sourceCredential))) {
		t.Fatalf("credential found=%v destroyed=%v err=%v", found, bytes.Equal(sourceCredential, make([]byte, len(sourceCredential))), err)
	}
	for _, secret := range [][]byte{[]byte(sourceSecret), []byte(registrySecret)} {
		if found, err := treeContains(outputDir, secret); err != nil || found {
			t.Fatalf("credential in executor output=%v err=%v", found, err)
		}
	}
	if found, err := distributionContains(context.Background(), apiBase, spec.Publication, result.Remote.Manifest, []byte(registrySecret)); err != nil || found {
		t.Fatalf("registry credential in OCI image=%v err=%v", found, err)
	}
	t.Logf("buildkit_digest=%s remote_digest=%s registry_reference=%s", result.BuildDescriptor.Digest, result.Remote.Descriptor.Digest, result.RegistryReference)
}

func TestGHCRPrivateSmoke(t *testing.T) {
	tokenText := os.Getenv("OPSI_TEST_GHCR_TOKEN")
	owner := os.Getenv("OPSI_TEST_GHCR_OWNER")
	if tokenText == "" || owner == "" {
		t.Skip("OPSI_TEST_GHCR_TOKEN and OPSI_TEST_GHCR_OWNER are required")
	}
	repository := newGitRepository(t)
	writeRepositoryFile(t, repository, "Dockerfile", "FROM scratch\nCOPY marker /marker\n")
	writeRepositoryFile(t, repository, "marker", "private-ghcr")
	sha := commitRepository(t, repository, "private ghcr")
	spec := testSpec(sha, ".", "Dockerfile")
	spec.Publication = (buildjob.RegistryConfig{Host: "ghcr.io", Namespace: owner, RepositoryPrefix: "builds", Visibility: "private"}).Target("p05b2b2-smoke", spec.BuildJobID)
	workspace := integrationWorkspace(t)
	credential := []byte(tokenText)
	var log bytes.Buffer
	result, err := Execute(context.Background(), Request{Spec: spec, AttemptID: "attempt-ghcr-smoke", RemoteURL: repository, Credential: []byte("ghcr-source-credential"), Workspace: workspace, OutputDir: filepath.Join(t.TempDir(), "output"), Publisher: GHCRRegistryPublisher{Username: owner, Credential: credential}}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if result.ImageDigest != result.BuildDescriptor.Digest || result.ImageDigest != result.Remote.Descriptor.Digest || !result.Remote.Private || result.RegistryReference != spec.Publication.DigestReference(result.ImageDigest) {
		t.Fatalf("result=%+v", result)
	}
	if found, err := treeContains(workspace, []byte(tokenText)); err != nil || found || !bytes.Equal(credential, make([]byte, len(credential))) || strings.Contains(log.String(), tokenText) {
		t.Fatalf("GHCR credential found=%v destroyed=%v log_leak=%v err=%v", found, bytes.Equal(credential, make([]byte, len(credential))), strings.Contains(log.String(), tokenText), err)
	}
	t.Logf("ghcr_reference=%s buildkit_digest=%s remote_digest=%s private=%v", result.RegistryReference, result.BuildDescriptor.Digest, result.Remote.Descriptor.Digest, result.Remote.Private)
}

type distributionPublisher struct {
	APIBase            string
	Username, Password string
}

func (p distributionPublisher) Prepare(ctx context.Context, target buildjob.PublicationTarget, workspace string, env []string) error {
	if p.Username == "" {
		return nil
	}
	command := execCommandContext(ctx, "docker", "login", target.Host, "--username", p.Username, "--password-stdin")
	command.Dir = workspace
	command.Env = env
	command.Stdin = strings.NewReader(p.Password)
	if _, err := command.CombinedOutput(); err != nil {
		return Error{Code: "REGISTRY_AUTH_FAILED", Phase: "publication", Message: "test registry authentication failed"}
	}
	return nil
}

func (p distributionPublisher) Verify(ctx context.Context, target buildjob.PublicationTarget, local buildjob.ImageDescriptor, _ string, _ []string) (buildjob.RemoteRegistryEvidence, error) {
	repository := strings.TrimPrefix(target.Repository, target.Host+"/")
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.APIBase, "/")+"/v2/"+repository+"/manifests/"+local.Digest, nil)
	setDistributionAuth(request, p.Username, p.Password)
	request.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	response, err := http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_ARTIFACT_NOT_FOUND", Phase: "verification", Message: "local registry artifact was not found"}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return buildjob.RemoteRegistryEvidence{}, err
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	mediaType := response.Header.Get("Content-Type")
	if value, _, ok := strings.Cut(mediaType, ";"); ok {
		mediaType = value
	}
	platform := platformFromManifest(raw)
	if platform == "" {
		var manifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if json.Unmarshal(raw, &manifest) != nil || manifest.Config.Digest == "" {
			return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_DIGEST_MISMATCH", Phase: "verification", Message: "local registry manifest platform is unavailable"}
		}
		configRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.APIBase, "/")+"/v2/"+repository+"/blobs/"+manifest.Config.Digest, nil)
		setDistributionAuth(configRequest, p.Username, p.Password)
		configResponse, configErr := http.DefaultClient.Do(configRequest)
		if configErr != nil || configResponse.StatusCode != http.StatusOK {
			return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_ARTIFACT_NOT_FOUND", Phase: "verification", Message: "local registry image config was not found"}
		}
		defer configResponse.Body.Close()
		var image struct{ OS, Architecture string }
		if json.NewDecoder(configResponse.Body).Decode(&image) != nil {
			return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_DIGEST_MISMATCH", Phase: "verification", Message: "local registry image config is invalid"}
		}
		platform = image.OS + "/" + image.Architecture
	}
	if digest != local.Digest || mediaType != local.MediaType || platform != Platform {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_DIGEST_MISMATCH", Phase: "verification", Message: "local registry descriptor differs from BuildKit"}
	}
	return buildjob.RemoteRegistryEvidence{Descriptor: buildjob.ImageDescriptor{Digest: digest, MediaType: mediaType, Size: int64(len(raw))}, Platform: platform, Manifest: raw, Private: true}, nil
}

func (p distributionPublisher) Cleanup(ctx context.Context, target buildjob.PublicationTarget, workspace string, env []string) {
	if p.Username != "" {
		_, _ = run(ctx, workspace, env, "docker", "logout", target.Host)
	}
}

func distributionContains(ctx context.Context, apiBase string, target buildjob.PublicationTarget, manifest, secret []byte, credentials ...string) (bool, error) {
	var document struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if json.Unmarshal(manifest, &document) != nil {
		return false, errors.New("manifest is invalid")
	}
	repository := strings.TrimPrefix(target.Repository, target.Host+"/")
	digests := []string{document.Config.Digest}
	for _, layer := range document.Layers {
		digests = append(digests, layer.Digest)
	}
	for _, child := range document.Manifests {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(apiBase, "/")+"/v2/"+repository+"/manifests/"+child.Digest, nil)
		if len(credentials) == 2 {
			setDistributionAuth(request, credentials[0], credentials[1])
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return false, err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if readErr != nil {
			return false, readErr
		}
		if found, err := distributionContains(ctx, apiBase, target, data, secret, credentials...); err != nil || found {
			return found, err
		}
	}
	for _, digest := range digests {
		if digest == "" {
			continue
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(apiBase, "/")+"/v2/"+repository+"/blobs/"+digest, nil)
		if len(credentials) == 2 {
			setDistributionAuth(request, credentials[0], credentials[1])
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return false, err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		response.Body.Close()
		if readErr != nil {
			return false, readErr
		}
		if bytes.Contains(data, secret) {
			return true, nil
		}
	}
	return false, nil
}

func setDistributionAuth(request *http.Request, username, password string) {
	if username != "" {
		request.SetBasicAuth(username, password)
	}
}

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
		{name: "network.host", dockerfile: "FROM scratch\nRUN --network=host true\n"},
		{name: "security.insecure", dockerfile: "FROM scratch\nRUN --security=insecure true\n"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			repository := newGitRepository(t)
			writeRepositoryFile(t, repository, "Dockerfile", fixture.dockerfile)
			sha := commitRepository(t, repository, fixture.name)
			result, err := Execute(context.Background(), Request{Spec: testSpec(sha, ".", "Dockerfile"), AttemptID: "attempt-entitlement", RemoteURL: repository, Credential: []byte("fixture-source-token"), Workspace: integrationWorkspace(t), OutputDir: filepath.Join(t.TempDir(), "output")}, testLogWriter{t})
			var typed Error
			if !errors.As(err, &typed) || typed.Code != "USER_BUILD_FAILED" || typed.Phase != "build" || result.Status != "failed" || result.FailureCode != "USER_BUILD_FAILED" {
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
	for _, name := range []string{"source", "git-home", "tmp"} {
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
