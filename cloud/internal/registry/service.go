package registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	"golang.org/x/crypto/bcrypt"
)

const (
	ProjectNoNodes       = "no_nodes"
	ProjectBootstrapping = "bootstrapping"
	ProjectReady         = "ready"
	ProjectBlocked       = "blocked"

	RuntimeNoNodes      = "no_nodes"
	RuntimeProvisioning = "provisioning"
	RuntimeReady        = "ready"

	NodePending         = "pending"
	NodeAgentConnecting = "agent_connecting"
	NodeHealthy         = "healthy"
	NodeOffline         = "offline"
	NodeDraining        = "draining"
	NodeRemoved         = "removed"

	NodeLifecycleRequested   = "requested"
	NodeLifecycleAccepted    = "accepted"
	NodeLifecycleRunning     = "running"
	NodeLifecycleVerifying   = "verifying"
	NodeLifecycleCompleted   = "completed"
	NodeLifecycleFailed      = "failed"
	NodeLifecycleUnsupported = "unsupported"

	DeploymentQueued       = "queued"
	DeploymentPlanning     = "planning"
	DeploymentWaitingAgent = "waiting_agent"
	DeploymentApplying     = "applying"
	DeploymentRolloutWait  = "rollout_wait"
	DeploymentVerifying    = "verifying"
	DeploymentSucceeded    = "succeeded"
	DeploymentFailed       = "failed"
	DeploymentRollingBack  = "rolling_back"
	DeploymentRolledBack   = "rolled_back"
	DeploymentDeadLetter   = "dead_letter"

	BootstrapPending    = "pending"
	BootstrapRetryWait  = "retry_wait"
	BootstrapDeadLetter = "dead_letter"

	EventDeploymentQueued       = "DEPLOYMENT_QUEUED"
	EventDeploymentPlanCreated  = "DEPLOYMENT_PLAN_CREATED"
	EventAgentJobAccepted       = "AGENT_JOB_ACCEPTED"
	EventManifestApplyStarted   = "MANIFEST_APPLY_STARTED"
	EventManifestApplySucceeded = "MANIFEST_APPLY_SUCCEEDED"
	EventRolloutWaitStarted     = "ROLLOUT_WAIT_STARTED"
	EventHealthCheckPassed      = "HEALTH_CHECK_PASSED"
	EventDeploymentSucceeded    = "DEPLOYMENT_SUCCEEDED"
	EventDeploymentFailed       = "DEPLOYMENT_FAILED"
	EventRollbackAvailable      = "ROLLBACK_AVAILABLE"
	EventAgentLeaseExpired      = "AGENT_LEASE_EXPIRED"
	EventDeploymentDeadLetter   = "DEPLOYMENT_DEAD_LETTER"
	EventNodeLifecycleRequested = "NODE_LIFECYCLE_REQUESTED"
	EventNodeLifecycleAccepted  = "NODE_LIFECYCLE_ACCEPTED"
	EventNodeLifecycleCompleted = "NODE_LIFECYCLE_COMPLETED"
	EventNodeLifecycleFailed    = "NODE_LIFECYCLE_FAILED"
)

var ErrNotFound = errors.New("not found")

const (
	defaultDeploymentLeaseDuration = 5 * time.Minute
	defaultDeploymentMaxAttempts   = 3
	defaultBootstrapMaxAttempts    = 3
	bootstrapRetryBaseDelay        = 5 * time.Second
	bootstrapRetryMaximumDelay     = 5 * time.Minute
)

type APIError struct {
	Status     int    `json:"-"`
	Code       string `json:"error_code"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}

func (e APIError) Error() string { return e.Code + ": " + e.Message }

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

type Environment struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Runtime struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID string    `json:"environment_id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	ServerNodeID  string    `json:"server_node_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Node struct {
	ID                     string     `json:"id"`
	OrgID                  string     `json:"org_id"`
	ProjectID              string     `json:"project_id"`
	EnvironmentID          string     `json:"environment_id"`
	RuntimeID              string     `json:"runtime_id"`
	Name                   string     `json:"name"`
	Role                   string     `json:"role"`
	Status                 string     `json:"status"`
	PublicHost             string     `json:"public_host,omitempty"`
	PrivateIP              string     `json:"private_ip,omitempty"`
	Provider               string     `json:"provider,omitempty"`
	Region                 string     `json:"region,omitempty"`
	OSName                 string     `json:"os_name,omitempty"`
	OSVersion              string     `json:"os_version,omitempty"`
	Arch                   string     `json:"arch,omitempty"`
	CPUCores               int        `json:"cpu_cores,omitempty"`
	MemoryMB               int        `json:"memory_mb,omitempty"`
	DiskTotalGB            int        `json:"disk_total_gb,omitempty"`
	K3SRole                string     `json:"k3s_role,omitempty"`
	K3SStatus              string     `json:"k3s_status,omitempty"`
	K3SVersion             string     `json:"k3s_version,omitempty"`
	AgentID                string     `json:"agent_id,omitempty"`
	AgentVersion           string     `json:"agent_version,omitempty"`
	AgentEndpoint          string     `json:"agent_endpoint,omitempty"`
	AgentPort              int        `json:"agent_port,omitempty"`
	AgentTLSServerName     string     `json:"agent_tls_server_name,omitempty"`
	AgentCertSHA256        string     `json:"agent_cert_sha256,omitempty"`
	LastSeenAt             *time.Time `json:"last_seen_at,omitempty"`
	LastInventoryAt        *time.Time `json:"last_inventory_at,omitempty"`
	FailureCode            string     `json:"failure_code,omitempty"`
	FailureMessageRedacted string     `json:"failure_message_redacted,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type AgentHeartbeat struct {
	Version      string         `json:"version"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	K3SStatus    string         `json:"k3s_status,omitempty"`
	NodeReady    bool           `json:"node_ready"`
	Capacity     NodeCapacity   `json:"capacity,omitempty"`
}

// AgentEndpoint is public, non-secret connection metadata reported during
// bootstrap registration and consumed by the authenticated CLI connection flow.
type AgentEndpoint struct {
	Address       string `json:"agent_endpoint"`
	Port          int    `json:"agent_port"`
	TLSServerName string `json:"agent_tls_server_name"`
	CertSHA256    string `json:"agent_cert_sha256"`
}

type NodeCapacity struct {
	CPUCores    int `json:"cpu_cores,omitempty"`
	MemoryMB    int `json:"memory_mb,omitempty"`
	DiskTotalGB int `json:"disk_total_gb,omitempty"`
}

type NodeDiagnostics struct {
	Node                Node             `json:"node"`
	Agent               *Agent           `json:"agent,omitempty"`
	OpenBootstrapEvents []BootstrapEvent `json:"open_bootstrap_events,omitempty"`
	RecentDeployments   []DeploymentJob  `json:"recent_deployment_jobs,omitempty"`
	Readiness           Readiness        `json:"readiness"`
}

type NodeLifecycleJob struct {
	ID                     string     `json:"id"`
	OrgID                  string     `json:"org_id"`
	ProjectID              string     `json:"project_id"`
	RuntimeID              string     `json:"runtime_id"`
	Action                 string     `json:"action"`
	Status                 string     `json:"status"`
	TargetNodeID           string     `json:"target_node_id"`
	TargetNodeName         string     `json:"target_node_name"`
	NodeID                 string     `json:"node_id"`
	AgentID                string     `json:"agent_id,omitempty"`
	RequestedBy            string     `json:"requested_by,omitempty"`
	IdempotencyKey         string     `json:"idempotency_key,omitempty"`
	ConfirmRemove          bool       `json:"confirm_remove,omitempty"`
	LeaseToken             string     `json:"-"`
	LeaseExpiresAt         *time.Time `json:"lease_expires_at,omitempty"`
	AttemptCount           int        `json:"attempt_count"`
	MaxAttempts            int        `json:"max_attempts"`
	FailureCode            string     `json:"failure_code,omitempty"`
	FailureMessageRedacted string     `json:"failure_message_redacted,omitempty"`
	Verified               bool       `json:"verified"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	FinishedAt             *time.Time `json:"finished_at,omitempty"`
}

type NodeLifecycleLease struct {
	Job        NodeLifecycleJob `json:"job"`
	LeaseToken string           `json:"lease_token"`
}

type NodeLifecycleResult struct {
	Status                 string `json:"status"`
	LeaseToken             string `json:"lease_token,omitempty"`
	FailureCode            string `json:"failure_code,omitempty"`
	FailureMessageRedacted string `json:"failure_message_redacted,omitempty"`
	Verified               bool   `json:"verified"`
}

type Agent struct {
	ID                   string         `json:"id"`
	OrgID                string         `json:"org_id"`
	ProjectID            string         `json:"project_id"`
	RuntimeID            string         `json:"runtime_id"`
	NodeID               string         `json:"node_id"`
	PublicKeyFingerprint string         `json:"public_key_fingerprint"`
	CredentialHash       string         `json:"-"`
	Version              string         `json:"version,omitempty"`
	Capabilities         map[string]any `json:"capabilities,omitempty"`
	Status               string         `json:"status"`
	LastSeenAt           *time.Time     `json:"last_seen_at,omitempty"`
	LastRotationAt       *time.Time     `json:"last_rotation_at,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

type BootstrapSession struct {
	ID                  string              `json:"id"`
	OrgID               string              `json:"org_id"`
	ProjectID           string              `json:"project_id"`
	EnvironmentID       string              `json:"environment_id"`
	RuntimeID           string              `json:"runtime_id"`
	NodeID              string              `json:"node_id,omitempty"`
	CreatedBy           string              `json:"created_by,omitempty"`
	Role                string              `json:"role"`
	Status              string              `json:"status"`
	IdempotencyKey      string              `json:"idempotency_key"`
	PublicHost          string              `json:"public_host,omitempty"`
	SSHPort             int                 `json:"ssh_port,omitempty"`
	SSHUsername         string              `json:"ssh_username,omitempty"`
	AuthMethod          string              `json:"auth_method,omitempty"`
	ExpiresAt           time.Time           `json:"expires_at"`
	StartedAt           *time.Time          `json:"started_at,omitempty"`
	FinishedAt          *time.Time          `json:"finished_at,omitempty"`
	LeaseOwner          string              `json:"lease_owner,omitempty"`
	LeaseTokenHash      string              `json:"-"`
	LeaseExpiresAt      *time.Time          `json:"lease_expires_at,omitempty"`
	LeasedAt            *time.Time          `json:"leased_at,omitempty"`
	AttemptCount        int                 `json:"attempt_count"`
	MaxAttempts         int                 `json:"max_attempts"`
	NextAttemptAt       *time.Time          `json:"next_attempt_at,omitempty"`
	LeaseHeartbeatAt    *time.Time          `json:"lease_heartbeat_at,omitempty"`
	LastFailureCode     string              `json:"last_failure_code,omitempty"`
	LastFailureRedacted string              `json:"last_failure_message_redacted,omitempty"`
	DeadLetteredAt      *time.Time          `json:"dead_lettered_at,omitempty"`
	Checkpoint          BootstrapCheckpoint `json:"checkpoint"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

const (
	BootstrapCheckpointSchemaVersion = 1
	FirstServerBootstrapPlanVersion  = "first-server-v1"
)

type BootstrapCheckpoint struct {
	SchemaVersion     int        `json:"schema_version"`
	PlanVersion       string     `json:"plan_version"`
	PlanFingerprint   string     `json:"plan_fingerprint"`
	NextStepIndex     int        `json:"next_step_index"`
	LastCompletedStep string     `json:"last_completed_step,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
}

func BootstrapStepIDs(planVersion string) []string {
	if planVersion != FirstServerBootstrapPlanVersion {
		return nil
	}
	return []string{"preflight", "install_k3s", "install_agent", "register_agent"}
}

type BootstrapSessionLease struct {
	Session        BootstrapSession `json:"session"`
	LeaseToken     string           `json:"lease_token"`
	LeaseExpiresAt time.Time        `json:"lease_expires_at"`
}

type BootstrapFinishResult struct {
	Status          string
	FailureCode     string
	MessageRedacted string
	Retryable       bool
}

type BootstrapRecoverySummary struct {
	Recovered    []BootstrapSession
	DeadLettered []BootstrapSession
	Expired      []BootstrapSession
}

type BootstrapManualRetryResult struct {
	Session BootstrapSession
	Applied bool
}

type BootstrapEvent struct {
	ID              string    `json:"id"`
	OrgID           string    `json:"org_id"`
	ProjectID       string    `json:"project_id"`
	SessionID       string    `json:"session_id"`
	NodeID          string    `json:"node_id,omitempty"`
	Level           string    `json:"level"`
	Step            string    `json:"step"`
	MessageRedacted string    `json:"message_redacted"`
	ProgressPercent int       `json:"progress_percent"`
	CreatedAt       time.Time `json:"created_at"`
}

type ServiceRecord struct {
	ID               string            `json:"id"`
	OrgID            string            `json:"org_id"`
	ProjectID        string            `json:"project_id"`
	EnvironmentID    string            `json:"environment_id"`
	RuntimeID        string            `json:"runtime_id"`
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Status           string            `json:"status"`
	SourceType       string            `json:"source_type"`
	RepoURL          string            `json:"repo_url,omitempty"`
	Image            string            `json:"image,omitempty"`
	Branch           string            `json:"branch,omitempty"`
	GitSHA           string            `json:"git_sha,omitempty"`
	BuildMethod      string            `json:"build_method,omitempty"`
	BuildContext     string            `json:"build_context,omitempty"`
	Dockerfile       string            `json:"dockerfile,omitempty"`
	ManifestPath     string            `json:"manifest_path,omitempty"`
	WatchPaths       []string          `json:"watch_paths,omitempty"`
	ContainerPort    int               `json:"container_port,omitempty"`
	HealthPath       string            `json:"health_path,omitempty"`
	Replicas         int               `json:"replicas,omitempty"`
	ResourceRequests map[string]string `json:"resource_requests,omitempty"`
	ResourceLimits   map[string]string `json:"resource_limits,omitempty"`
	Bindings         []ServiceBinding  `json:"bindings,omitempty"`
	Namespace        string            `json:"namespace"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type ServiceDraft struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	SourceType       string            `json:"source_type"`
	RepoURL          string            `json:"repo_url"`
	Image            string            `json:"image"`
	Branch           string            `json:"branch"`
	GitSHA           string            `json:"git_sha"`
	BuildMethod      string            `json:"build_method"`
	BuildContext     string            `json:"build_context"`
	Dockerfile       string            `json:"dockerfile"`
	ManifestPath     string            `json:"manifest_path"`
	WatchPaths       []string          `json:"watch_paths"`
	ContainerPort    int               `json:"container_port"`
	HealthPath       string            `json:"health_path"`
	Replicas         int               `json:"replicas"`
	ResourceRequests map[string]string `json:"resource_requests"`
	ResourceLimits   map[string]string `json:"resource_limits"`
	Bindings         []ServiceBinding  `json:"bindings"`
}

type ServiceBinding struct {
	ServiceID       string   `json:"service_id"`
	Alias           string   `json:"alias,omitempty"`
	EnvPrefix       string   `json:"env_prefix,omitempty"`
	ExposeAsDefault bool     `json:"expose_as_default,omitempty"`
	EnvKeys         []string `json:"env_keys,omitempty"`
}

const DeploymentIntentVersion = "2026-07-opsi-deployment-intent-v1"

type DeploymentIntent struct {
	IntentVersion string                    `json:"intent_version"`
	ProjectID     string                    `json:"project_id"`
	ServiceID     string                    `json:"service_id"`
	DeploymentID  string                    `json:"deployment_id"`
	RequestedBy   string                    `json:"requested_by,omitempty"`
	Source        DeploymentIntentSource    `json:"source"`
	Image         DeploymentIntentImage     `json:"image,omitempty"`
	Runtime       DeploymentIntentRuntime   `json:"runtime"`
	Health        DeploymentIntentHealth    `json:"health"`
	Resources     map[string]any            `json:"resources,omitempty"`
	Bindings      []DeploymentIntentBinding `json:"bindings,omitempty"`
	Rollout       DeploymentIntentRollout   `json:"rollout"`
	Review        DeploymentIntentReview    `json:"review"`
}

type DeploymentIntentSource struct {
	Type         string   `json:"type"`
	RepoURL      string   `json:"repo_url,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	GitSHA       string   `json:"git_sha,omitempty"`
	BuildContext string   `json:"build_context,omitempty"`
	Dockerfile   string   `json:"dockerfile,omitempty"`
	ManifestPath string   `json:"manifest_path,omitempty"`
	WatchPaths   []string `json:"watch_paths,omitempty"`
}

type DeploymentIntentImage struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	PullPolicy string `json:"pull_policy,omitempty"`
}

type DeploymentIntentRuntime struct {
	ContainerPort int            `json:"container_port,omitempty"`
	Env           map[string]any `json:"env,omitempty"`
	Replicas      int            `json:"replicas,omitempty"`
}

type DeploymentIntentHealth struct {
	Path string `json:"path,omitempty"`
}

type DeploymentIntentBinding struct {
	ServiceID       string   `json:"service_id"`
	Alias           string   `json:"alias,omitempty"`
	EnvPrefix       string   `json:"env_prefix,omitempty"`
	ExposeAsDefault bool     `json:"expose_as_default,omitempty"`
	EnvKeys         []string `json:"env_keys,omitempty"`
}

type DeploymentIntentRollout struct {
	TimeoutSeconds int  `json:"timeout_seconds"`
	AutoRollback   bool `json:"auto_rollback"`
}

type DeploymentIntentReview struct {
	Confirmed    bool   `json:"confirmed"`
	ManifestHash string `json:"manifest_hash,omitempty"`
	IntentHash   string `json:"intent_hash,omitempty"`
}

type DeploymentJob struct {
	SchemaVersion          string                      `json:"schema_version,omitempty"`
	Mode                   string                      `json:"mode,omitempty"`
	ID                     string                      `json:"id"`
	OrgID                  string                      `json:"org_id"`
	ProjectID              string                      `json:"project_id"`
	EnvironmentID          string                      `json:"environment_id"`
	RuntimeID              string                      `json:"runtime_id"`
	ServiceID              string                      `json:"service_id"`
	Status                 string                      `json:"status"`
	Action                 string                      `json:"action,omitempty"`
	IdempotencyKey         string                      `json:"idempotency_key"`
	DeploymentPlanHash     string                      `json:"deployment_plan_hash,omitempty"`
	ManifestHash           string                      `json:"manifest_hash,omitempty"`
	IntentHash             string                      `json:"intent_hash,omitempty"`
	DeploymentIntent       *DeploymentIntent           `json:"deployment_intent,omitempty"`
	PreviousRevisionRef    string                      `json:"previous_revision_ref,omitempty"`
	RollbackEligible       bool                        `json:"rollback_eligible"`
	RollbackBlockedReason  string                      `json:"rollback_blocked_reason,omitempty"`
	RequestedBy            string                      `json:"requested_by,omitempty"`
	AgentID                string                      `json:"agent_id,omitempty"`
	NodeID                 string                      `json:"node_id,omitempty"`
	FailureCode            string                      `json:"failure_code,omitempty"`
	FailureMessageRedacted string                      `json:"failure_message_redacted,omitempty"`
	LeaseToken             string                      `json:"-"`
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
	PayloadHash            string                      `json:"payload_hash,omitempty"`
	Reused                 bool                        `json:"reused,omitempty"`
	TerminalResult         *deploymentv1.AgentResult   `json:"terminal_result,omitempty"`
	BaseDeploymentID       string                      `json:"base_deployment_id,omitempty"`
	RolloutIntent          *deploymentv1.RolloutIntent `json:"rollout_intent,omitempty"`
	RolloutState           string                      `json:"rollout_state,omitempty"`
	RolloutStateHash       string                      `json:"rollout_state_hash,omitempty"`
	RolloutVersion         uint64                      `json:"rollout_version,omitempty"`
	DesiredDigest          string                      `json:"desired_digest,omitempty"`
	CurrentDigest          string                      `json:"current_digest,omitempty"`
	PreviousDigest         string                      `json:"previous_digest,omitempty"`
	ExposureSpec           *exposurev1.ExposureSpec    `json:"exposure_spec,omitempty"`
	KnownGoodID            string                      `json:"known_good_id,omitempty"`
	KnownGoodHash          string                      `json:"known_good_hash,omitempty"`
	ReadinessEvidenceHash  string                      `json:"readiness_evidence_hash,omitempty"`
}

type deploymentLock struct {
	DeploymentID string
	ExpiresAt    time.Time
}

type DeploymentEvent struct {
	SchemaVersion   string    `json:"schema_version,omitempty"`
	ID              string    `json:"id"`
	OrgID           string    `json:"org_id"`
	ProjectID       string    `json:"project_id"`
	DeploymentID    string    `json:"deployment_id"`
	ServiceID       string    `json:"service_id"`
	Level           string    `json:"level"`
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

type DeploymentLease struct {
	Deployment DeploymentJob              `json:"deployment"`
	Service    ServiceRecord              `json:"service"`
	Action     string                     `json:"action"`
	LeaseToken string                     `json:"lease_token,omitempty"`
	Command    *deploymentv1.AgentCommand `json:"command,omitempty"`
}

type DeploymentResult struct {
	SchemaVersion          string                    `json:"schema_version,omitempty"`
	Status                 string                    `json:"status"`
	LeaseToken             string                    `json:"lease_token,omitempty"`
	FinalRevisionRef       string                    `json:"final_revision_ref,omitempty"`
	IntentHash             string                    `json:"intent_hash,omitempty"`
	FailureCode            string                    `json:"failure_code,omitempty"`
	FailureMessageRedacted string                    `json:"failure_message_redacted,omitempty"`
	RollbackEligible       bool                      `json:"rollback_eligible"`
	RollbackBlockedReason  string                    `json:"rollback_blocked_reason,omitempty"`
	SpecHash               string                    `json:"spec_hash,omitempty"`
	ApplicationImage       string                    `json:"application_image,omitempty"`
	ApplicationImageID     string                    `json:"application_image_id,omitempty"`
	Namespace              string                    `json:"namespace,omitempty"`
	DeploymentName         string                    `json:"deployment_name,omitempty"`
	ServiceName            string                    `json:"service_name,omitempty"`
	AvailableReplicas      int32                     `json:"available_replicas,omitempty"`
	RolloutResult          *deploymentv1.AgentResult `json:"rollout_result,omitempty"`
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

type Readiness struct {
	ProjectID  string `json:"project_id"`
	Status     string `json:"status"`
	CanDeploy  bool   `json:"can_deploy"`
	NextAction string `json:"next_action,omitempty"`
}

type Service struct {
	mu                      sync.Mutex
	projects                map[string]Project
	envs                    map[string]Environment
	runtimes                map[string]Runtime
	nodes                   map[string]Node
	agents                  map[string]Agent
	bootstraps              map[string]BootstrapSession
	events                  map[string][]BootstrapEvent
	services                map[string]ServiceRecord
	deployments             map[string]DeploymentJob
	lifecycles              map[string]NodeLifecycleJob
	deployEvents            map[string][]DeploymentEvent
	deployLocks             map[string]deploymentLock
	audit                   []AuditEvent
	idempotency             map[string]any
	githubInstallations     map[int64]GitHubInstallation
	githubRepositories      map[int64]GitHubRepository
	githubInstallationLinks map[string]GitHubInstallationProjectLink
	githubRepositoryClaims  map[int64]GitHubRepositoryClaim
	githubServiceBindings   map[string]GitHubServiceBinding
	githubWebhookDeliveries map[string]GitHubWebhookDelivery
	now                     func() time.Time
}

type API interface {
	CreateProject(orgID, name, slug, createdBy, key string) (Project, error)
	ListProjects(orgID string) ([]Project, error)
	ProjectReadiness(projectID string) (Readiness, error)
	ListNodes(projectID string) ([]Node, error)
	PlacementFacts(context.Context, string) (topology.Facts, error)
	NodeDiagnostics(projectID, nodeID string) (NodeDiagnostics, error)
	UpsertNode(projectID, name, role, status, publicHost, agentID, key string) (Node, error)
	RegisterAgent(projectID, nodeID, fingerprint, credentialHash, version, key string, capabilities map[string]any, endpoints ...AgentEndpoint) (Agent, error)
	RecordAgentHeartbeat(projectID, nodeID string, heartbeat AgentHeartbeat) (Node, error)
	VerifyAgent(projectID, nodeID, token string) (Agent, error)
	RotateAgent(projectID, agentID, credentialHash string) (Agent, error)
	RevokeAgent(projectID, agentID string) (Agent, error)
	DrainNode(projectID, nodeID string) (Node, error)
	RemoveNode(projectID, nodeID string, force bool) (Node, error)
	MarkNodeOffline(projectID, nodeID string) (Node, error)
	RequestNodeLifecycle(projectID, targetNodeID, action, requestedBy, key, requestID string, confirmRemove, force bool) (NodeLifecycleJob, error)
	LeaseNodeLifecycle(projectID, nodeID string) (NodeLifecycleLease, bool, error)
	CompleteNodeLifecycle(projectID, nodeID, jobID, requestID string, result NodeLifecycleResult) (NodeLifecycleJob, error)
	CreateBootstrapSession(projectID, role, publicHost, username, authMethod, createdBy, key string, sshPort int) (BootstrapSession, error)
	UpdateBootstrapSession(projectID, sessionID, status, message string) (BootstrapSession, error)
	LeaseNextBootstrapSession(workerID string, now time.Time, leaseDuration time.Duration) (BootstrapSessionLease, bool, error)
	RenewBootstrapLease(projectID, sessionID, workerID, rawLeaseToken string, now time.Time, leaseDuration time.Duration) (BootstrapSession, error)
	RecoverExpiredBootstrapLeases(now time.Time) (BootstrapRecoverySummary, error)
	GetBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken string, now time.Time) (BootstrapSession, error)
	UpdateBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken, status, message string, now time.Time) (BootstrapSession, error)
	UpdateBootstrapCheckpointForLease(projectID, sessionID, workerID, leaseToken string, checkpoint BootstrapCheckpoint, now time.Time) (BootstrapSession, error)
	FinishBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken string, result BootstrapFinishResult, now time.Time) (BootstrapSession, error)
	ManualRetryBootstrapSession(projectID, sessionID, idempotencyKey string, now time.Time) (BootstrapManualRetryResult, error)
	GetBootstrapSession(projectID, sessionID string) (BootstrapSession, error)
	ListBootstrapSessions(projectID string) ([]BootstrapSession, error)
	BootstrapEvents(projectID, sessionID string) ([]BootstrapEvent, error)
	CreateService(projectID string, draft ServiceDraft, key string) (ServiceRecord, error)
	ListServices(projectID string) ([]ServiceRecord, error)
	StartDeployment(projectID, serviceID, requestedBy, key, requestID string) (DeploymentJob, error)
	RollbackDeployment(projectID, deploymentID, requestedBy, key, requestID string) (DeploymentJob, error)
	LeaseDeployment(projectID, nodeID string) (DeploymentLease, bool, error)
	CompleteDeployment(projectID, nodeID, deploymentID, requestID string, result DeploymentResult) (DeploymentJob, error)
	ListDeployments(projectID string) ([]DeploymentJob, error)
	DeploymentEvents(projectID, deploymentID string) ([]DeploymentEvent, error)
	PreviewExposure(projectID, actorUserID string, request deploymentv1.ExposureMutationRequest) (deploymentv1.ExposurePreview, error)
	StartExposureRollout(projectID, actorUserID, idempotencyKey, requestID string, request deploymentv1.ExposureMutationRequest) (DeploymentJob, bool, error)
	ListAudit(projectID string) ([]AuditEvent, error)
	UpsertGitHubInstallation(installation GitHubInstallation) (GitHubInstallation, error)
	UpsertGitHubRepository(repository GitHubRepository) (GitHubRepository, error)
	MarkGitHubInstallationStatus(installationID int64, status string, suspended bool) error
	MarkGitHubRepositoryStatus(repositoryID int64, status string) error
	RecordGitHubWebhookEvent(ctx context.Context, event GitHubWebhookMutation) (bool, error)
	ListGitHubInstallations(projectID string) ([]GitHubInstallation, error)
	ListGitHubRepositories(projectID string) ([]GitHubRepository, error)
	ClaimGitHubInstallation(projectID string, installationID int64, userID string) (GitHubInstallationProjectLink, error)
	ClaimGitHubRepository(projectID string, repositoryID int64, userID string) (GitHubRepositoryClaim, error)
	ReleaseGitHubRepository(projectID string, repositoryID int64, userID string) error
	CreateGitHubServiceBinding(projectID string, draft GitHubServiceBindingDraft) (GitHubServiceBinding, error)
	RemoveGitHubServiceBinding(projectID, bindingID, userID string) error
	ListGitHubServiceBindings(projectID string) ([]GitHubServiceBinding, error)
	ResolveBuildBinding(ctx context.Context, repositoryID uint64, serviceKey string) (buildrecord.Binding, error)
	AuditWorkload(projectID, action, resourceID, result string, metadata map[string]any)
	Audit(orgID, projectID, actorUserID, action, resourceType, resourceID, result string, metadata map[string]any)
}

func NewService() *Service {
	return &Service{
		projects:                map[string]Project{},
		envs:                    map[string]Environment{},
		runtimes:                map[string]Runtime{},
		nodes:                   map[string]Node{},
		agents:                  map[string]Agent{},
		bootstraps:              map[string]BootstrapSession{},
		events:                  map[string][]BootstrapEvent{},
		services:                map[string]ServiceRecord{},
		deployments:             map[string]DeploymentJob{},
		lifecycles:              map[string]NodeLifecycleJob{},
		deployEvents:            map[string][]DeploymentEvent{},
		deployLocks:             map[string]deploymentLock{},
		audit:                   []AuditEvent{},
		idempotency:             map[string]any{},
		githubInstallations:     map[int64]GitHubInstallation{},
		githubRepositories:      map[int64]GitHubRepository{},
		githubInstallationLinks: map[string]GitHubInstallationProjectLink{},
		githubRepositoryClaims:  map[int64]GitHubRepositoryClaim{},
		githubServiceBindings:   map[string]GitHubServiceBinding{},
		githubWebhookDeliveries: map[string]GitHubWebhookDelivery{},
	}
}

func (s *Service) CreateProject(orgID, name, slug, createdBy, key string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.idempotency["project:"+orgID+":"+key].(Project); ok {
		return got, nil
	}
	now := s.clock()
	project := Project{ID: newID("proj"), OrgID: orgID, Name: name, Slug: slug, Status: ProjectNoNodes, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now}
	if project.Name == "" {
		project.Name = project.ID
	}
	if project.Slug == "" {
		project.Slug = project.ID
	}
	env := Environment{ID: newID("env"), OrgID: orgID, ProjectID: project.ID, Name: "default", Type: "dev", Status: "active", CreatedAt: now, UpdatedAt: now}
	runtime := Runtime{ID: newID("rt"), OrgID: orgID, ProjectID: project.ID, EnvironmentID: env.ID, Name: "default", Type: "k3s", Status: RuntimeNoNodes, CreatedAt: now, UpdatedAt: now}
	s.projects[project.ID] = project
	s.envs[env.ID] = env
	s.runtimes[runtime.ID] = runtime
	s.idempotency["project:"+orgID+":"+key] = project
	return project, nil
}

func (s *Service) ListProjects(orgID string) ([]Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Project{}
	for _, project := range s.projects {
		if project.OrgID == orgID {
			out = append(out, project)
		}
	}
	return out, nil
}

func (s *Service) ProjectReadiness(projectID string) (Readiness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireBootstrapsLocked()
	return s.readinessLocked(projectID)
}

func (s *Service) ListNodes(projectID string) ([]Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	out := []Node{}
	for _, node := range s.nodes {
		if node.ProjectID == projectID {
			out = append(out, node)
		}
	}
	return out, nil
}

func (s *Service) ListServices(projectID string) ([]ServiceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	out := []ServiceRecord{}
	for _, service := range s.services {
		if service.ProjectID == projectID {
			out = append(out, service)
		}
	}
	return out, nil
}

func (s *Service) ListDeployments(projectID string) ([]DeploymentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	out := []DeploymentJob{}
	for _, job := range s.deployments {
		if job.ProjectID == projectID {
			out = append(out, job)
		}
	}
	return out, nil
}

func (s *Service) ListAudit(projectID string) ([]AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	out := []AuditEvent{}
	for _, event := range s.audit {
		if event.ProjectID == projectID {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *Service) NodeDiagnostics(projectID, nodeID string) (NodeDiagnostics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || node.ProjectID != projectID {
		return NodeDiagnostics{}, ErrNotFound
	}
	diag := NodeDiagnostics{Node: node}
	if node.AgentID != "" {
		if agent, ok := s.agents[node.AgentID]; ok {
			diag.Agent = &agent
		}
	}
	for _, session := range s.bootstraps {
		if session.ProjectID != projectID || session.NodeID != nodeID {
			continue
		}
		diag.OpenBootstrapEvents = append(diag.OpenBootstrapEvents, s.events[session.ID]...)
	}
	for _, job := range s.deployments {
		if job.ProjectID == projectID && job.RuntimeID == node.RuntimeID {
			diag.RecentDeployments = append(diag.RecentDeployments, job)
		}
	}
	readiness, err := s.readinessLocked(projectID)
	if err != nil {
		return NodeDiagnostics{}, err
	}
	diag.Readiness = readiness
	return diag, nil
}

func (s *Service) UpsertNode(projectID, name, role, status, publicHost, agentID, key string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.idempotency["node:"+projectID+":"+key].(Node); ok {
		return got, nil
	}
	project, runtime, env, ok := s.defaultScopeLocked(projectID)
	if !ok {
		return Node{}, ErrNotFound
	}
	now := s.clock()
	if role == "" {
		role = "worker"
	}
	if status == "" {
		status = "pending"
	}
	node := Node{ID: newID("node"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: name, Role: role, Status: status, PublicHost: publicHost, AgentID: agentID, CreatedAt: now, UpdatedAt: now}
	if node.Name == "" {
		node.Name = node.ID
	}
	if status == NodeHealthy {
		node.LastSeenAt = &now
	}
	s.nodes[node.ID] = node
	s.idempotency["node:"+projectID+":"+key] = node
	if role == "server" && status == NodeHealthy {
		runtime.Status = RuntimeReady
		runtime.ServerNodeID = node.ID
		runtime.UpdatedAt = now
		s.runtimes[runtime.ID] = runtime
	}
	s.refreshProjectLocked(project.ID)
	return node, nil
}

func (s *Service) RegisterAgent(projectID, nodeID, fingerprint, credentialHash, version, key string, capabilities map[string]any, endpoints ...AgentEndpoint) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.idempotency["agent:"+projectID+":"+nodeID+":"+key].(Agent); ok {
		return got, nil
	}
	node, ok := s.nodes[nodeID]
	if !ok || node.ProjectID != projectID {
		return Agent{}, ErrNotFound
	}
	if fingerprint == "" {
		return Agent{}, APIError{Status: 400, Code: "AGENT_FINGERPRINT_REQUIRED", Message: "agent public key fingerprint is required"}
	}
	if credentialHash == "" {
		return Agent{}, APIError{Status: 400, Code: "AGENT_CREDENTIAL_REQUIRED", Message: "agent credential hash is required"}
	}
	endpoint := AgentEndpoint{}
	if len(endpoints) > 1 {
		return Agent{}, APIError{Status: 400, Code: "AGENT_ENDPOINT_INVALID", Message: "only one agent endpoint is allowed"}
	}
	if len(endpoints) == 1 {
		endpoint = endpoints[0]
		if err := validateAgentEndpoint(node.PublicHost, endpoint); err != nil {
			return Agent{}, err
		}
	}
	now := s.clock()
	agent := Agent{ID: newID("agent"), OrgID: node.OrgID, ProjectID: projectID, RuntimeID: node.RuntimeID, NodeID: node.ID, PublicKeyFingerprint: fingerprint, CredentialHash: credentialHash, Version: version, Capabilities: capabilities, Status: "active", LastSeenAt: &now, CreatedAt: now, UpdatedAt: now}
	node.AgentID = agent.ID
	node.AgentVersion = version
	if len(endpoints) == 1 {
		node.AgentEndpoint = endpoint.Address
		node.AgentPort = endpoint.Port
		node.AgentTLSServerName = endpoint.TLSServerName
		node.AgentCertSHA256 = endpoint.CertSHA256
	}
	node.Status = NodeAgentConnecting
	node.LastSeenAt = &now
	node.UpdatedAt = now
	s.nodes[node.ID] = node
	s.agents[agent.ID] = agent
	s.idempotency["agent:"+projectID+":"+nodeID+":"+key] = agent
	return agent, nil
}

func (s *Service) RecordAgentHeartbeat(projectID, nodeID string, heartbeat AgentHeartbeat) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	now := s.clock()
	if node.AgentID != "" {
		agent := s.agents[node.AgentID]
		if heartbeat.Version != "" {
			agent.Version = heartbeat.Version
			node.AgentVersion = heartbeat.Version
		}
		if heartbeat.Capabilities != nil {
			agent.Capabilities = heartbeat.Capabilities
		}
		agent.LastSeenAt = &now
		agent.UpdatedAt = now
		s.agents[agent.ID] = agent
	}
	node.LastSeenAt = &now
	node.LastInventoryAt = &now
	node.CPUCores = heartbeat.Capacity.CPUCores
	node.MemoryMB = heartbeat.Capacity.MemoryMB
	node.DiskTotalGB = heartbeat.Capacity.DiskTotalGB
	if heartbeat.K3SStatus != "" {
		node.K3SStatus = heartbeat.K3SStatus
	}
	if heartbeat.NodeReady {
		node.Status = NodeHealthy
		node.FailureCode = ""
		node.FailureMessageRedacted = ""
	} else if node.Status != NodeDraining && node.Status != NodeRemoved {
		node.Status = NodeAgentConnecting
	}
	node.UpdatedAt = now
	s.nodes[node.ID] = node
	if node.Role == "server" && node.Status == NodeHealthy {
		runtime := s.runtimes[node.RuntimeID]
		runtime.Status = RuntimeReady
		runtime.ServerNodeID = node.ID
		runtime.UpdatedAt = now
		s.runtimes[runtime.ID] = runtime
	}
	if node.Status == NodeHealthy {
		for id, session := range s.bootstraps {
			if session.ProjectID != projectID || session.NodeID != nodeID || !isActiveBootstrap(session.Status) {
				continue
			}
			session.Status = "verifying"
			session.UpdatedAt = now
			s.bootstraps[id] = session
			s.events[id] = append(s.events[id], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: projectID, SessionID: id, NodeID: nodeID, Level: "info", Step: "verifying", MessageRedacted: "agent heartbeat marked node healthy; waiting for worker verification", ProgressPercent: 90, CreatedAt: now})
		}
	}
	s.refreshProjectLocked(projectID)
	return node, nil
}

func (s *Service) VerifyAgent(projectID, nodeID, token string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return Agent{}, APIError{Status: 401, Code: "AGENT_AUTH_REQUIRED", Message: "agent bearer token is required"}
	}
	now := s.clock()
	for id, agent := range s.agents {
		if agent.ProjectID != projectID || agent.NodeID != nodeID {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(agent.CredentialHash), []byte(token)) != nil {
			continue
		}
		if agent.Status != "active" {
			return Agent{}, APIError{Status: 403, Code: "AGENT_REVOKED", Message: "agent is not active"}
		}
		agent.LastSeenAt = &now
		agent.UpdatedAt = now
		s.agents[id] = agent
		return agent, nil
	}
	return Agent{}, APIError{Status: 403, Code: "AGENT_AUTH_INVALID", Message: "agent credential is invalid"}
}

func (s *Service) RotateAgent(projectID, agentID, credentialHash string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || agent.ProjectID != projectID {
		return Agent{}, ErrNotFound
	}
	if credentialHash == "" {
		return Agent{}, APIError{Status: 400, Code: "AGENT_CREDENTIAL_REQUIRED", Message: "agent credential hash is required"}
	}
	now := s.clock()
	agent.CredentialHash = credentialHash
	agent.LastRotationAt = &now
	agent.UpdatedAt = now
	s.agents[agentID] = agent
	return agent, nil
}

func (s *Service) RevokeAgent(projectID, agentID string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || agent.ProjectID != projectID {
		return Agent{}, ErrNotFound
	}
	now := s.clock()
	agent.Status = "revoked"
	agent.UpdatedAt = now
	s.agents[agentID] = agent
	return agent, nil
}

func (s *Service) DrainNode(projectID, nodeID string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	return Node{}, APIError{Status: 501, Code: "NODE_LIFECYCLE_AGENT_REQUIRED", Message: "node drain must execute through Agent/K3s; registry metadata cannot mark it complete", NextAction: "wire_agent_node_lifecycle_endpoint"}
}

func (s *Service) RemoveNode(projectID, nodeID string, force bool) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	if node.Role == "server" && !force && s.healthyServerCountLocked(projectID) <= 1 {
		return Node{}, APIError{Status: 409, Code: "ONLY_SERVER_NODE", Message: "removing the only healthy server would block the runtime", NextAction: "add_or_promote_server_first"}
	}
	return Node{}, APIError{Status: 501, Code: "NODE_LIFECYCLE_AGENT_REQUIRED", Message: "node remove must execute through Agent/K3s; registry metadata cannot mark it complete", NextAction: "wire_agent_node_lifecycle_endpoint"}
}

// MarkNodeOffline records an operator-confirmed target reset without claiming
// that Agent/Kubernetes removal was performed through the old Agent.
func (s *Service) MarkNodeOffline(projectID, nodeID string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	now := s.clock()
	node.Status = NodeOffline
	node.FailureCode = "OPERATOR_CONFIRMED_TARGET_RESET"
	node.FailureMessageRedacted = "operator confirmed target reset; record is offline"
	node.UpdatedAt = now
	s.nodes[node.ID] = node
	if node.AgentID != "" {
		agent := s.agents[node.AgentID]
		agent.Status = "revoked"
		agent.UpdatedAt = now
		s.agents[agent.ID] = agent
	}
	runtime := s.runtimes[node.RuntimeID]
	if runtime.ServerNodeID == node.ID {
		runtime.ServerNodeID = ""
		runtime.Status = RuntimeNoNodes
		runtime.UpdatedAt = now
		s.runtimes[runtime.ID] = runtime
	}
	s.refreshProjectLocked(projectID)
	return node, nil
}

func (s *Service) RequestNodeLifecycle(projectID, targetNodeID, action, requestedBy, key, requestID string, confirmRemove, force bool) (NodeLifecycleJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		if got, ok := s.idempotency["node_lifecycle:"+projectID+":"+targetNodeID+":"+action+":"+key].(NodeLifecycleJob); ok {
			return got, nil
		}
	}
	if action != "drain" && action != "remove" {
		return NodeLifecycleJob{}, APIError{Status: 400, Code: "NODE_LIFECYCLE_UNSUPPORTED", Message: "node lifecycle action is not supported", RequestID: requestID}
	}
	target, ok := s.nodes[targetNodeID]
	if !ok || target.ProjectID != projectID {
		return NodeLifecycleJob{}, ErrNotFound
	}
	if target.Name == "" {
		return NodeLifecycleJob{}, APIError{Status: 400, Code: "INVALID_NODE_TARGET", Message: "node target name is required", RequestID: requestID}
	}
	if action == "remove" {
		if target.Role == "server" && !force && s.healthyServerCountLocked(projectID) <= 1 {
			return NodeLifecycleJob{}, APIError{Status: 409, Code: "ONLY_SERVER_NODE", Message: "removing the only healthy server would block the runtime", NextAction: "add_or_promote_server_first", RequestID: requestID}
		}
		if !confirmRemove {
			return NodeLifecycleJob{}, APIError{Status: 400, Code: "REMOVE_INTENT_REQUIRED", Message: "remove requires explicit intent", RequestID: requestID}
		}
	}
	executor, agent, err := s.lifecycleAgentLocked(projectID, target.RuntimeID, target.ID, requestID)
	if err != nil {
		return NodeLifecycleJob{}, err
	}
	now := s.clock()
	job := NodeLifecycleJob{ID: newID("nlj"), OrgID: target.OrgID, ProjectID: projectID, RuntimeID: target.RuntimeID, Action: action, Status: NodeLifecycleRequested, TargetNodeID: target.ID, TargetNodeName: target.Name, NodeID: executor.ID, AgentID: agent.ID, RequestedBy: requestedBy, IdempotencyKey: key, ConfirmRemove: confirmRemove, MaxAttempts: defaultDeploymentMaxAttempts, CreatedAt: now, UpdatedAt: now}
	s.lifecycles[job.ID] = job
	if key != "" {
		s.idempotency["node_lifecycle:"+projectID+":"+targetNodeID+":"+action+":"+key] = job
	}
	return job, nil
}

func (s *Service) LeaseNodeLifecycle(projectID, nodeID string) (NodeLifecycleLease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireNodeLifecycleLeasesLocked(projectID)
	for id, job := range s.lifecycles {
		if job.ProjectID != projectID || job.NodeID != nodeID || job.Status != NodeLifecycleRequested {
			continue
		}
		now := s.clock()
		leaseExpiresAt := now.Add(defaultDeploymentLeaseDuration)
		job.Status = NodeLifecycleAccepted
		job.AttemptCount++
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		job.LeaseToken = newID("lease")
		job.LeaseExpiresAt = &leaseExpiresAt
		job.UpdatedAt = now
		s.lifecycles[id] = job
		return NodeLifecycleLease{Job: job, LeaseToken: job.LeaseToken}, true, nil
	}
	return NodeLifecycleLease{}, false, nil
}

func (s *Service) CompleteNodeLifecycle(projectID, nodeID, jobID, requestID string, result NodeLifecycleResult) (NodeLifecycleJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.lifecycles[jobID]
	if !ok || job.ProjectID != projectID || job.NodeID != nodeID {
		return NodeLifecycleJob{}, ErrNotFound
	}
	if nodeLifecycleTerminal(job.Status) {
		return job, nil
	}
	now := s.clock()
	if job.Status != NodeLifecycleAccepted || job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return NodeLifecycleJob{}, APIError{Status: 409, Code: "NODE_LIFECYCLE_LEASE_EXPIRED", Message: "node lifecycle lease is not active", NextAction: "poll_for_new_lease", RequestID: requestID}
	}
	if job.LeaseToken != "" && result.LeaseToken != job.LeaseToken {
		return NodeLifecycleJob{}, APIError{Status: 409, Code: "NODE_LIFECYCLE_STALE_LEASE", Message: "node lifecycle result lease token is stale", NextAction: "discard_result_and_poll", RequestID: requestID}
	}
	job.Status = normalizeNodeLifecycleResult(result)
	job.FailureCode = result.FailureCode
	job.FailureMessageRedacted = RedactString(result.FailureMessageRedacted)
	job.Verified = result.Verified
	job.LeaseToken = ""
	job.LeaseExpiresAt = nil
	job.FinishedAt = &now
	job.UpdatedAt = now
	if job.Status == NodeLifecycleCompleted && !job.Verified {
		job.Status = NodeLifecycleFailed
		job.FailureCode = "NODE_LIFECYCLE_NOT_VERIFIED"
		job.FailureMessageRedacted = "node lifecycle result was not verified by Agent"
	}
	if target, ok := s.nodes[job.TargetNodeID]; ok && target.ProjectID == projectID {
		target.UpdatedAt = now
		target.FailureCode = job.FailureCode
		target.FailureMessageRedacted = job.FailureMessageRedacted
		if job.Status == NodeLifecycleCompleted {
			if job.Action == "drain" {
				target.Status = NodeDraining
			}
			if job.Action == "remove" {
				target.Status = NodeRemoved
				if target.AgentID != "" {
					agent := s.agents[target.AgentID]
					agent.Status = "revoked"
					agent.UpdatedAt = now
					s.agents[agent.ID] = agent
				}
			}
		}
		s.nodes[target.ID] = target
	}
	s.lifecycles[jobID] = job
	s.refreshProjectLocked(projectID)
	return job, nil
}

func (s *Service) CreateBootstrapSession(projectID, role, publicHost, username, authMethod, createdBy, key string, sshPort int) (BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.idempotency["bootstrap:"+projectID+":"+key].(BootstrapSession); ok {
		return got, nil
	}
	project, runtime, env, ok := s.defaultScopeLocked(projectID)
	if !ok {
		return BootstrapSession{}, ErrNotFound
	}
	now := s.clock()
	if role == "" {
		role = "first_server"
		if s.hasHealthyServerLocked(projectID) {
			role = "worker"
		}
	}
	if err := s.validateBootstrapLocked(projectID, role, publicHost); err != nil {
		return BootstrapSession{}, err
	}
	node := Node{ID: newID("node"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: publicHost, Role: roleForNode(role), Status: NodePending, PublicHost: publicHost, K3SRole: k3sRoleForBootstrap(role), CreatedAt: now, UpdatedAt: now}
	for _, existing := range s.nodes {
		if existing.RuntimeID == runtime.ID && existing.Name == node.Name {
			node.Name = publicHost + "-" + node.ID[len("node-"):]
			break
		}
	}
	session := BootstrapSession{ID: newID("boot"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, NodeID: node.ID, CreatedBy: createdBy, Role: role, Status: BootstrapPending, IdempotencyKey: key, PublicHost: publicHost, SSHPort: sshPort, SSHUsername: username, AuthMethod: authMethod, ExpiresAt: now.Add(30 * time.Minute), MaxAttempts: defaultBootstrapMaxAttempts, CreatedAt: now, UpdatedAt: now}
	event := BootstrapEvent{ID: newID("evt"), OrgID: project.OrgID, ProjectID: project.ID, SessionID: session.ID, NodeID: node.ID, Level: "info", Step: "pending", MessageRedacted: "bootstrap session pending worker", ProgressPercent: 0, CreatedAt: now}
	runtime.Status = RuntimeProvisioning
	runtime.UpdatedAt = now
	s.nodes[node.ID] = node
	s.bootstraps[session.ID] = session
	s.events[session.ID] = []BootstrapEvent{event}
	s.runtimes[runtime.ID] = runtime
	s.idempotency["bootstrap:"+projectID+":"+key] = session
	s.refreshProjectLocked(project.ID)
	return session, nil
}

func (s *Service) UpdateBootstrapSession(projectID, sessionID, status, message string) (BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}
	if isTerminalBootstrap(session.Status) {
		return BootstrapSession{}, APIError{Status: 409, Code: "BOOTSTRAP_TERMINAL", Message: "terminal bootstrap session cannot change state"}
	}
	if !validBootstrapStatus(status) {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap status is invalid"}
	}
	now := s.clock()
	if status == "waiting_agent" && (!isLeasedBootstrapStatus(session.Status) || session.LeaseExpiresAt == nil || !session.LeaseExpiresAt.After(now)) {
		return BootstrapSession{}, APIError{Status: 410, Code: "BOOTSTRAP_LEASE_EXPIRED", Message: "bootstrap lease is not active"}
	}
	session.Status = status
	session.UpdatedAt = now
	if (status == "validating" || status == "preflight") && session.StartedAt == nil {
		session.StartedAt = &now
	}
	if !isActiveBootstrap(status) {
		session.FinishedAt = &now
	}
	s.bootstraps[sessionID] = session
	level := "info"
	if status == "failed" {
		level = "error"
	}
	s.events[sessionID] = append(s.events[sessionID], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: session.ProjectID, SessionID: sessionID, NodeID: session.NodeID, Level: level, Step: status, MessageRedacted: RedactString(message), ProgressPercent: bootstrapProgress(status), CreatedAt: now})
	s.refreshProjectLocked(projectID)
	return session, nil
}

func (s *Service) LeaseNextBootstrapSession(workerID string, now time.Time, leaseDuration time.Duration) (BootstrapSessionLease, bool, error) {
	if err := ValidateBootstrapWorkerID(workerID); err != nil {
		return BootstrapSessionLease{}, false, err
	}
	if leaseDuration <= 0 {
		return BootstrapSessionLease{}, false, errors.New("bootstrap lease duration must be positive")
	}
	token, tokenHash, err := newBootstrapLeaseToken()
	if err != nil {
		return BootstrapSessionLease{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoverExpiredBootstrapLeasesLocked(now.UTC())
	var selected BootstrapSession
	for _, session := range s.bootstraps {
		eligible := session.Status == "created" || session.Status == BootstrapPending || (session.Status == BootstrapRetryWait && session.NextAttemptAt != nil && !session.NextAttemptAt.After(now))
		if !eligible || session.LeaseTokenHash != "" || !now.Before(session.ExpiresAt) {
			continue
		}
		if selected.ID == "" || session.CreatedAt.Before(selected.CreatedAt) || (session.CreatedAt.Equal(selected.CreatedAt) && session.ID < selected.ID) {
			selected = session
		}
	}
	if selected.ID == "" {
		return BootstrapSessionLease{}, false, nil
	}
	now = now.UTC()
	expiresAt := now.Add(leaseDuration)
	selected.Status = "validating"
	selected.LeaseOwner = workerID
	selected.LeaseTokenHash = tokenHash
	selected.LeaseExpiresAt = &expiresAt
	selected.LeasedAt = &now
	selected.LeaseHeartbeatAt = &now
	selected.NextAttemptAt = nil
	selected.AttemptCount++
	if selected.MaxAttempts <= 0 {
		selected.MaxAttempts = defaultBootstrapMaxAttempts
	}
	selected.UpdatedAt = now
	if selected.StartedAt == nil {
		selected.StartedAt = &now
	}
	s.bootstraps[selected.ID] = selected
	s.events[selected.ID] = append(s.events[selected.ID], BootstrapEvent{ID: newID("evt"), OrgID: selected.OrgID, ProjectID: selected.ProjectID, SessionID: selected.ID, NodeID: selected.NodeID, Level: "info", Step: "validating", MessageRedacted: "bootstrap session leased by worker", ProgressPercent: bootstrapProgress("validating"), CreatedAt: now})
	return BootstrapSessionLease{Session: selected, LeaseToken: token, LeaseExpiresAt: expiresAt}, true, nil
}

func (s *Service) GetBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken string, now time.Time) (BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}
	if err := validateBootstrapLease(session, workerID, leaseToken, now); err != nil {
		return BootstrapSession{}, err
	}
	return session, nil
}

func (s *Service) UpdateBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken, status, message string, now time.Time) (BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}
	if err := validateBootstrapLease(session, workerID, leaseToken, now); err != nil {
		return BootstrapSession{}, err
	}
	if !isLeasedBootstrapStatus(status) {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap status is invalid"}
	}
	now = now.UTC()
	session.Status = status
	session.UpdatedAt = now
	s.bootstraps[sessionID] = session
	level := "info"
	if status == "failed" {
		level = "error"
	}
	s.events[sessionID] = append(s.events[sessionID], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: session.ProjectID, SessionID: sessionID, NodeID: session.NodeID, Level: level, Step: status, MessageRedacted: RedactString(message), ProgressPercent: bootstrapProgress(status), CreatedAt: now})
	s.refreshProjectLocked(projectID)
	return session, nil
}

func (s *Service) GetBootstrapSession(projectID, sessionID string) (BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireBootstrapsLocked()
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return BootstrapSession{}, ErrNotFound
	}
	return session, nil
}

func (s *Service) BootstrapEvents(projectID, sessionID string) ([]BootstrapEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireBootstrapsLocked()
	session, ok := s.bootstraps[sessionID]
	if !ok || session.ProjectID != projectID {
		return nil, ErrNotFound
	}
	return append([]BootstrapEvent(nil), s.events[sessionID]...), nil
}

func (s *Service) ListBootstrapSessions(projectID string) ([]BootstrapSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireBootstrapsLocked()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	out := []BootstrapSession{}
	for _, session := range s.bootstraps {
		if session.ProjectID == projectID {
			out = append(out, session)
		}
	}
	return out, nil
}

func (s *Service) CreateService(projectID string, draft ServiceDraft, key string) (ServiceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.idempotency["service:"+projectID+":"+key].(ServiceRecord); ok {
		return got, nil
	}
	project, runtime, env, ok := s.defaultScopeLocked(projectID)
	if !ok {
		return ServiceRecord{}, ErrNotFound
	}
	now := s.clock()
	if draft.Type == "" {
		draft.Type = "application"
	}
	if draft.SourceType == "" {
		draft.SourceType = "git"
	}
	if draft.Branch == "" {
		draft.Branch = "main"
	}
	if draft.BuildMethod == "" {
		draft.BuildMethod = "dockerfile"
	}
	if draft.BuildContext == "" {
		draft.BuildContext = "."
	}
	if draft.Dockerfile == "" {
		draft.Dockerfile = "Dockerfile"
	}
	if draft.ManifestPath == "" {
		draft.ManifestPath = "k8s/deployment.yaml"
	}
	if draft.HealthPath == "" {
		draft.HealthPath = "/health"
	}
	if draft.Replicas == 0 {
		draft.Replicas = 1
	}
	record := ServiceRecord{ID: newID("svc"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: draft.Name, Type: draft.Type, Status: "draft", SourceType: draft.SourceType, RepoURL: draft.RepoURL, Image: draft.Image, Branch: draft.Branch, GitSHA: draft.GitSHA, BuildMethod: draft.BuildMethod, BuildContext: draft.BuildContext, Dockerfile: draft.Dockerfile, ManifestPath: draft.ManifestPath, WatchPaths: draft.WatchPaths, ContainerPort: draft.ContainerPort, HealthPath: draft.HealthPath, Replicas: draft.Replicas, ResourceRequests: cloneStringMap(draft.ResourceRequests), ResourceLimits: cloneStringMap(draft.ResourceLimits), Bindings: cloneServiceBindings(draft.Bindings), Namespace: "default", CreatedAt: now, UpdatedAt: now}
	if record.Name == "" {
		record.Name = record.ID
	}
	s.services[record.ID] = record
	s.idempotency["service:"+projectID+":"+key] = record
	return record, nil
}

func (s *Service) StartDeployment(projectID, serviceID, requestedBy, key, requestID string) (DeploymentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireBootstrapsLocked()
	if got, ok := s.idempotency["deploy:"+projectID+":"+serviceID+":"+key].(DeploymentJob); ok {
		return got, nil
	}
	readiness, err := s.readinessLocked(projectID)
	if err != nil {
		return DeploymentJob{}, err
	}
	if !readiness.CanDeploy {
		return DeploymentJob{}, APIError{Status: 409, Code: "PROJECT_NOT_READY", Message: "Add a healthy server before deploying services.", NextAction: readiness.NextAction, RequestID: requestID}
	}
	service, ok := s.services[serviceID]
	if !ok || service.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	if err := validateServiceForDeploy(service, requestID); err != nil {
		return DeploymentJob{}, err
	}
	node, agent, err := s.deployAgentLocked(projectID, service.RuntimeID, requestID)
	if err != nil {
		return DeploymentJob{}, err
	}
	now := s.clock()
	previous := s.previousSuccessfulLocked(projectID, serviceID)
	job := deploymentJobForPlan(service, previous, node, agent, key, requestedBy, now)
	if err := s.acquireDeploymentLockLocked(serviceID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, err
	}
	s.deployments[job.ID] = job
	s.deployEvents[job.ID] = deploymentQueuedEvents(job, requestID, now)
	s.idempotency["deploy:"+projectID+":"+serviceID+":"+key] = job
	return job, nil
}

// StartImmutableDeployment is the canonical R5-010 create path. The caller
// supplies an authority snapshot produced by BuildRecord + routing services;
// this method only persists the snapshot and never accepts source or manifest data.
func (s *Service) StartImmutableDeployment(snapshot deploymentv1.JobSnapshot, requestedBy, key, requestID string) (DeploymentJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	if existing, ok := s.idempotency["deploy:v1:"+snapshot.ProjectID+":"+key].(DeploymentJob); ok {
		if existing.PayloadHash != snapshot.PayloadHash {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used with a different deployment payload", RequestID: requestID}
		}
		if current, exists := s.deployments[existing.ID]; exists {
			existing = current
		}
		existing.Reused = true
		return existing, true, nil
	}
	if snapshot.SchemaVersion != deploymentv1.JobSchemaVersion || snapshot.ProjectID == "" || snapshot.Authority.BuildRecord.ProjectID != snapshot.ProjectID {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "DEPLOYMENT_SNAPSHOT_INVALID", Message: "deployment authority snapshot is invalid", RequestID: requestID}
	}
	if err := snapshot.Image.Validate(); err != nil {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "BUILD_ARTIFACT_INVALID", Message: "immutable image reference is invalid", RequestID: requestID}
	}
	if err := snapshot.Workload.Validate(); err != nil {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "WORKLOAD_SPEC_INVALID", Message: "WorkloadSpec is invalid", RequestID: requestID}
	}
	if hash, err := snapshot.Workload.Hash(); err != nil || hash != snapshot.SpecHash {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "WORKLOAD_SPEC_HASH_INVALID", Message: "WorkloadSpec hash does not match", RequestID: requestID}
	}
	if snapshot.Image.Repository != snapshot.Authority.BuildRecord.Build.OCIRepository || snapshot.Image.Digest != snapshot.Authority.BuildRecord.Build.OCIDigest {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "BUILD_ARTIFACT_MISMATCH", Message: "image reference does not match the accepted BuildRecord", RequestID: requestID}
	}
	service, ok := s.services[snapshot.Authority.BuildRecord.ServiceID]
	if !ok || service.ProjectID != snapshot.ProjectID || service.Name == "" {
		return DeploymentJob{}, false, ErrNotFound
	}
	if service.ID != snapshot.Authority.BuildRecord.ServiceID || service.EnvironmentID != snapshot.Authority.EnvironmentID || service.RuntimeID != snapshot.Authority.RuntimeID {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "DEPLOYMENT_SERVICE_BINDING_INVALID", Message: "service binding does not match the resolved target", RequestID: requestID}
	}
	node, agent, err := s.deployAgentLocked(snapshot.ProjectID, snapshot.Authority.RuntimeID, requestID)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	if node.ID != snapshot.Authority.NodeID || agent.ID != snapshot.Authority.AgentID {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "ROUTING_TARGET_CHANGED", Message: "resolved Agent target changed before job creation", RequestID: requestID}
	}
	now := s.clock()
	snapshot.CreatedAt = now
	snapshot.ActorUserID = requestedBy
	snapshot.IdempotencyKey = key
	job := DeploymentJob{SchemaVersion: deploymentv1.JobSchemaVersion, Mode: "immutable_image", ID: newID("dep"), OrgID: service.OrgID, ProjectID: snapshot.ProjectID, EnvironmentID: snapshot.Authority.EnvironmentID, RuntimeID: snapshot.Authority.RuntimeID, ServiceID: service.ID, Status: deploymentv1.StateQueued, Action: "deploy", IdempotencyKey: key, RequestedBy: requestedBy, AgentID: agent.ID, NodeID: node.ID, MaxAttempts: defaultDeploymentMaxAttempts, Snapshot: &snapshot, SpecHash: snapshot.SpecHash, PayloadHash: snapshot.PayloadHash, CreatedAt: now, UpdatedAt: now}
	job.DeploymentPlanHash = hashJSON(map[string]any{"topology": snapshot.Authority.TopologyHash, "policy": snapshot.Authority.DeploymentPolicyHash, "routing": snapshot.Authority.RoutingDecisionHash, "spec": snapshot.SpecHash, "image": snapshot.Image.Reference})
	if err := s.acquireDeploymentLockLocked(service.ID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, false, err
	}
	s.deployments[job.ID] = job
	s.deployEvents[job.ID] = []DeploymentEvent{{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: deploymentv1.StateQueued, MessageRedacted: "immutable image deployment queued", ProgressPercent: 0, Attempt: job.AttemptCount, CreatedAt: now}}
	s.idempotency["deploy:v1:"+snapshot.ProjectID+":"+key] = job
	return job, false, nil
}

func (s *Service) ReplayImmutableDeployment(projectID, key, payloadHash string) (DeploymentJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.idempotency["deploy:v1:"+projectID+":"+key].(DeploymentJob)
	if !ok {
		return DeploymentJob{}, false, nil
	}
	if existing.PayloadHash != payloadHash {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used with a different deployment payload"}
	}
	if current, exists := s.deployments[existing.ID]; exists {
		existing = current
	}
	existing.Reused = true
	return existing, true, nil
}

func (s *Service) ImmutableDeploymentCommand(job DeploymentJob) *deploymentv1.AgentCommand {
	if job.Snapshot == nil {
		return nil
	}
	return &deploymentv1.AgentCommand{SchemaVersion: deploymentv1.CommandSchemaVersion, JobID: job.ID, ProjectID: job.ProjectID, EnvironmentID: job.EnvironmentID, RuntimeID: job.RuntimeID, NodeID: job.NodeID, AgentID: job.AgentID, LeaseToken: job.LeaseToken, Attempt: int32(job.AttemptCount), Image: job.Snapshot.Image, Workload: job.Snapshot.Workload, SpecHash: job.SpecHash, Rollout: job.RolloutIntent}
}

func (s *Service) RollbackDeployment(projectID, deploymentID, requestedBy, key, requestID string) (DeploymentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	if got, ok := s.idempotency["rollback:"+projectID+":"+deploymentID+":"+key].(DeploymentJob); ok {
		if current, exists := s.deployments[got.ID]; exists {
			got = current
		}
		got.Reused = true
		return got, nil
	}
	source, ok := s.deployments[deploymentID]
	if !ok || source.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	if !source.RollbackEligible {
		reason := source.RollbackBlockedReason
		if reason == "" {
			reason = "deployment is not rollback eligible"
		}
		return DeploymentJob{}, APIError{Status: 409, Code: "ROLLBACK_NOT_AVAILABLE", Message: reason, RequestID: requestID}
	}
	if source.Mode == "rollout" {
		if source.RolloutIntent == nil || source.ExposureSpec == nil || source.RolloutIntent.PreviousKnownGoodID == "" || source.TerminalResult == nil || source.TerminalResult.RolloutState != deploymentv1.RolloutStateSucceeded || source.TerminalResult.KnownGoodID == "" {
			return DeploymentJob{}, APIError{Status: 409, Code: "ROLLBACK_NOT_AVAILABLE", Message: "only a succeeded rollout with an exact previous known-good snapshot can be rolled back", RequestID: requestID}
		}
		jobID := newID("dep")
		exposure := *source.ExposureSpec
		exposure.DeploymentJobID = jobID
		exposure.SpecHash = ""
		canonical, err := exposure.Canonicalize()
		if err != nil {
			return DeploymentJob{}, err
		}
		intent, err := buildRolloutIntent(source, canonical, source.RolloutIntent.PreviousKnownGoodID, source.RolloutIntent.PreviousKnownGoodHash, source.RolloutIntent.PreviousDigest, source.TerminalResult.KnownGoodID, source.TerminalResult.KnownGoodHash, deploymentv1.RolloutOperationRollback, s.clock())
		if err != nil {
			return DeploymentJob{}, err
		}
		job := rolloutDeploymentJob(source, intent, canonical, requestedBy, key, hashJSON(map[string]string{"source": deploymentID, "operation": "rollback"}), s.clock())
		if err := s.acquireDeploymentLockLocked(job.ServiceID, job.ID, job.CreatedAt, requestID); err != nil {
			return DeploymentJob{}, err
		}
		s.deployments[job.ID] = job
		s.deployEvents[job.ID] = []DeploymentEvent{rolloutEvent(job, deploymentv1.RolloutStatePrepared, "explicit rollback prepared", 0, requestID, job.CreatedAt, "")}
		s.idempotency["rollback:"+projectID+":"+deploymentID+":"+key] = job
		return job, nil
	}
	service, ok := s.services[source.ServiceID]
	if !ok || service.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	node, agent, err := s.deployAgentLocked(projectID, service.RuntimeID, requestID)
	if err != nil {
		return DeploymentJob{}, err
	}
	now := s.clock()
	job := deploymentJobForPlan(service, source, node, agent, key, requestedBy, now)
	job.Status = DeploymentRollingBack
	job.Action = "rollback"
	if err := s.acquireDeploymentLockLocked(service.ID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, err
	}
	s.deployments[job.ID] = job
	s.deployEvents[job.ID] = []DeploymentEvent{{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: DeploymentRollingBack, MessageRedacted: "rollback queued", ProgressPercent: 0, RequestID: requestID, CreatedAt: now}}
	s.idempotency["rollback:"+projectID+":"+deploymentID+":"+key] = job
	return job, nil
}

func (s *Service) LeaseDeployment(projectID, nodeID string) (DeploymentLease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireDeploymentLeasesLocked(projectID)
	for id, job := range s.deployments {
		if job.ProjectID != projectID || job.NodeID != nodeID || (job.Status != DeploymentQueued && job.Status != DeploymentRollingBack) {
			continue
		}
		if job.RetryAfter != nil && job.RetryAfter.After(s.clock()) {
			continue
		}
		service, ok := s.services[job.ServiceID]
		if !ok {
			return DeploymentLease{}, false, ErrNotFound
		}
		action := "deploy"
		if job.Status == DeploymentRollingBack {
			action = "rollback"
		}
		if job.Action != "" {
			action = job.Action
		}
		now := s.clock()
		leaseExpiresAt := now.Add(defaultDeploymentLeaseDuration)
		if isProductionDeploymentMode(job.Mode) {
			job.Status = deploymentv1.StateLeased
		} else {
			job.Status = DeploymentWaitingAgent
		}
		job.Action = action
		job.AttemptCount++
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		job.LeaseToken = newID("lease")
		job.LeaseExpiresAt = &leaseExpiresAt
		job.RetryAfter = nil
		if job.StartedAt == nil {
			job.StartedAt = &now
		}
		job.UpdatedAt = now
		s.deployments[id] = job
		event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: id, ServiceID: job.ServiceID, Level: "info", Step: EventAgentJobAccepted, MessageRedacted: "agent accepted deployment job", ProgressPercent: 20, CreatedAt: job.UpdatedAt}
		if isProductionDeploymentMode(job.Mode) {
			event.SchemaVersion, event.Step, event.ProgressPercent, event.Attempt = deploymentv1.EventSchemaVersion, deploymentv1.StateLeased, 10, job.AttemptCount
		}
		s.deployEvents[id] = append(s.deployEvents[id], event)
		return DeploymentLease{Deployment: job, Service: service, Action: action, LeaseToken: job.LeaseToken, Command: s.ImmutableDeploymentCommand(job)}, true, nil
	}
	return DeploymentLease{}, false, nil
}

func (s *Service) CompleteDeployment(projectID, nodeID, deploymentID, requestID string, result DeploymentResult) (DeploymentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.deployments[deploymentID]
	if !ok || job.ProjectID != projectID || job.NodeID != nodeID {
		return DeploymentJob{}, ErrNotFound
	}
	if deploymentTerminalStatus(job.Status) && job.Mode != "rollout" || job.Mode == "rollout" && job.TerminalResult != nil {
		if job.Mode != "rollout" {
			return job, nil
		}
		if exactTerminalReplay(job, result) {
			return job, nil
		}
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_TERMINAL_IMMUTABLE", Message: "terminal deployment result is immutable", RequestID: requestID}
	}
	now := s.clock()
	if !deploymentLeaseActiveStatus(job.Status) || job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_LEASE_EXPIRED", Message: "deployment lease is not active", NextAction: "poll_for_new_lease", RequestID: requestID}
	}
	if job.LeaseToken != "" && result.LeaseToken != job.LeaseToken {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_STALE_LEASE", Message: "deployment result lease token is stale", NextAction: "discard_result_and_poll", RequestID: requestID}
	}
	if job.IntentHash != "" && result.IntentHash != "" && result.IntentHash != job.IntentHash {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_RESULT_MISMATCH", Message: "deployment result intent hash does not match leased job", RequestID: requestID}
	}
	if job.Mode == "immutable_image" && result.Status != deploymentv1.StateSucceeded && result.Status != deploymentv1.StateFailed {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_RESULT_STATUS_INVALID", Message: "immutable deployment result must be succeeded or failed", RequestID: requestID}
	}
	if job.Mode == "rollout" && result.RolloutResult == nil {
		return DeploymentJob{}, APIError{Status: 409, Code: "ROLLOUT_RESULT_INVALID", Message: "rollout deployment requires a sanitized rollout result", RequestID: requestID}
	}
	status := normalizedDeploymentResultStatus(result.Status)
	if job.Mode == "rollout" {
		status = result.RolloutResult.RolloutState
	}
	job.Status = status
	job.FailureCode = result.FailureCode
	job.FailureMessageRedacted = RedactString(result.FailureMessageRedacted)
	if result.FinalRevisionRef != "" {
		job.ManifestHash = result.FinalRevisionRef
	}
	job.RollbackEligible = result.RollbackEligible
	job.RollbackBlockedReason = result.RollbackBlockedReason
	job.LeaseToken = ""
	job.LeaseExpiresAt = nil
	job.FinishedAt = &now
	job.UpdatedAt = now
	if job.Mode == "rollout" {
		rollout := result.RolloutResult
		if err := validateRolloutResult(job, rollout); err != nil {
			return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_RESULT_MISMATCH", Message: err.Error(), RequestID: requestID}
		}
		copy := *rollout
		copy.LeaseToken = ""
		copy.FailureMessageRedacted = RedactString(copy.FailureMessageRedacted)
		job.TerminalResult = &copy
		job.RolloutState = copy.RolloutState
		job.RolloutStateHash = copy.StateHash
		job.DesiredDigest = copy.DesiredDigest
		job.CurrentDigest = copy.CurrentDigest
		job.PreviousDigest = copy.PreviousDigest
		job.KnownGoodID = copy.KnownGoodID
		job.KnownGoodHash = copy.KnownGoodHash
		job.ReadinessEvidenceHash = copy.ReadinessEvidenceHash
		job.RolloutVersion++
		job.RollbackEligible = copy.RolloutState == deploymentv1.RolloutStateSucceeded && job.RolloutIntent.PreviousKnownGoodID != ""
	} else if job.Mode == "immutable_image" {
		if result.SchemaVersion != deploymentv1.ResultSchemaVersion || result.SpecHash != job.SpecHash || job.Snapshot == nil || result.ApplicationImage != job.Snapshot.Image.Reference || !deploymentResultHasExactDigest(result.ApplicationImageID, job.Snapshot.Image.Digest) && status == DeploymentSucceeded {
			return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_RESULT_MISMATCH", Message: "Agent result does not match the immutable deployment command", RequestID: requestID}
		}
		job.TerminalResult = &deploymentv1.AgentResult{SchemaVersion: result.SchemaVersion, LeaseToken: result.LeaseToken, Status: status, SpecHash: result.SpecHash, ApplicationImage: result.ApplicationImage, ApplicationImageID: result.ApplicationImageID, Namespace: result.Namespace, DeploymentName: result.DeploymentName, ServiceName: result.ServiceName, AvailableReplicas: result.AvailableReplicas, FailureCode: result.FailureCode, FailureMessageRedacted: job.FailureMessageRedacted}
	}
	s.deployments[deploymentID] = job
	if job.Mode == "rollout" {
		event := rolloutEvent(job, job.RolloutState, "Agent reported terminal rollout result", 100, requestID, now, job.RolloutStateHash)
		event.EvidenceHash = job.ReadinessEvidenceHash
		s.deployEvents[deploymentID] = append(s.deployEvents[deploymentID], event)
	} else {
		s.deployEvents[deploymentID] = append(s.deployEvents[deploymentID], deploymentCompletionEvents(job, requestID, now)...)
	}
	if deploymentTerminalStatus(status) {
		delete(s.deployLocks, job.ServiceID)
	}
	return job, nil
}

func (s *Service) ProgressImmutableDeployment(projectID, nodeID, deploymentID, requestID string, progress deploymentv1.Progress) (DeploymentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.deployments[deploymentID]
	if !ok || job.ProjectID != projectID || job.NodeID != nodeID || !isProductionDeploymentMode(job.Mode) {
		return DeploymentJob{}, ErrNotFound
	}
	now := s.clock()
	if job.Mode == "rollout" && job.TerminalResult != nil || job.Mode != "rollout" && (deploymentTerminalStatus(job.Status) || job.Status == deploymentv1.StateCancelled) {
		return job, nil
	}
	if job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) || progress.LeaseToken == "" || progress.LeaseToken != job.LeaseToken {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_STALE_LEASE", Message: "deployment progress lease is stale", RequestID: requestID}
	}
	if job.Mode == "rollout" {
		if err := validateRolloutProgress(job, progress); err != nil {
			return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_STATE_INVALID", Message: err.Error(), RequestID: requestID}
		}
	} else if !validImmutableTransition(job.Status, progress.State) {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_STATE_INVALID", Message: "deployment state transition is not monotonic", RequestID: requestID}
	}
	message := RedactString(progress.MessageRedacted)
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = "deployment progress updated"
	}
	rolloutReplay := job.Mode == "rollout" && job.RolloutState == progress.State && job.RolloutStateHash == progress.StateHash
	if job.Mode == "rollout" {
		if !rolloutReplay {
			if !deploymentv1.IsTerminalRolloutState(progress.State) {
				job.Status = progress.State
			}
			job.RolloutState = progress.State
			job.RolloutStateHash = progress.StateHash
			job.RolloutVersion++
			job.DesiredDigest = progress.DesiredDigest
			job.CurrentDigest = progress.CurrentDigest
			job.PreviousDigest = progress.PreviousDigest
			job.ReadinessEvidenceHash = progress.ReadinessEvidenceHash
			job.FailureCode = progress.FailureCode
		}
	} else {
		job.Status = progress.State
	}
	leaseExpiresAt := now.Add(defaultDeploymentLeaseDuration)
	job.LeaseExpiresAt = &leaseExpiresAt
	job.UpdatedAt = now
	s.deployments[deploymentID] = job
	if !rolloutReplay && len(s.deployEvents[deploymentID]) < 199 {
		percent := immutableProgressPercent(progress.State)
		if progress.ProgressPercent > 0 {
			percent = int(progress.ProgressPercent)
		}
		s.deployEvents[deploymentID] = append(s.deployEvents[deploymentID], DeploymentEvent{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: progress.State, MessageRedacted: message, ProgressPercent: percent, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now, RolloutID: progress.RolloutID, IntentHash: progress.IntentHash, StateHash: progress.StateHash, EvidenceHash: progress.ReadinessEvidenceHash})
	}
	return job, nil
}

func validImmutableTransition(current, next string) bool {
	rank := map[string]int{deploymentv1.StateLeased: 1, deploymentv1.StatePulling: 2, deploymentv1.StateApplying: 3, deploymentv1.StateWaitingReady: 4}
	return rank[next] != 0 && (rank[next] == rank[current] || rank[next] == rank[current]+1)
}

func immutableProgressPercent(state string) int {
	switch state {
	case deploymentv1.StateLeased:
		return 10
	case deploymentv1.StatePulling:
		return 25
	case deploymentv1.StateApplying:
		return 55
	case deploymentv1.StateWaitingReady:
		return 75
	default:
		return 0
	}
}

func (s *Service) expireDeploymentLeasesLocked(projectID string) {
	now := s.clock()
	for id, job := range s.deployments {
		if job.ProjectID != projectID || !deploymentLeaseActiveStatus(job.Status) || job.LeaseExpiresAt == nil || job.LeaseExpiresAt.After(now) {
			continue
		}
		job.LeaseToken = ""
		job.LeaseExpiresAt = nil
		job.RetryAfter = nil
		job.UpdatedAt = now
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		if job.AttemptCount >= job.MaxAttempts {
			job.Status = DeploymentDeadLetter
			if isProductionDeploymentMode(job.Mode) {
				job.Status = deploymentv1.StateFailed
			}
			job.FailureCode = "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED"
			job.FailureMessageRedacted = "deployment lease attempts exhausted"
			job.FinishedAt = &now
			delete(s.deployLocks, job.ServiceID)
			step := EventDeploymentDeadLetter
			if isProductionDeploymentMode(job.Mode) {
				step = deploymentv1.StateFailed
			}
			event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: id, ServiceID: job.ServiceID, Level: "error", Step: step, MessageRedacted: job.FailureMessageRedacted, ProgressPercent: 100, CreatedAt: now}
			if isProductionDeploymentMode(job.Mode) {
				event.SchemaVersion = deploymentv1.EventSchemaVersion
				event.Attempt = job.AttemptCount
			}
			s.deployEvents[id] = append(s.deployEvents[id], event)
		} else {
			job.Status = DeploymentQueued
			if isProductionDeploymentMode(job.Mode) {
				job.Status = deploymentv1.StateQueued
			}
			if job.Action == "rollback" {
				job.Status = DeploymentRollingBack
			}
			message := "agent lease expired; job returned to queue"
			if isProductionDeploymentMode(job.Mode) {
				retryAfter := now.Add(deploymentRetryBackoff(job.AttemptCount))
				job.RetryAfter = &retryAfter
				message = "agent lease expired; job queued with bounded retry backoff"
			}
			event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: id, ServiceID: job.ServiceID, Level: "warn", Step: EventAgentLeaseExpired, MessageRedacted: message, ProgressPercent: 20, CreatedAt: now}
			if isProductionDeploymentMode(job.Mode) {
				event.SchemaVersion = deploymentv1.EventSchemaVersion
				event.Attempt = job.AttemptCount
			}
			s.deployEvents[id] = append(s.deployEvents[id], event)
		}
		s.deployments[id] = job
	}
}

func (s *Service) DeploymentEvents(projectID, deploymentID string) ([]DeploymentEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.deployments[deploymentID]
	if !ok || job.ProjectID != projectID {
		return nil, ErrNotFound
	}
	return append([]DeploymentEvent(nil), s.deployEvents[deploymentID]...), nil
}

func (s *Service) GetDeployment(projectID, deploymentID string) (DeploymentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.deployments[deploymentID]
	if !ok || job.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	return job, nil
}

func (s *Service) CancelDeployment(projectID, deploymentID, key, requestID string) (DeploymentJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	scope := "deploy-cancel:v1:" + projectID + ":" + key
	if existingID, exists := s.idempotency[scope].(string); exists {
		if existingID != deploymentID {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used for another deployment cancellation", RequestID: requestID}
		}
		job, ok := s.deployments[deploymentID]
		if !ok || job.ProjectID != projectID {
			return DeploymentJob{}, false, ErrNotFound
		}
		job.Reused = true
		return job, true, nil
	}
	job, ok := s.deployments[deploymentID]
	if !ok || job.ProjectID != projectID {
		return DeploymentJob{}, false, ErrNotFound
	}
	if deploymentTerminalStatus(job.Status) || job.Status == deploymentv1.StateCancelled {
		s.idempotency[scope] = deploymentID
		return job, false, nil
	}
	if job.Status != deploymentv1.StateQueued {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "CANCEL_UNSAFE", Message: "deployment has reached an Agent or runtime mutation stage", NextAction: "watch_deployment", RequestID: requestID}
	}
	now := s.clock()
	job.Status = deploymentv1.StateCancelled
	job.FinishedAt = &now
	job.UpdatedAt = now
	job.LeaseToken = ""
	job.LeaseExpiresAt = nil
	job.RetryAfter = nil
	s.deployments[deploymentID] = job
	s.idempotency[scope] = deploymentID
	delete(s.deployLocks, job.ServiceID)
	s.deployEvents[deploymentID] = append(s.deployEvents[deploymentID], DeploymentEvent{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: deploymentv1.StateCancelled, MessageRedacted: "deployment cancelled before runtime mutation", ProgressPercent: 100, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now})
	return job, false, nil
}

func (s *Service) RetryDeployment(projectID, deploymentID, key, requestID string) (DeploymentJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	job, ok := s.deployments[deploymentID]
	if !ok || job.ProjectID != projectID || !isProductionDeploymentMode(job.Mode) {
		return DeploymentJob{}, false, ErrNotFound
	}
	scope := "deploy-retry:v1:" + projectID + ":" + key
	if existingID, exists := s.idempotency[scope].(string); exists {
		if existingID != deploymentID {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used for another deployment retry", RequestID: requestID}
		}
		job.Reused = true
		return job, true, nil
	}
	if job.Status != deploymentv1.StateFailed || job.TerminalResult != nil || job.FailureCode != "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "RETRY_TERMINAL_IMMUTABLE", Message: "only a lease-exhausted job without an Agent terminal result can be retried in place", NextAction: "create_explicit_redeploy", RequestID: requestID}
	}
	now := s.clock()
	job.Status = deploymentv1.StateQueued
	job.FailureCode = ""
	job.FailureMessageRedacted = ""
	job.FinishedAt = nil
	job.LeaseToken = ""
	job.LeaseExpiresAt = nil
	job.MaxAttempts = job.AttemptCount + defaultDeploymentMaxAttempts
	job.UpdatedAt = now
	if err := s.acquireDeploymentLockLocked(job.ServiceID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, false, err
	}
	s.deployments[deploymentID] = job
	s.idempotency[scope] = deploymentID
	s.deployEvents[deploymentID] = append(s.deployEvents[deploymentID], DeploymentEvent{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: deploymentv1.StateQueued, MessageRedacted: "lease-exhausted deployment queued for another bounded attempt window", ProgressPercent: 0, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now})
	return job, false, nil
}

func validDeploymentIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char <= ' ' || char == 127 {
			return false
		}
	}
	return true
}

func deploymentRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := 5 * time.Second
	for index := 1; index < attempt && backoff < time.Minute; index++ {
		backoff *= 2
	}
	if backoff > time.Minute {
		return time.Minute
	}
	return backoff
}

func validateServiceForDeploy(service ServiceRecord, requestID string) error {
	if service.Type != "application" {
		return APIError{Status: 409, Code: "SERVICE_NOT_DEPLOYABLE", Message: "Only application services can be deployed by the rollout workflow.", RequestID: requestID}
	}
	switch service.SourceType {
	case "git":
		if service.RepoURL == "" {
			return APIError{Status: 400, Code: "SERVICE_CONFIG_INVALID", Message: "git services require repo_url before deployment.", RequestID: requestID}
		}
		if service.GitSHA == "" {
			return APIError{Status: 400, Code: "SERVICE_CONFIG_INVALID", Message: "git services require git_sha before deployment.", RequestID: requestID}
		}
		if service.BuildContext == "" {
			return APIError{Status: 400, Code: "SERVICE_CONFIG_INVALID", Message: "git services require build_context before deployment.", RequestID: requestID}
		}
		if service.Dockerfile == "" {
			return APIError{Status: 400, Code: "SERVICE_CONFIG_INVALID", Message: "git services require dockerfile before deployment.", RequestID: requestID}
		}
		if service.ManifestPath == "" {
			return APIError{Status: 400, Code: "SERVICE_CONFIG_INVALID", Message: "git services require manifest_path before deployment.", RequestID: requestID}
		}
	case "image":
		return APIError{Status: 400, Code: "IMAGE_DEPLOY_NOT_SUPPORTED", Message: "Image-source deploy is not supported by the current Agent runner. Use Git source or enable the image deploy capability.", RequestID: requestID}
	default:
		return APIError{Status: 400, Code: "SERVICE_CONFIG_INVALID", Message: "source_type must be git or image.", RequestID: requestID}
	}
	return nil
}

func (s *Service) deployAgentLocked(projectID, runtimeID, requestID string) (Node, Agent, error) {
	for _, node := range s.nodes {
		if node.ProjectID != projectID || node.RuntimeID != runtimeID || node.Role != "server" || node.Status != NodeHealthy || node.AgentID == "" {
			continue
		}
		agent := s.agents[node.AgentID]
		if agent.ProjectID == projectID && agent.Status == "active" && capabilityEnabled(agent.Capabilities, "deploy") {
			return node, agent, nil
		}
	}
	return Node{}, Agent{}, APIError{Status: 409, Code: "AGENT_NOT_READY", Message: "A healthy server with an online deploy-capable agent is required.", NextAction: "wait_for_agent", RequestID: requestID}
}

func (s *Service) lifecycleAgentLocked(projectID, runtimeID, targetNodeID, requestID string) (Node, Agent, error) {
	for _, node := range s.nodes {
		if node.ProjectID != projectID || node.RuntimeID != runtimeID || node.Status != NodeHealthy || node.AgentID == "" {
			continue
		}
		agent := s.agents[node.AgentID]
		if agent.ProjectID == projectID && agent.Status == "active" && capabilityEnabled(agent.Capabilities, "node_lifecycle") {
			return node, agent, nil
		}
	}
	return Node{}, Agent{}, APIError{Status: 409, Code: "AGENT_NOT_READY", Message: "A healthy node-lifecycle-capable Agent is required.", NextAction: "wait_for_agent", RequestID: requestID}
}

func (s *Service) expireNodeLifecycleLeasesLocked(projectID string) {
	now := s.clock()
	for id, job := range s.lifecycles {
		if job.ProjectID != projectID || job.Status != NodeLifecycleAccepted || job.LeaseExpiresAt == nil || job.LeaseExpiresAt.After(now) {
			continue
		}
		job.LeaseToken = ""
		job.LeaseExpiresAt = nil
		job.UpdatedAt = now
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		if job.AttemptCount >= job.MaxAttempts {
			job.Status = NodeLifecycleFailed
			job.FailureCode = "NODE_LIFECYCLE_LEASE_ATTEMPTS_EXHAUSTED"
			job.FailureMessageRedacted = "node lifecycle lease attempts exhausted"
			job.FinishedAt = &now
		} else {
			job.Status = NodeLifecycleRequested
		}
		s.lifecycles[id] = job
	}
}

func normalizeNodeLifecycleResult(result NodeLifecycleResult) string {
	switch result.Status {
	case NodeLifecycleCompleted:
		return NodeLifecycleCompleted
	case NodeLifecycleUnsupported:
		return NodeLifecycleUnsupported
	default:
		return NodeLifecycleFailed
	}
}

func nodeLifecycleTerminal(status string) bool {
	return status == NodeLifecycleCompleted || status == NodeLifecycleFailed || status == NodeLifecycleUnsupported
}

func capabilityEnabled(capabilities map[string]any, name string) bool {
	if capabilities == nil {
		return false
	}
	value, ok := capabilities[name]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func (s *Service) previousSuccessfulLocked(projectID, serviceID string) DeploymentJob {
	var latest DeploymentJob
	for _, job := range s.deployments {
		if job.ProjectID == projectID && job.ServiceID == serviceID && job.Status == DeploymentSucceeded && job.CreatedAt.After(latest.CreatedAt) {
			latest = job
		}
	}
	return latest
}

func deploymentJobForPlan(service ServiceRecord, previous DeploymentJob, node Node, agent Agent, key, requestedBy string, now time.Time) DeploymentJob {
	job := DeploymentJob{ID: newID("dep"), OrgID: service.OrgID, ProjectID: service.ProjectID, EnvironmentID: service.EnvironmentID, RuntimeID: service.RuntimeID, ServiceID: service.ID, Status: DeploymentQueued, Action: "deploy", IdempotencyKey: key, RequestedBy: requestedBy, AgentID: agent.ID, NodeID: node.ID, MaxAttempts: defaultDeploymentMaxAttempts, CreatedAt: now, UpdatedAt: now}
	intent := deploymentIntentForService(service, job.ID, requestedBy)
	job.ManifestHash = hashJSON(map[string]any{"service_id": service.ID, "source_type": service.SourceType, "repo_url": service.RepoURL, "image": service.Image, "branch": service.Branch, "git_sha": service.GitSHA, "build_context": service.BuildContext, "dockerfile": service.Dockerfile, "manifest_path": service.ManifestPath, "watch_paths": service.WatchPaths, "container_port": service.ContainerPort, "health_path": service.HealthPath, "replicas": service.Replicas, "resource_requests": service.ResourceRequests, "resource_limits": service.ResourceLimits, "bindings": service.Bindings, "namespace": service.Namespace})
	intent.Review.ManifestHash = job.ManifestHash
	intent.Review.IntentHash = hashJSON(intent)
	job.IntentHash = intent.Review.IntentHash
	job.DeploymentIntent = &intent
	job.PreviousRevisionRef = previous.ManifestHash
	if job.PreviousRevisionRef == "" && previous.ID != "" {
		job.PreviousRevisionRef = previous.ID
	}
	job.RollbackEligible = job.PreviousRevisionRef != ""
	if !job.RollbackEligible {
		job.RollbackBlockedReason = "no previous successful revision"
	}
	job.DeploymentPlanHash = hashJSON(map[string]any{"service_id": service.ID, "manifest_hash": job.ManifestHash, "intent_hash": job.IntentHash, "previous_revision_ref": job.PreviousRevisionRef, "target_node_id": node.ID, "agent_id": agent.ID})
	return job
}

func deploymentIntentForService(service ServiceRecord, deploymentID, requestedBy string) DeploymentIntent {
	image := DeploymentIntentImage{}
	if service.Image != "" {
		image.Repository = service.Image
		image.PullPolicy = "IfNotPresent"
	}
	replicas := service.Replicas
	if replicas == 0 {
		replicas = 1
	}
	return DeploymentIntent{
		IntentVersion: DeploymentIntentVersion,
		ProjectID:     service.ProjectID,
		ServiceID:     service.ID,
		DeploymentID:  deploymentID,
		RequestedBy:   requestedBy,
		Source: DeploymentIntentSource{
			Type:         service.SourceType,
			RepoURL:      service.RepoURL,
			Branch:       service.Branch,
			GitSHA:       service.GitSHA,
			BuildContext: service.BuildContext,
			Dockerfile:   service.Dockerfile,
			ManifestPath: service.ManifestPath,
			WatchPaths:   append([]string(nil), service.WatchPaths...),
		},
		Image:     image,
		Runtime:   DeploymentIntentRuntime{ContainerPort: service.ContainerPort, Env: map[string]any{}, Replicas: replicas},
		Health:    DeploymentIntentHealth{Path: service.HealthPath},
		Resources: deploymentIntentResources(service),
		Bindings:  deploymentIntentBindings(service.Bindings),
		Rollout:   DeploymentIntentRollout{TimeoutSeconds: 600, AutoRollback: true},
		Review:    DeploymentIntentReview{Confirmed: true},
	}
}

func deploymentIntentResources(service ServiceRecord) map[string]any {
	resources := map[string]any{}
	if len(service.ResourceRequests) > 0 {
		resources["requests"] = cloneStringMap(service.ResourceRequests)
	}
	if len(service.ResourceLimits) > 0 {
		resources["limits"] = cloneStringMap(service.ResourceLimits)
	}
	return resources
}

func deploymentIntentBindings(bindings []ServiceBinding) []DeploymentIntentBinding {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]DeploymentIntentBinding, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, DeploymentIntentBinding{
			ServiceID:       binding.ServiceID,
			Alias:           binding.Alias,
			EnvPrefix:       binding.EnvPrefix,
			ExposeAsDefault: binding.ExposeAsDefault,
			EnvKeys:         append([]string(nil), binding.EnvKeys...),
		})
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneServiceBindings(in []ServiceBinding) []ServiceBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]ServiceBinding, 0, len(in))
	for _, binding := range in {
		binding.EnvKeys = append([]string(nil), binding.EnvKeys...)
		out = append(out, binding)
	}
	return out
}

func deploymentQueuedEvents(job DeploymentJob, requestID string, now time.Time) []DeploymentEvent {
	events := []DeploymentEvent{
		{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventDeploymentQueued, MessageRedacted: "deployment queued", ProgressPercent: 0, RequestID: requestID, CreatedAt: now},
		{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventDeploymentPlanCreated, MessageRedacted: "deployment plan created", ProgressPercent: 10, RequestID: requestID, CreatedAt: now},
	}
	if job.RollbackEligible {
		events = append(events, DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventRollbackAvailable, MessageRedacted: "rollback target recorded", ProgressPercent: 10, RequestID: requestID, CreatedAt: now})
	}
	return events
}

func deploymentCompletionEvents(job DeploymentJob, requestID string, now time.Time) []DeploymentEvent {
	if isProductionDeploymentMode(job.Mode) {
		level, message := "info", "immutable image deployment succeeded"
		if job.Status != deploymentv1.StateSucceeded {
			level, message = "error", job.FailureMessageRedacted
			if message == "" {
				message = "immutable image deployment failed"
			}
		}
		return []DeploymentEvent{{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: level, Step: job.Status, MessageRedacted: message, ProgressPercent: 100, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now}}
	}
	switch job.Status {
	case DeploymentSucceeded:
		return []DeploymentEvent{
			{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventManifestApplyStarted, MessageRedacted: "manifest apply started", ProgressPercent: 55, RequestID: requestID, CreatedAt: now},
			{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventManifestApplySucceeded, MessageRedacted: "manifest applied", ProgressPercent: 70, RequestID: requestID, CreatedAt: now},
			{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventRolloutWaitStarted, MessageRedacted: "rollout watch completed", ProgressPercent: 90, RequestID: requestID, CreatedAt: now},
			{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventHealthCheckPassed, MessageRedacted: "health check passed", ProgressPercent: 95, RequestID: requestID, CreatedAt: now},
			{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventDeploymentSucceeded, MessageRedacted: "deployment succeeded", ProgressPercent: 100, RequestID: requestID, CreatedAt: now},
		}
	case DeploymentRolledBack:
		return []DeploymentEvent{{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: DeploymentRolledBack, MessageRedacted: "rollback completed", ProgressPercent: 100, RequestID: requestID, CreatedAt: now}}
	default:
		message := job.FailureMessageRedacted
		if message == "" {
			message = "deployment failed"
		}
		return []DeploymentEvent{{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "error", Step: EventDeploymentFailed, MessageRedacted: message, ProgressPercent: 100, RequestID: requestID, CreatedAt: now}}
	}
}

func normalizedDeploymentResultStatus(status string) string {
	switch status {
	case DeploymentSucceeded, "success":
		return DeploymentSucceeded
	case DeploymentRolledBack:
		return DeploymentRolledBack
	default:
		return DeploymentFailed
	}
}

func deploymentLeaseActiveStatus(status string) bool {
	return status == DeploymentWaitingAgent || status == deploymentv1.StateLeased || status == deploymentv1.StatePulling || status == deploymentv1.StateApplying || status == deploymentv1.StateWaitingReady || status == deploymentv1.RolloutStatePrepared || status == deploymentv1.RolloutStateApplying || status == deploymentv1.RolloutStateWaiting || status == deploymentv1.RolloutStateFailed || status == deploymentv1.RolloutStateRollingBack
}

func deploymentTerminalStatus(status string) bool {
	switch status {
	case DeploymentSucceeded, DeploymentFailed, DeploymentRolledBack, DeploymentDeadLetter, deploymentv1.StateCancelled, deploymentv1.RolloutStateRollbackFailed:
		return true
	default:
		return false
	}
}

func isProductionDeploymentMode(mode string) bool {
	return mode == "immutable_image" || mode == "rollout"
}

func deploymentResultHasExactDigest(imageID, digest string) bool {
	return imageID == digest || strings.HasSuffix(imageID, "@"+digest) || strings.HasSuffix(imageID, "://"+digest)
}

func (s *Service) acquireDeploymentLockLocked(serviceID, deploymentID string, now time.Time, requestID string) error {
	if lock, ok := s.deployLocks[serviceID]; ok && lock.ExpiresAt.After(now) {
		return APIError{Status: 409, Code: "DEPLOYMENT_LOCKED", Message: "Another deployment is already active for this service.", NextAction: "watch_existing_deployment", RequestID: requestID}
	}
	s.deployLocks[serviceID] = deploymentLock{DeploymentID: deploymentID, ExpiresAt: now.Add(30 * time.Minute)}
	return nil
}

func hashJSON(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Service) Audit(orgID, projectID, actorUserID, action, resourceType, resourceID, result string, metadata map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, AuditEvent{ID: newID("aud"), OrgID: orgID, ProjectID: projectID, ActorUserID: actorUserID, ActorType: "user", Action: action, ResourceType: resourceType, ResourceID: resourceID, Result: result, MetadataRedacted: RedactMap(metadata), CreatedAt: s.clock()})
}

func (s *Service) AuditWorkload(projectID, action, resourceID, result string, metadata map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok {
		return
	}
	s.audit = append(s.audit, AuditEvent{ID: newID("aud"), OrgID: project.OrgID, ProjectID: projectID, ActorType: "github_actions", Action: action, ResourceType: "build_record", ResourceID: resourceID, Result: result, MetadataRedacted: RedactMap(metadata), CreatedAt: s.clock()})
}

func (s *Service) readinessLocked(projectID string) (Readiness, error) {
	if _, ok := s.projects[projectID]; !ok {
		return Readiness{}, ErrNotFound
	}
	status := s.refreshProjectLocked(projectID)
	ready := Readiness{ProjectID: projectID, Status: status, CanDeploy: status == ProjectReady}
	if !ready.CanDeploy {
		ready.NextAction = "add_first_server"
	}
	if status == ProjectBootstrapping {
		ready.NextAction = "wait_for_bootstrap"
	}
	return ready, nil
}

func (s *Service) refreshProjectLocked(projectID string) string {
	project := s.projects[projectID]
	status := ProjectNoNodes
	for _, session := range s.bootstraps {
		if session.ProjectID == projectID && isActiveBootstrap(session.Status) {
			status = ProjectBootstrapping
			break
		}
	}
	for _, node := range s.nodes {
		if node.ProjectID == projectID && node.Role == "server" && node.Status == NodeHealthy {
			status = ProjectReady
			break
		}
	}
	if project.Status == "archived" {
		status = "archived"
	}
	project.Status = status
	project.UpdatedAt = s.clock()
	s.projects[projectID] = project
	return status
}

func (s *Service) validateBootstrapLocked(projectID, role, publicHost string) error {
	if publicHost == "" {
		return APIError{Status: 400, Code: "PUBLIC_HOST_REQUIRED", Message: "public_host is required"}
	}
	if role != "first_server" && role != "worker" {
		return APIError{Status: 400, Code: "INVALID_NODE_ROLE", Message: "role must be first_server or worker"}
	}
	if role == "first_server" && s.hasHealthyServerLocked(projectID) {
		return APIError{Status: 409, Code: "SERVER_NODE_EXISTS", Message: "this runtime already has a healthy server", NextAction: "add_worker"}
	}
	if role == "worker" && !s.hasHealthyServerLocked(projectID) {
		return APIError{Status: 409, Code: "SERVER_NODE_REQUIRED", Message: "add a healthy first server before adding workers", NextAction: "add_first_server"}
	}
	for _, session := range s.bootstraps {
		if session.ProjectID == projectID && session.PublicHost == publicHost && isActiveBootstrap(session.Status) {
			return APIError{Status: 409, Code: "ACTIVE_BOOTSTRAP_EXISTS", Message: "an active bootstrap session already targets this host", NextAction: "watch_existing_session"}
		}
	}
	return nil
}

func (s *Service) hasHealthyServerLocked(projectID string) bool {
	return s.healthyServerCountLocked(projectID) > 0
}

func (s *Service) healthyServerCountLocked(projectID string) int {
	count := 0
	for _, node := range s.nodes {
		if node.ProjectID == projectID && node.Role == "server" && node.Status == NodeHealthy {
			count++
		}
	}
	return count
}

func (s *Service) expireBootstrapsLocked() {
	now := s.clock()
	s.recoverExpiredBootstrapLeasesLocked(now)
	for id, session := range s.bootstraps {
		if !isActiveBootstrap(session.Status) || isLeasedBootstrapStatus(session.Status) || now.Before(session.ExpiresAt) {
			continue
		}
		session.Status = "expired"
		session.UpdatedAt = now
		session.FinishedAt = &now
		clearBootstrapLease(&session)
		s.bootstraps[id] = session
		s.events[id] = append(s.events[id], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: session.ProjectID, SessionID: id, NodeID: session.NodeID, Level: "warn", Step: "expired", MessageRedacted: "bootstrap session expired", ProgressPercent: 100, CreatedAt: now})
		s.refreshProjectLocked(session.ProjectID)
	}
}

func isActiveBootstrap(status string) bool {
	switch status {
	case "created", BootstrapPending, BootstrapRetryWait, "preflight", "validating", "connecting", "installing", "installing_k3s", "installing_agent", "registering_agent", "waiting_agent", "verifying_agent", "verifying":
		return true
	default:
		return false
	}
}

func validBootstrapStatus(status string) bool {
	return isActiveBootstrap(status) || isTerminalBootstrap(status) || status == "failed"
}

func isLeasedBootstrapStatus(status string) bool {
	switch status {
	case "preflight", "validating", "connecting", "installing", "installing_k3s", "installing_agent", "registering_agent", "waiting_agent", "verifying_agent", "verifying":
		return true
	default:
		return false
	}
}

func isTerminalBootstrap(status string) bool {
	switch status {
	case "completed", "succeeded", "cancelled", "expired", BootstrapDeadLetter:
		return true
	default:
		return false
	}
}

var bootstrapWorkerIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func ValidateBootstrapWorkerID(workerID string) error {
	if workerID == "" {
		return APIError{Status: 400, Code: "BOOTSTRAP_WORKER_ID_REQUIRED", Message: "worker_id is required"}
	}
	if len(workerID) > 128 || !bootstrapWorkerIDPattern.MatchString(workerID) {
		return APIError{Status: 400, Code: "BOOTSTRAP_WORKER_ID_INVALID", Message: "worker_id must be at most 128 characters and contain only letters, numbers, dot, underscore, or hyphen"}
	}
	return nil
}

func newBootstrapLeaseToken() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("generate bootstrap lease token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

func validateBootstrapLease(session BootstrapSession, workerID, leaseToken string, now time.Time) error {
	if !isLeasedBootstrapStatus(session.Status) || session.LeaseTokenHash == "" || session.LeaseExpiresAt == nil {
		return APIError{Status: 409, Code: "BOOTSTRAP_LEASE_INACTIVE", Message: "bootstrap session no longer has an active lease"}
	}
	if !now.UTC().Before(session.ExpiresAt) {
		return APIError{Status: 410, Code: "BOOTSTRAP_SESSION_EXPIRED", Message: "bootstrap session has expired"}
	}
	if session.LeaseOwner != workerID {
		return APIError{Status: 403, Code: "BOOTSTRAP_LEASE_OWNER_MISMATCH", Message: "bootstrap lease owner does not match worker"}
	}
	if leaseToken == "" {
		return APIError{Status: 403, Code: "BOOTSTRAP_LEASE_INVALID", Message: "bootstrap lease token is invalid"}
	}
	sum := sha256.Sum256([]byte(leaseToken))
	if subtle.ConstantTimeCompare([]byte(session.LeaseTokenHash), []byte(hex.EncodeToString(sum[:]))) != 1 {
		return APIError{Status: 403, Code: "BOOTSTRAP_LEASE_INVALID", Message: "bootstrap lease token is invalid"}
	}
	if !now.UTC().Before(*session.LeaseExpiresAt) {
		return APIError{Status: 410, Code: "BOOTSTRAP_LEASE_EXPIRED", Message: "bootstrap lease has expired"}
	}
	return nil
}

func bootstrapProgress(status string) int {
	switch status {
	case BootstrapPending, BootstrapRetryWait, "created":
		return 0
	case "preflight", "validating":
		return 10
	case "connecting":
		return 20
	case "installing", "installing_k3s":
		return 45
	case "installing_agent":
		return 65
	case "registering_agent", "waiting_agent":
		return 80
	case "verifying_agent", "verifying":
		return 90
	case "completed", "succeeded", "failed", "cancelled", "expired", BootstrapDeadLetter:
		return 100
	default:
		return 0
	}
}

func (s *Service) defaultScopeLocked(projectID string) (Project, Runtime, Environment, bool) {
	project, ok := s.projects[projectID]
	if !ok {
		return Project{}, Runtime{}, Environment{}, false
	}
	for _, runtime := range s.runtimes {
		if runtime.ProjectID == projectID {
			return project, runtime, s.envs[runtime.EnvironmentID], true
		}
	}
	return Project{}, Runtime{}, Environment{}, false
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func roleForNode(role string) string {
	if role == "first_server" {
		return "server"
	}
	return role
}

func k3sRoleForBootstrap(role string) string {
	if role == "first_server" {
		return "server"
	}
	return "agent"
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

var redactors = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN (?:OPENSSH|RSA) PRIVATE KEY-----.*?-----END (?:OPENSSH|RSA) PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)(private[_ -]?key['"]?\s*[:=]\s*)[^\s,}]+`),
	regexp.MustCompile(`(?i)(kubeconfig['"]?\s*[:=]\s*)[^\s,}]+`),
	regexp.MustCompile(`(?i)(pat['"]?\s*[:=]\s*)[^\s,}]+`),
	regexp.MustCompile(`(?i)(app[_ -]?secret['"]?\s*[:=]\s*)[^\s,}]+`),
	regexp.MustCompile(`(?i)(password=)[^\s&]+`),
	regexp.MustCompile(`(?i)(password['"]?\s*[:=]\s*)[^\s,}]+`),
	regexp.MustCompile(`(?i)(token=)[^\s&]+`),
	regexp.MustCompile(`(?i)(token['"]?\s*[:=]\s*)[^\s,}]+`),
	regexp.MustCompile(`(?i)(K3S_TOKEN=)[^\s&]+`),
	regexp.MustCompile(`(?i)(Authorization:\s*)\S+`),
	regexp.MustCompile(`(?i)(DATABASE_URL=)[^\s]+`),
	regexp.MustCompile(`(?i)(REDIS_URL=)[^\s]+`),
}

func RedactString(value string) string {
	for _, re := range redactors {
		value = re.ReplaceAllString(value, "${1}[REDACTED]")
	}
	return value
}

func RedactMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return RedactString(x)
	case map[string]any:
		return RedactMap(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = redactValue(x[i])
		}
		return out
	default:
		return v
	}
}
