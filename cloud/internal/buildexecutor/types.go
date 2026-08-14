package buildexecutor

import (
	"encoding/json"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

const (
	Platform                  = "linux/amd64"
	BuilderName               = "opsi"
	BuildxVersion             = "v0.36.1"
	BuildKitVersion           = "v0.32.2"
	BuildKitImage             = "moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8"
	BuildKitDaemonFlag        = "--allow-insecure-entitlement=network.host"
	PackVersion               = "0.40.9"
	PackArchiveSHA256         = "dc0ee1e931cf8a106d7555a01a214864f9acb60b77adf15d69b74df4404758e9"
	BuildpackBuilder          = "paketobuildpacks/ubuntu-noble-builder:0.0.167@sha256:cebbe41ca97c166e10f4fc6076724df39c4e247f8ee9c81b852a9219b7a993c0"
	BuildpackBuilderDigest    = "sha256:cebbe41ca97c166e10f4fc6076724df39c4e247f8ee9c81b852a9219b7a993c0"
	BuildpackRunImage         = "paketobuildpacks/ubuntu-noble-run:0.0.112@sha256:a9433b9e0b786dc2f90a433464cf7c11ede0877e30e4155a66abe35001a56d20"
	BuildpackRunImageDigest   = "sha256:a9433b9e0b786dc2f90a433464cf7c11ede0877e30e4155a66abe35001a56d20"
	BuildpackLifecycleVersion = "0.21.15"
)

type Error struct {
	Code    string `json:"code"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

func (e Error) Error() string { return e.Code }

type SourceAccess struct {
	BuildJobID           string    `json:"build_job_id"`
	Repository           string    `json:"repository"`
	RepositoryID         int64     `json:"repository_id"`
	GitHubInstallationID int64     `json:"github_installation_id"`
	ResolvedCommitSHA    string    `json:"resolved_commit_sha"`
	AccessToken          secret    `json:"access_token"`
	ExpiresAt            time.Time `json:"expires_at"`
}

func (s *SourceAccess) Credential() []byte { return []byte(s.AccessToken) }
func (s *SourceAccess) Destroy()           { s.AccessToken.destroy() }

type secret []byte

func (s *secret) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = append((*s)[:0], value...)
	return nil
}

func (s secret) destroy() {
	for i := range s {
		s[i] = 0
	}
}

type Result struct {
	BuildJobID        string                          `json:"build_job_id"`
	AttemptID         string                          `json:"attempt_id"`
	ResolvedCommitSHA string                          `json:"resolved_commit_sha"`
	Strategy          string                          `json:"strategy"`
	DockerfilePath    string                          `json:"dockerfile_path"`
	BuildContext      string                          `json:"build_context"`
	Platform          string                          `json:"platform"`
	BuildKitVersion   string                          `json:"buildkit_version"`
	BuildxVersion     string                          `json:"buildx_version"`
	BuilderIdentity   string                          `json:"builder_identity"`
	Builder           buildrecordv1.BuilderMetadata   `json:"builder,omitempty"`
	ImageDigest       string                          `json:"image_digest,omitempty"`
	RegistryReference string                          `json:"registry_reference,omitempty"`
	BuildDescriptor   buildjob.ImageDescriptor        `json:"build_descriptor,omitempty"`
	Remote            buildjob.RemoteRegistryEvidence `json:"remote,omitempty"`
	OCIArtifactPath   string                          `json:"oci_artifact_path,omitempty"`
	OCIArtifactSHA256 string                          `json:"oci_artifact_sha256,omitempty"`
	BuildMetadataPath string                          `json:"build_metadata_path,omitempty"`
	StartedAt         time.Time                       `json:"started_at"`
	CompletedAt       time.Time                       `json:"completed_at"`
	Status            string                          `json:"status"`
	FailureCode       string                          `json:"failure_code,omitempty"`
}

type Request struct {
	Spec       buildjob.BuildSpec
	AttemptID  string
	RemoteURL  string
	Credential []byte
	Workspace  string
	OutputDir  string
	Publisher  RegistryPublisher
}
