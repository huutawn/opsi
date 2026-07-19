// Package topologyv1 defines manual placement contracts. Exposure is intent
// metadata only; this contract does not authorize or execute runtime changes.
package topologyv1

import "time"

const SchemaVersion = "opsi.topology_plan/v1"

type ExposureIntent struct {
	Mode string `json:"mode"`
}

type Rationale struct {
	Summary string `json:"summary,omitempty"`
}

type EnvironmentFact struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
}

type RuntimeFact struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Status        string `json:"status"`
}

type NodeFact struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	RuntimeID  string     `json:"runtime_id"`
	Status     string     `json:"status"`
	CPUCores   int        `json:"cpu_cores,omitempty"`
	MemoryMB   int        `json:"memory_mb,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type AgentFact struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	RuntimeID    string         `json:"runtime_id"`
	NodeID       string         `json:"node_id"`
	Status       string         `json:"status"`
	Capabilities map[string]any `json:"capabilities"`
	LastSeenAt   *time.Time     `json:"last_seen_at,omitempty"`
}

type ServiceFact struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Key       string `json:"key"`
}

type PlacementFacts struct {
	ProjectID    string            `json:"project_id"`
	Environments []EnvironmentFact `json:"environments"`
	Runtimes     []RuntimeFact     `json:"runtimes"`
	Nodes        []NodeFact        `json:"nodes"`
	Agents       []AgentFact       `json:"agents"`
	Services     []ServiceFact     `json:"services"`
}

type Assignment struct {
	ServiceKey           string         `json:"service_key"`
	EnvironmentID        string         `json:"environment_id"`
	RuntimeID            string         `json:"runtime_id"`
	Replicas             int32          `json:"replicas"`
	CPURequestMillicores int64          `json:"cpu_request_millicores"`
	MemoryRequestBytes   int64          `json:"memory_request_bytes"`
	Exposure             ExposureIntent `json:"exposure"`
	Rationale            Rationale      `json:"rationale,omitempty"`
}

type Draft struct {
	SchemaVersion string       `json:"schema_version"`
	ProjectID     string       `json:"project_id"`
	Assignments   []Assignment `json:"assignments"`
}

type Plan struct {
	SchemaVersion string       `json:"schema_version"`
	ID            string       `json:"id"`
	ProjectID     string       `json:"project_id"`
	Revision      uint64       `json:"revision"`
	StateHash     string       `json:"state_hash"`
	PlanHash      string       `json:"plan_hash"`
	Assignments   []Assignment `json:"assignments"`
	CreatedBy     string       `json:"created_by"`
	AppliedBy     string       `json:"applied_by"`
	CreatedAt     time.Time    `json:"created_at"`
	AppliedAt     time.Time    `json:"applied_at"`
}

type Preview struct {
	Draft     Draft  `json:"draft"`
	PlanHash  string `json:"plan_hash"`
	StateHash string `json:"state_hash"`
}

type Issue struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	ServiceKey string `json:"service_key,omitempty"`
	RuntimeID  string `json:"runtime_id,omitempty"`
	Severity   string `json:"severity"`
}

type Capacity struct {
	RuntimeID                     string     `json:"runtime_id"`
	NodeID                        string     `json:"node_id,omitempty"`
	AgentID                       string     `json:"agent_id,omitempty"`
	Source                        string     `json:"source"`
	SourceRevision                uint64     `json:"source_revision,omitempty"`
	ObservedAt                    *time.Time `json:"observed_at,omitempty"`
	HeartbeatAgeSeconds           int64      `json:"heartbeat_age_seconds,omitempty"`
	HeartbeatFresh                bool       `json:"heartbeat_fresh"`
	CPUCapacityMillicores         int64      `json:"cpu_capacity_millicores,omitempty"`
	MemoryCapacityBytes           int64      `json:"memory_capacity_bytes,omitempty"`
	ReservedCPUMillicores         int64      `json:"reserved_cpu_millicores"`
	ReservedMemoryBytes           int64      `json:"reserved_memory_bytes"`
	AssignedCPUMillicores         int64      `json:"assigned_cpu_millicores"`
	AssignedMemoryBytes           int64      `json:"assigned_memory_bytes"`
	RequestedCPUMillicores        int64      `json:"requested_cpu_millicores"`
	RequestedMemoryBytes          int64      `json:"requested_memory_bytes"`
	AvailableCPUMillicores        int64      `json:"available_cpu_millicores,omitempty"`
	AvailableMemoryBytes          int64      `json:"available_memory_bytes,omitempty"`
	UnknownCapacity               bool       `json:"unknown_capacity"`
	UnknownCapacityPolicyOverride bool       `json:"unknown_capacity_policy_override"`
	Oversubscribed                bool       `json:"oversubscribed"`
}

type RuntimeValidation struct {
	RuntimeID string   `json:"runtime_id"`
	Eligible  bool     `json:"eligible"`
	Capacity  Capacity `json:"capacity"`
	Issues    []Issue  `json:"issues"`
}

type ValidationResult struct {
	SchemaVersion string              `json:"schema_version"`
	ProjectID     string              `json:"project_id"`
	PlanHash      string              `json:"plan_hash"`
	Valid         bool                `json:"valid"`
	Runtimes      []RuntimeValidation `json:"runtimes"`
	Issues        []Issue             `json:"issues"`
	ValidatedAt   time.Time           `json:"validated_at"`
}

type DiffEntry struct {
	ServiceKey string      `json:"service_key"`
	Change     string      `json:"change"`
	Before     *Assignment `json:"before,omitempty"`
	After      *Assignment `json:"after,omitempty"`
}

type Diff struct {
	ProjectID       string      `json:"project_id"`
	CurrentRevision uint64      `json:"current_revision"`
	CurrentHash     string      `json:"current_hash,omitempty"`
	ProposedHash    string      `json:"proposed_hash"`
	Changes         []DiffEntry `json:"changes"`
}

type ApplyRequest struct {
	Draft             Draft  `json:"draft"`
	ExpectedRevision  uint64 `json:"expected_revision"`
	ExpectedStateHash string `json:"expected_state_hash"`
	PolicyID          string `json:"policy_id,omitempty"`
}

type ApplyResult struct {
	Plan   Plan `json:"plan"`
	Reused bool `json:"reused"`
}

type OperatorCapacityDraft struct {
	RuntimeID             string `json:"runtime_id"`
	CPUMillicores         int64  `json:"cpu_millicores"`
	MemoryBytes           int64  `json:"memory_bytes"`
	ReservedCPUMillicores int64  `json:"reserved_cpu_millicores"`
	ReservedMemoryBytes   int64  `json:"reserved_memory_bytes"`
}

type OperatorCapacity struct {
	ID                    string    `json:"id"`
	ProjectID             string    `json:"project_id"`
	RuntimeID             string    `json:"runtime_id"`
	Revision              uint64    `json:"revision"`
	Source                string    `json:"source"`
	CPUMillicores         int64     `json:"cpu_millicores"`
	MemoryBytes           int64     `json:"memory_bytes"`
	ReservedCPUMillicores int64     `json:"reserved_cpu_millicores"`
	ReservedMemoryBytes   int64     `json:"reserved_memory_bytes"`
	DeclaredBy            string    `json:"declared_by"`
	DeclaredAt            time.Time `json:"declared_at"`
	StateHash             string    `json:"state_hash"`
}

type OperatorCapacityApplyRequest struct {
	Draft             OperatorCapacityDraft `json:"draft"`
	ExpectedRevision  uint64                `json:"expected_revision"`
	ExpectedStateHash string                `json:"expected_state_hash"`
}

type OperatorCapacityApplyResult struct {
	Capacity OperatorCapacity `json:"capacity"`
	Reused   bool             `json:"reused"`
}
