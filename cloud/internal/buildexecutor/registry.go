package buildexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

type RegistryPublisher interface {
	Prepare(context.Context, buildjob.PublicationTarget, string, []string) error
	Verify(context.Context, buildjob.PublicationTarget, buildjob.ImageDescriptor, string, []string) (buildjob.RemoteRegistryEvidence, error)
	Cleanup(context.Context, buildjob.PublicationTarget, string, []string)
}

type GHCRRegistryPublisher struct {
	Username   string
	Credential []byte
}

func (p GHCRRegistryPublisher) Prepare(ctx context.Context, target buildjob.PublicationTarget, workspace string, env []string) error {
	if target.Host != "ghcr.io" || p.Username == "" || len(p.Credential) == 0 {
		return Error{Code: "REGISTRY_AUTH_FAILED", Phase: "publication", Message: "GHCR publication credential is unavailable"}
	}
	command := execCommandContext(ctx, "docker", "login", target.Host, "--username", p.Username, "--password-stdin")
	command.Dir = workspace
	command.Env = env
	command.Stdin = bytes.NewReader(p.Credential)
	if output, err := command.CombinedOutput(); err != nil {
		_ = output
		_ = os.Remove(filepath.Join(workspace, "docker-config", "config.json"))
		for index := range p.Credential {
			p.Credential[index] = 0
		}
		return Error{Code: "REGISTRY_AUTH_FAILED", Phase: "publication", Message: "GHCR authentication failed"}
	}
	return nil
}

func (p GHCRRegistryPublisher) Verify(ctx context.Context, target buildjob.PublicationTarget, local buildjob.ImageDescriptor, workspace string, env []string) (buildjob.RemoteRegistryEvidence, error) {
	reference := target.DigestReference(local.Digest)
	raw, err := run(ctx, workspace, env, "docker", "buildx", "imagetools", "inspect", "--raw", reference)
	if err != nil {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_ARTIFACT_NOT_FOUND", Phase: "verification", Message: "Published registry artifact was not found"}
	}
	var manifest struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.MediaType == "" {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_ARTIFACT_NOT_FOUND", Phase: "verification", Message: "Remote registry manifest is invalid"}
	}
	sum := sha256.Sum256(raw)
	remoteDigest := "sha256:" + hex.EncodeToString(sum[:])
	if remoteDigest != local.Digest || manifest.MediaType != local.MediaType {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_DIGEST_MISMATCH", Phase: "verification", Message: "BuildKit and remote registry descriptors differ"}
	}
	platform := platformFromManifest(raw)
	if platform == "" {
		platform = inspectPlatform(ctx, workspace, env, reference)
	}
	if platform != Platform {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_DIGEST_MISMATCH", Phase: "verification", Message: "Remote registry platform differs from the canonical build platform"}
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodHead, "https://ghcr.io/v2/"+strings.TrimPrefix(target.Repository, target.Host+"/")+"/manifests/"+local.Digest, nil)
	request.Header.Set("Accept", manifest.MediaType)
	response, requestErr := http.DefaultClient.Do(request)
	if requestErr != nil {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "verification", Message: "Anonymous registry visibility verification failed"}
	}
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "REGISTRY_PUSH_FAILED", Phase: "verification", Message: "Canonical build artifact is not private"}
	}
	if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
		return buildjob.RemoteRegistryEvidence{}, Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "verification", Message: "Registry visibility response is invalid"}
	}
	return buildjob.RemoteRegistryEvidence{Descriptor: buildjob.ImageDescriptor{Digest: remoteDigest, MediaType: manifest.MediaType, Size: int64(len(raw))}, Platform: platform, Manifest: raw, Private: true}, nil
}

func (p GHCRRegistryPublisher) Cleanup(ctx context.Context, target buildjob.PublicationTarget, workspace string, env []string) {
	_, _ = run(ctx, workspace, env, "docker", "logout", target.Host)
	_ = os.Remove(filepath.Join(workspace, "docker-config", "config.json"))
	for index := range p.Credential {
		p.Credential[index] = 0
	}
}

func inspectPlatform(ctx context.Context, workspace string, env []string, reference string) string {
	output, err := run(ctx, workspace, env, "docker", "buildx", "imagetools", "inspect", "--format", "{{json .Image}}", reference)
	if err != nil {
		return ""
	}
	var image struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	}
	if json.Unmarshal(output, &image) != nil || strings.TrimSpace(image.OS) == "" || strings.TrimSpace(image.Architecture) == "" {
		return ""
	}
	return image.OS + "/" + image.Architecture
}

func platformFromManifest(raw []byte) string {
	var index struct {
		Manifests []struct {
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if json.Unmarshal(raw, &index) != nil {
		return ""
	}
	for _, manifest := range index.Manifests {
		if manifest.Platform.OS == "linux" && manifest.Platform.Architecture == "amd64" {
			return Platform
		}
	}
	return ""
}

var execCommandContext = exec.CommandContext
