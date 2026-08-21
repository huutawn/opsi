package cloudclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

type BuildRecord = buildrecordv1.Record
type BuildRecordList = buildrecordv1.ListResult
type ManagedResource = resourcev1.Resource
type ManagedResourceBinding = resourcev1.Binding
type ApplicationDependency = serviceconfigurationv1.ApplicationDependency
type DependencyVerificationContract = serviceconfigurationv1.DependencyVerificationContract
type DependencyInjectionMapping = serviceconfigurationv1.DependencyInjectionMapping
type VerificationRun = verificationv1.VerificationRun
type TopologyDraft = topologyv1.Draft
type TopologyPlan = topologyv1.Plan
type TopologyPreview = topologyv1.Preview
type TopologyValidation = topologyv1.ValidationResult
type TopologyDiff = topologyv1.Diff
type TopologyApplyRequest = topologyv1.ApplyRequest
type TopologyApplyResult = topologyv1.ApplyResult
type OperatorCapacity = topologyv1.OperatorCapacity
type OperatorCapacityApplyRequest = topologyv1.OperatorCapacityApplyRequest
type OperatorCapacityApplyResult = topologyv1.OperatorCapacityApplyResult
type DeploymentPolicyDraft = deploymentpolicyv1.Draft
type DeploymentPolicy = deploymentpolicyv1.Policy
type DeploymentPolicyPreview = deploymentpolicyv1.Preview
type DeploymentPolicyDiff = deploymentpolicyv1.Diff
type DeploymentPolicyApplyRequest = deploymentpolicyv1.ApplyRequest
type DeploymentPolicyApplyResult = deploymentpolicyv1.ApplyResult
type DeploymentPolicyDisableRequest = deploymentpolicyv1.DisableRequest
type RoutingRequest = deploymentpolicyv1.RoutingRequest
type RoutingDecision = deploymentpolicyv1.RoutingDecision
type PlacementFacts = topologyv1.PlacementFacts
type DeploymentCreateRequest = deploymentv1.CreateRequest
type DeploymentPreview = deploymentv1.Preview
type PreflightResult = deploymentv1.PreflightResult
type PreflightCheck = deploymentv1.PreflightCheck
type WorkloadSpec = deploymentv1.WorkloadSpec
type WorkloadResources = deploymentv1.Resources
type WorkloadResourceValues = deploymentv1.ResourceValues
type WorkloadProbe = deploymentv1.Probe
type WorkloadEnvironmentVariable = deploymentv1.EnvironmentVariable
type WorkloadSecretReference = deploymentv1.SecretReference
type WorkloadExposureIntent = deploymentv1.ExposureIntent
type ExposureSpec = exposurev1.ExposureSpec
type ExposureMutationRequest = deploymentv1.ExposureMutationRequest
type ExposurePreview = deploymentv1.ExposurePreview
type ActionDevice = actionv1.ActionDevice

type DeploymentJob struct {
	SchemaVersion          string                      `json:"schema_version,omitempty"`
	Mode                   string                      `json:"mode,omitempty"`
	ID                     string                      `json:"id"`
	ProjectID              string                      `json:"project_id"`
	EnvironmentID          string                      `json:"environment_id"`
	RuntimeID              string                      `json:"runtime_id"`
	ServiceID              string                      `json:"service_id"`
	Status                 string                      `json:"status"`
	AgentID                string                      `json:"agent_id,omitempty"`
	NodeID                 string                      `json:"node_id,omitempty"`
	FailureCode            string                      `json:"failure_code,omitempty"`
	FailureMessageRedacted string                      `json:"failure_message_redacted,omitempty"`
	LeaseExpiresAt         *time.Time                  `json:"lease_expires_at,omitempty"`
	RetryAfter             *time.Time                  `json:"retry_after,omitempty"`
	AttemptCount           int                         `json:"attempt_count,omitempty"`
	MaxAttempts            int                         `json:"max_attempts,omitempty"`
	StartedAt              *time.Time                  `json:"started_at,omitempty"`
	FinishedAt             *time.Time                  `json:"finished_at,omitempty"`
	CreatedAt              time.Time                   `json:"created_at"`
	UpdatedAt              time.Time                   `json:"updated_at"`
	Snapshot               *deploymentv1.JobSnapshot   `json:"snapshot,omitempty"`
	SpecHash               string                      `json:"spec_hash,omitempty"`
	Reused                 bool                        `json:"reused,omitempty"`
	TerminalResult         *deploymentv1.AgentResult   `json:"terminal_result,omitempty"`
	BaseDeploymentID       string                      `json:"base_deployment_id,omitempty"`
	RolloutIntent          *deploymentv1.RolloutIntent `json:"rollout_intent,omitempty"`
	RolloutState           string                      `json:"rollout_state,omitempty"`
	RolloutStateHash       string                      `json:"rollout_state_hash,omitempty"`
	DesiredDigest          string                      `json:"desired_digest,omitempty"`
	CurrentDigest          string                      `json:"current_digest,omitempty"`
	PreviousDigest         string                      `json:"previous_digest,omitempty"`
	ExposureSpec           *exposurev1.ExposureSpec    `json:"exposure_spec,omitempty"`
	KnownGoodID            string                      `json:"known_good_id,omitempty"`
	KnownGoodHash          string                      `json:"known_good_hash,omitempty"`
	ReadinessEvidenceHash  string                      `json:"readiness_evidence_hash,omitempty"`
}

type DeploymentEvent struct {
	SchemaVersion   string    `json:"schema_version,omitempty"`
	ID              string    `json:"id"`
	DeploymentID    string    `json:"deployment_id"`
	Step            string    `json:"step"`
	MessageRedacted string    `json:"message_redacted"`
	ProgressPercent int       `json:"progress_percent"`
	Attempt         int       `json:"attempt,omitempty"`
	RequestID       string    `json:"request_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	RolloutID       string    `json:"rollout_id,omitempty"`
	IntentHash      string    `json:"intent_hash,omitempty"`
	StateHash       string    `json:"state_hash,omitempty"`
	EvidenceHash    string    `json:"readiness_evidence_hash,omitempty"`
}

type APIError struct {
	Status     int
	Code       string
	Message    string
	RequestID  string
	NextAction string
}

type Project struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Status    string    `json:"status"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AuditEvent struct {
	ID               string         `json:"id"`
	OrgID            string         `json:"org_id"`
	ProjectID        string         `json:"project_id,omitempty"`
	ActorUserID      string         `json:"actor_user_id,omitempty"`
	ActorType        string         `json:"actor_type"`
	Action           string         `json:"action"`
	ResourceType     string         `json:"resource_type"`
	ResourceID       string         `json:"resource_id"`
	Result           string         `json:"result"`
	MetadataRedacted map[string]any `json:"metadata_redacted,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

func (e *APIError) Error() string {
	code := e.Code
	if code == "" {
		code = fmt.Sprintf("HTTP_%d", e.Status)
	}
	value := fmt.Sprintf("Cloud API %s (status %d): %s", code, e.Status, e.Message)
	if e.RequestID != "" {
		value += " (request_id=" + e.RequestID + ")"
	}
	if e.NextAction != "" {
		value += " (next_action=" + e.NextAction + ")"
	}
	return value
}

type Service struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type ServiceBinding struct {
	Kind             string `json:"kind"`
	TargetServiceID  string `json:"target_service_id"`
	TargetServiceKey string `json:"target_service_key"`
	EnvPrefix        string `json:"env_prefix,omitempty"`
	EnvName          string `json:"env_name,omitempty"`
	Path             string `json:"path,omitempty"`
}

type PublicRouteIntent struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

type ServiceConfigurationResourceBinding = serviceconfigurationv1.ResourceBinding

type ServiceConfigurationDraft struct {
	SchemaVersion    string                                 `json:"schema_version"`
	Environment      []deploymentv1.EnvironmentVariable     `json:"environment,omitempty"`
	PublicRoute      *PublicRouteIntent                     `json:"public_route,omitempty"`
	Bindings         []ServiceBinding                       `json:"bindings,omitempty"`
	ResourceBindings []ServiceConfigurationResourceBinding  `json:"resource_bindings,omitempty"`
	Dependencies     []ApplicationDependency                `json:"dependencies,omitempty"`
}

type SourceRiskFinding struct {
	FindingID             string `json:"finding_id"`
	RuleID                string `json:"rule_id"`
	Severity              string `json:"severity"`
	Confidence            string `json:"confidence"`
	Category              string `json:"category"`
	DependencyLogicalName string `json:"dependency_logical_name,omitempty"`
	File                  string `json:"file"`
	Line                  int    `json:"line"`
	Column                int    `json:"column,omitempty"`
	SafeEvidence          string `json:"safe_evidence"`
	RemediationCode       string `json:"remediation_code,omitempty"`
}

type SourceRiskEnvReference struct {
	EnvKey string `json:"env_key"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

type SourceRiskReport struct {
	ID              string                   `json:"id"`
	ProjectID       string                   `json:"project_id"`
	ApplicationID   string                   `json:"application_id"`
	RepositoryID    int64                    `json:"repository_id"`
	CommitSHA       string                   `json:"commit_sha"`
	ApplicationRoot string                   `json:"application_root"`
	ScannerVersion  string                   `json:"scanner_version"`
	BuildJobID      string                   `json:"build_job_id,omitempty"`
	AnalysisStatus  string                   `json:"analysis_status"`
	FilesScanned    int                      `json:"files_scanned"`
	BytesScanned    int64                    `json:"bytes_scanned"`
	Truncated       bool                     `json:"truncated"`
	Findings        []SourceRiskFinding      `json:"findings"`
	EnvReferences   []SourceRiskEnvReference `json:"env_references"`
	ReportHash      string                   `json:"report_hash"`
	CreatedAt       time.Time                `json:"created_at"`
}

type ServiceConfiguration struct {
	ServiceConfigurationDraft
	Revision  uint64     `json:"revision"`
	StateHash string     `json:"state_hash"`
	AppliedBy string     `json:"applied_by,omitempty"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
}

type GeneratedEnvironment struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Binding int    `json:"binding"`
}

type ServiceConfigurationPreview struct {
	Configuration        ServiceConfigurationDraft `json:"configuration"`
	GeneratedEnvironment []GeneratedEnvironment    `json:"generated_environment,omitempty"`
	CurrentRevision      uint64                    `json:"current_revision"`
	CurrentStateHash     string                    `json:"current_state_hash"`
	DraftStateHash       string                    `json:"draft_state_hash"`
}

type ServiceConfigurationValidation struct {
	Valid  bool `json:"valid"`
	Issues []struct {
		Code    string `json:"code"`
		Field   string `json:"field,omitempty"`
		Message string `json:"message"`
	} `json:"issues,omitempty"`
}

type ServiceConfigurationDiff struct {
	Changes []struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Name   string `json:"name,omitempty"`
		Before string `json:"before,omitempty"`
		After  string `json:"after,omitempty"`
	} `json:"changes"`
}

type ServiceConfigurationApplyRequest struct {
	Draft             ServiceConfigurationDraft `json:"draft"`
	ExpectedRevision  uint64                    `json:"expected_revision"`
	ExpectedStateHash string                    `json:"expected_state_hash"`
}

type ServiceConfigurationApplyResult struct {
	Configuration ServiceConfiguration `json:"configuration"`
	Reused        bool                 `json:"reused"`
}

type Node struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	Name               string `json:"name"`
	Role               string `json:"role"`
	Status             string `json:"status"`
	PublicHost         string `json:"public_host"`
	AgentID            string `json:"agent_id"`
	AgentVersion       string `json:"agent_version"`
	AgentEndpoint      string `json:"agent_endpoint"`
	AgentPort          int    `json:"agent_port"`
	AgentTLSServerName string `json:"agent_tls_server_name"`
	AgentCertSHA256    string `json:"agent_cert_sha256"`
}

type nodeListResponse struct {
	Nodes []Node `json:"nodes"`
}

func (r *nodeListResponse) UnmarshalJSON(data []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	rawNodes, ok := envelope["nodes"]
	if !ok {
		return errors.New("nodes envelope is missing")
	}
	var nodes []Node
	if err := json.Unmarshal(rawNodes, &nodes); err != nil {
		return err
	}
	if nodes == nil {
		return errors.New("nodes must be an array")
	}
	r.Nodes = nodes
	return nil
}

type BootstrapRequest struct {
	Role          string `json:"role"`
	PublicHost    string `json:"public_host"`
	SSHPort       int    `json:"ssh_port"`
	SSHUsername   string `json:"ssh_username"`
	AuthMethod    string `json:"auth_method"`
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHPassword   string `json:"ssh_password,omitempty"`
	K3SToken      string `json:"k3s_token,omitempty"`
}

type BootstrapSession struct {
	ID                  string              `json:"id"`
	OrgID               string              `json:"org_id"`
	ProjectID           string              `json:"project_id"`
	EnvironmentID       string              `json:"environment_id"`
	RuntimeID           string              `json:"runtime_id"`
	NodeID              string              `json:"node_id,omitempty"`
	Role                string              `json:"role"`
	Status              string              `json:"status"`
	PublicHost          string              `json:"public_host,omitempty"`
	SSHPort             int                 `json:"ssh_port,omitempty"`
	SSHUsername         string              `json:"ssh_username,omitempty"`
	AuthMethod          string              `json:"auth_method,omitempty"`
	ExpiresAt           time.Time           `json:"expires_at"`
	StartedAt           *time.Time          `json:"started_at,omitempty"`
	FinishedAt          *time.Time          `json:"finished_at,omitempty"`
	AttemptCount        int                 `json:"attempt_count"`
	MaxAttempts         int                 `json:"max_attempts"`
	LastFailureCode     string              `json:"last_failure_code,omitempty"`
	LastFailureRedacted string              `json:"last_failure_message_redacted,omitempty"`
	Checkpoint          BootstrapCheckpoint `json:"checkpoint"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

type BootstrapCheckpoint struct {
	SchemaVersion     int        `json:"schema_version"`
	PlanVersion       string     `json:"plan_version"`
	PlanFingerprint   string     `json:"plan_fingerprint"`
	NextStepIndex     int        `json:"next_step_index"`
	LastCompletedStep string     `json:"last_completed_step,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
}

type BootstrapEvent struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	NodeID          string    `json:"node_id,omitempty"`
	Level           string    `json:"level"`
	Step            string    `json:"step"`
	MessageRedacted string    `json:"message_redacted"`
	ProgressPercent int       `json:"progress_percent"`
	CreatedAt       time.Time `json:"created_at"`
}

type GitHubInstallation struct {
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
	Status         string `json:"status"`
	Suspended      bool   `json:"suspended"`
}

type GitHubRepository struct {
	RepositoryID     int64  `json:"repository_id"`
	InstallationID   int64  `json:"installation_id"`
	OwnerLogin       string `json:"owner_login"`
	Name             string `json:"name"`
	FullName         string `json:"full_name"`
	Archived         bool   `json:"archived"`
	Disabled         bool   `json:"disabled"`
	DefaultBranch    string `json:"default_branch"`
	Status           string `json:"status"`
	ClaimStatus      string `json:"claim_status"`
	ClaimedProjectID string `json:"claimed_project_id,omitempty"`
}

type GitHubBinding struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	ServiceID       string `json:"service_id"`
	RepositoryID    int64  `json:"repository_id"`
	InstallationID  int64  `json:"installation_id"`
	ServiceKey      string `json:"service_key"`
	ConfigPath      string `json:"config_path"`
	SelectedRef     string `json:"selected_ref"`
	ApplicationRoot string `json:"application_root"`
	BuildContext    string `json:"build_context"`
	BuildStrategy   string `json:"build_strategy"`
	DockerfilePath  string `json:"dockerfile_path,omitempty"`
	Status          string `json:"status"`
}

type RepositoryClaim struct {
	RepositoryID int64  `json:"repository_id"`
	ProjectID    string `json:"project_id"`
	Status       string `json:"status"`
}

type InstallationClaimStart struct {
	AuthorizationURL string `json:"authorization_url"`
}

type InstallationClaimResult struct {
	Installation       GitHubInstallation `json:"installation"`
	RepositoriesSynced int                `json:"repositories_synced"`
}
