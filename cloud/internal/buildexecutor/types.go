package buildexecutor

import (
	"encoding/json"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

const (
	Platform           = "linux/amd64"
	BuilderName        = "opsi"
	BuildxVersion      = "v0.36.1"
	BuildKitVersion    = "v0.32.2"
	BuildKitImage      = "moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8"
	BuildKitDaemonFlag = "--allow-insecure-entitlement=network.host"
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
	BuildJobID        string    `json:"build_job_id"`
	AttemptID         string    `json:"attempt_id"`
	ResolvedCommitSHA string    `json:"resolved_commit_sha"`
	Strategy          string    `json:"strategy"`
	DockerfilePath    string    `json:"dockerfile_path"`
	BuildContext      string    `json:"build_context"`
	Platform          string    `json:"platform"`
	BuildKitVersion   string    `json:"buildkit_version"`
	BuildxVersion     string    `json:"buildx_version"`
	ImageDigest       string    `json:"image_digest,omitempty"`
	OCIArtifactPath   string    `json:"oci_artifact_path,omitempty"`
	OCIArtifactSHA256 string    `json:"oci_artifact_sha256,omitempty"`
	BuildMetadataPath string    `json:"build_metadata_path,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	CompletedAt       time.Time `json:"completed_at"`
	Status            string    `json:"status"`
	FailureCode       string    `json:"failure_code,omitempty"`
}

type Request struct {
	Spec       buildjob.BuildSpec
	AttemptID  string
	RemoteURL  string
	Credential []byte
	Workspace  string
	OutputDir  string
}
