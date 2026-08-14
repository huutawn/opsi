// Package buildrecordv1 defines the wire contract for trusted build metadata.
package buildrecordv1

import "time"

const SchemaVersion = "opsi.build_record/v1"

type Submission struct {
	SchemaVersion     string `json:"schema_version"`
	ServiceKey        string `json:"service_key"`
	RepositoryID      uint64 `json:"repository_id"`
	RepositoryOwnerID uint64 `json:"repository_owner_id"`
	Ref               string `json:"ref"`
	SHA               string `json:"sha"`
	EventName         string `json:"event_name"`
	WorkflowRef       string `json:"workflow_ref"`
	JobWorkflowRef    string `json:"job_workflow_ref,omitempty"`
	RunID             uint64 `json:"run_id"`
	RunAttempt        uint32 `json:"run_attempt"`
	ConfigHash        string `json:"config_hash"`
	PlanHash          string `json:"plan_hash,omitempty"`
	Platform          string `json:"platform"`
	OCIRepository     string `json:"oci_repository"`
	OCIDigest         string `json:"oci_digest"`
	ProvenanceDigest  string `json:"provenance_digest,omitempty"`
	Status            string `json:"status"`
}

type WorkloadIdentity struct {
	Issuer            string `json:"issuer"`
	Subject           string `json:"subject"`
	RepositoryID      uint64 `json:"repository_id"`
	RepositoryOwnerID uint64 `json:"repository_owner_id"`
	Ref               string `json:"ref"`
	SHA               string `json:"sha"`
	EventName         string `json:"event_name"`
	Workflow          string `json:"workflow"`
	WorkflowRef       string `json:"workflow_ref"`
	JobWorkflowRef    string `json:"job_workflow_ref,omitempty"`
	RunID             uint64 `json:"run_id"`
	RunAttempt        uint32 `json:"run_attempt"`
}

type BuildMetadata struct {
	ConfigHash       string          `json:"config_hash"`
	PlanHash         string          `json:"plan_hash,omitempty"`
	Platform         string          `json:"platform"`
	OCIRepository    string          `json:"oci_repository"`
	OCIDigest        string          `json:"oci_digest"`
	ProvenanceDigest string          `json:"provenance_digest,omitempty"`
	BuildJobID       string          `json:"build_job_id,omitempty"`
	BuildStrategy    string          `json:"build_strategy,omitempty"`
	BuilderIdentity  string          `json:"builder_identity,omitempty"`
	BuilderVersion   string          `json:"builder_version,omitempty"`
	Builder          BuilderMetadata `json:"builder,omitempty"`
	MediaType        string          `json:"media_type,omitempty"`
	Status           string          `json:"status"`
}

type BuilderMetadata struct {
	PackVersion        string      `json:"pack_version,omitempty"`
	BuilderImage       string      `json:"builder_image,omitempty"`
	BuilderImageDigest string      `json:"builder_image_digest,omitempty"`
	RunImage           string      `json:"run_image,omitempty"`
	RunImageDigest     string      `json:"run_image_digest,omitempty"`
	LifecycleVersion   string      `json:"lifecycle_version,omitempty"`
	Buildpacks         []Buildpack `json:"buildpacks,omitempty"`
	Processes          []Process   `json:"processes,omitempty"`
}

type Buildpack struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Process struct {
	Type      string   `json:"type"`
	Command   []string `json:"command,omitempty"`
	Arguments []string `json:"arguments,omitempty"`
	Direct    bool     `json:"direct,omitempty"`
	Default   bool     `json:"default,omitempty"`
}

type Record struct {
	SchemaVersion     string           `json:"schema_version"`
	ID                string           `json:"id"`
	ProjectID         string           `json:"project_id"`
	RepositoryID      uint64           `json:"repository_id"`
	RepositoryOwnerID uint64           `json:"repository_owner_id"`
	ActiveBindingID   string           `json:"active_binding_id"`
	ServiceID         string           `json:"service_id"`
	ServiceKey        string           `json:"service_key"`
	CreatedAt         time.Time        `json:"created_at"`
	Workload          WorkloadIdentity `json:"workload"`
	Build             BuildMetadata    `json:"build"`
}

type ListResult struct {
	Records    []Record `json:"records"`
	NextCursor string   `json:"next_cursor,omitempty"`
}
