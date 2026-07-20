package cloudclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type BuildRecord = buildrecordv1.Record
type BuildRecordList = buildrecordv1.ListResult
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
type WorkloadSpec = deploymentv1.WorkloadSpec
type WorkloadResources = deploymentv1.Resources
type WorkloadResourceValues = deploymentv1.ResourceValues
type WorkloadProbe = deploymentv1.Probe
type WorkloadEnvironmentVariable = deploymentv1.EnvironmentVariable
type WorkloadSecretReference = deploymentv1.SecretReference
type WorkloadExposureIntent = deploymentv1.ExposureIntent

type DeploymentJob struct {
	SchemaVersion          string                    `json:"schema_version,omitempty"`
	Mode                   string                    `json:"mode,omitempty"`
	ID                     string                    `json:"id"`
	ProjectID              string                    `json:"project_id"`
	EnvironmentID          string                    `json:"environment_id"`
	RuntimeID              string                    `json:"runtime_id"`
	ServiceID              string                    `json:"service_id"`
	Status                 string                    `json:"status"`
	AgentID                string                    `json:"agent_id,omitempty"`
	NodeID                 string                    `json:"node_id,omitempty"`
	FailureCode            string                    `json:"failure_code,omitempty"`
	FailureMessageRedacted string                    `json:"failure_message_redacted,omitempty"`
	LeaseExpiresAt         *time.Time                `json:"lease_expires_at,omitempty"`
	RetryAfter             *time.Time                `json:"retry_after,omitempty"`
	AttemptCount           int                       `json:"attempt_count,omitempty"`
	MaxAttempts            int                       `json:"max_attempts,omitempty"`
	StartedAt              *time.Time                `json:"started_at,omitempty"`
	FinishedAt             *time.Time                `json:"finished_at,omitempty"`
	CreatedAt              time.Time                 `json:"created_at"`
	UpdatedAt              time.Time                 `json:"updated_at"`
	Snapshot               *deploymentv1.JobSnapshot `json:"snapshot,omitempty"`
	SpecHash               string                    `json:"spec_hash,omitempty"`
	Reused                 bool                      `json:"reused,omitempty"`
	TerminalResult         *deploymentv1.AgentResult `json:"terminal_result,omitempty"`
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
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("Cloud API returned status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("Cloud API %s (status %d): %s", e.Code, e.Status, e.Message)
}

type Service struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type Node struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
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
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	ServiceID      string `json:"service_id"`
	RepositoryID   int64  `json:"repository_id"`
	InstallationID int64  `json:"installation_id"`
	ServiceKey     string `json:"service_key"`
	ConfigPath     string `json:"config_path"`
	Status         string `json:"status"`
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
