// Package deploymentpolicyv1 defines exact-match routing policy contracts for
// BuildRecords that have already passed GitHub OIDC workload admission.
package deploymentpolicyv1

import "time"

const SchemaVersion = "opsi.deployment_policy/v1"

type Draft struct {
	SchemaVersion          string   `json:"schema_version"`
	ProjectID              string   `json:"project_id"`
	RepositoryID           uint64   `json:"repository_id"`
	ServiceKeys            []string `json:"service_keys"`
	WorkflowRefs           []string `json:"workflow_refs"`
	JobWorkflowRefs        []string `json:"job_workflow_refs,omitempty"`
	AllowedEvents          []string `json:"allowed_events"`
	AllowedGitRefs         []string `json:"allowed_git_refs"`
	EnvironmentID          string   `json:"environment_id"`
	AllowedRuntimeIDs      []string `json:"allowed_runtime_ids"`
	AllowedOCIRepositories []string `json:"allowed_oci_repositories"`
	AllowedOCIPrefixes     []string `json:"allowed_oci_prefixes,omitempty"`
	AllowedPlatforms       []string `json:"allowed_platforms"`
	AllowedConfigHashes    []string `json:"allowed_config_hashes"`
	AllowedBuildPlanHashes []string `json:"allowed_build_plan_hashes"`
	AllowUnknownCapacity   bool     `json:"allow_unknown_capacity"`
	Enabled                bool     `json:"enabled"`
}

type Policy struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	Revision      uint64    `json:"revision"`
	StateHash     string    `json:"state_hash"`
	PolicyHash    string    `json:"policy_hash"`
	Draft         Draft     `json:"policy"`
	CreatedBy     string    `json:"created_by"`
	AppliedBy     string    `json:"applied_by"`
	CreatedAt     time.Time `json:"created_at"`
	AppliedAt     time.Time `json:"applied_at"`
}

type Preview struct {
	Draft      Draft  `json:"policy"`
	PolicyHash string `json:"policy_hash"`
	StateHash  string `json:"state_hash"`
}

type DiffEntry struct {
	Field  string `json:"field"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

type Diff struct {
	PolicyID        string      `json:"policy_id,omitempty"`
	CurrentRevision uint64      `json:"current_revision"`
	CurrentHash     string      `json:"current_hash,omitempty"`
	ProposedHash    string      `json:"proposed_hash"`
	Changes         []DiffEntry `json:"changes"`
}

type ApplyRequest struct {
	PolicyID          string `json:"policy_id,omitempty"`
	Draft             Draft  `json:"policy"`
	ExpectedRevision  uint64 `json:"expected_revision"`
	ExpectedStateHash string `json:"expected_state_hash"`
}

type ApplyResult struct {
	Policy Policy `json:"policy"`
	Reused bool   `json:"reused"`
}

type DisableRequest struct {
	ExpectedRevision  uint64 `json:"expected_revision"`
	ExpectedStateHash string `json:"expected_state_hash"`
}

type RoutingRequest struct {
	BuildRecordID string `json:"build_record_id"`
	EnvironmentID string `json:"environment_id"`
}

type RoutingDecision struct {
	SchemaVersion            string    `json:"schema_version"`
	ProjectID                string    `json:"project_id"`
	BuildRecordID            string    `json:"build_record_id"`
	ServiceKey               string    `json:"service_key"`
	EnvironmentID            string    `json:"environment_id"`
	RuntimeID                string    `json:"runtime_id,omitempty"`
	NodeID                   string    `json:"node_id,omitempty"`
	AgentID                  string    `json:"agent_id,omitempty"`
	TopologyPlanID           string    `json:"topology_plan_id,omitempty"`
	TopologyRevision         uint64    `json:"topology_revision,omitempty"`
	DeploymentPolicyID       string    `json:"deployment_policy_id,omitempty"`
	DeploymentPolicyRevision uint64    `json:"deployment_policy_revision,omitempty"`
	Eligible                 bool      `json:"eligible"`
	DecisionCode             string    `json:"decision_code"`
	Message                  string    `json:"message"`
	DecisionHash             string    `json:"decision_hash"`
	UnknownCapacityOverride  bool      `json:"unknown_capacity_override"`
	DecidedAt                time.Time `json:"decided_at"`
}
