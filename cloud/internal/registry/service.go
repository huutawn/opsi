package registry

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

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
	ID             string     `json:"id"`
	OrgID          string     `json:"org_id"`
	ProjectID      string     `json:"project_id"`
	EnvironmentID  string     `json:"environment_id"`
	RuntimeID      string     `json:"runtime_id"`
	NodeID         string     `json:"node_id,omitempty"`
	CreatedBy      string     `json:"created_by,omitempty"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	IdempotencyKey string     `json:"idempotency_key"`
	PublicHost     string     `json:"public_host,omitempty"`
	SSHPort        int        `json:"ssh_port,omitempty"`
	SSHUsername    string     `json:"ssh_username,omitempty"`
	AuthMethod     string     `json:"auth_method,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	LeaseOwner     string     `json:"lease_owner,omitempty"`
	LeaseTokenHash string     `json:"-"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	LeasedAt       *time.Time `json:"leased_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type BootstrapSessionLease struct {
	Session        BootstrapSession `json:"session"`
	LeaseToken     string           `json:"lease_token"`
	LeaseExpiresAt time.Time        `json:"lease_expires_at"`
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
	ID                     string            `json:"id"`
	OrgID                  string            `json:"org_id"`
	ProjectID              string            `json:"project_id"`
	EnvironmentID          string            `json:"environment_id"`
	RuntimeID              string            `json:"runtime_id"`
	ServiceID              string            `json:"service_id"`
	Status                 string            `json:"status"`
	Action                 string            `json:"action,omitempty"`
	IdempotencyKey         string            `json:"idempotency_key"`
	DeploymentPlanHash     string            `json:"deployment_plan_hash,omitempty"`
	ManifestHash           string            `json:"manifest_hash,omitempty"`
	IntentHash             string            `json:"intent_hash,omitempty"`
	DeploymentIntent       *DeploymentIntent `json:"deployment_intent,omitempty"`
	PreviousRevisionRef    string            `json:"previous_revision_ref,omitempty"`
	RollbackEligible       bool              `json:"rollback_eligible"`
	RollbackBlockedReason  string            `json:"rollback_blocked_reason,omitempty"`
	RequestedBy            string            `json:"requested_by,omitempty"`
	AgentID                string            `json:"agent_id,omitempty"`
	NodeID                 string            `json:"node_id,omitempty"`
	FailureCode            string            `json:"failure_code,omitempty"`
	FailureMessageRedacted string            `json:"failure_message_redacted,omitempty"`
	LeaseToken             string            `json:"-"`
	LeaseExpiresAt         *time.Time        `json:"lease_expires_at,omitempty"`
	AttemptCount           int               `json:"attempt_count,omitempty"`
	MaxAttempts            int               `json:"max_attempts,omitempty"`
	StartedAt              *time.Time        `json:"started_at,omitempty"`
	FinishedAt             *time.Time        `json:"finished_at,omitempty"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

type deploymentLock struct {
	DeploymentID string
	ExpiresAt    time.Time
}

type DeploymentEvent struct {
	ID              string    `json:"id"`
	OrgID           string    `json:"org_id"`
	ProjectID       string    `json:"project_id"`
	DeploymentID    string    `json:"deployment_id"`
	ServiceID       string    `json:"service_id"`
	Level           string    `json:"level"`
	Step            string    `json:"step"`
	MessageRedacted string    `json:"message_redacted"`
	ProgressPercent int       `json:"progress_percent"`
	RequestID       string    `json:"request_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type DeploymentLease struct {
	Deployment DeploymentJob `json:"deployment"`
	Service    ServiceRecord `json:"service"`
	Action     string        `json:"action"`
	LeaseToken string        `json:"lease_token,omitempty"`
}

type DeploymentResult struct {
	Status                 string `json:"status"`
	LeaseToken             string `json:"lease_token,omitempty"`
	FinalRevisionRef       string `json:"final_revision_ref,omitempty"`
	IntentHash             string `json:"intent_hash,omitempty"`
	FailureCode            string `json:"failure_code,omitempty"`
	FailureMessageRedacted string `json:"failure_message_redacted,omitempty"`
	RollbackEligible       bool   `json:"rollback_eligible"`
	RollbackBlockedReason  string `json:"rollback_blocked_reason,omitempty"`
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
	mu           sync.Mutex
	projects     map[string]Project
	envs         map[string]Environment
	runtimes     map[string]Runtime
	nodes        map[string]Node
	agents       map[string]Agent
	bootstraps   map[string]BootstrapSession
	events       map[string][]BootstrapEvent
	services     map[string]ServiceRecord
	deployments  map[string]DeploymentJob
	lifecycles   map[string]NodeLifecycleJob
	deployEvents map[string][]DeploymentEvent
	deployLocks  map[string]deploymentLock
	audit        []AuditEvent
	idempotency  map[string]any
	now          func() time.Time
}

type API interface {
	CreateProject(orgID, name, slug, createdBy, key string) (Project, error)
	ListProjects(orgID string) ([]Project, error)
	ProjectReadiness(projectID string) (Readiness, error)
	ListNodes(projectID string) ([]Node, error)
	NodeDiagnostics(projectID, nodeID string) (NodeDiagnostics, error)
	UpsertNode(projectID, name, role, status, publicHost, agentID, key string) (Node, error)
	RegisterAgent(projectID, nodeID, fingerprint, credentialHash, version, key string, capabilities map[string]any) (Agent, error)
	RecordAgentHeartbeat(projectID, nodeID string, heartbeat AgentHeartbeat) (Node, error)
	VerifyAgent(projectID, nodeID, token string) (Agent, error)
	RotateAgent(projectID, agentID, credentialHash string) (Agent, error)
	RevokeAgent(projectID, agentID string) (Agent, error)
	DrainNode(projectID, nodeID string) (Node, error)
	RemoveNode(projectID, nodeID string, force bool) (Node, error)
	RequestNodeLifecycle(projectID, targetNodeID, action, requestedBy, key, requestID string, confirmRemove, force bool) (NodeLifecycleJob, error)
	LeaseNodeLifecycle(projectID, nodeID string) (NodeLifecycleLease, bool, error)
	CompleteNodeLifecycle(projectID, nodeID, jobID, requestID string, result NodeLifecycleResult) (NodeLifecycleJob, error)
	CreateBootstrapSession(projectID, role, publicHost, username, authMethod, createdBy, key string, sshPort int) (BootstrapSession, error)
	UpdateBootstrapSession(projectID, sessionID, status, message string) (BootstrapSession, error)
	LeaseNextBootstrapSession(workerID string, now time.Time, leaseDuration time.Duration) (BootstrapSessionLease, bool, error)
	GetBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken string, now time.Time) (BootstrapSession, error)
	UpdateBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken, status, message string, now time.Time) (BootstrapSession, error)
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
	ListAudit(projectID string) ([]AuditEvent, error)
	Audit(orgID, projectID, actorUserID, action, resourceType, resourceID, result string, metadata map[string]any)
}

func NewService() *Service {
	return &Service{
		projects:     map[string]Project{},
		envs:         map[string]Environment{},
		runtimes:     map[string]Runtime{},
		nodes:        map[string]Node{},
		agents:       map[string]Agent{},
		bootstraps:   map[string]BootstrapSession{},
		events:       map[string][]BootstrapEvent{},
		services:     map[string]ServiceRecord{},
		deployments:  map[string]DeploymentJob{},
		lifecycles:   map[string]NodeLifecycleJob{},
		deployEvents: map[string][]DeploymentEvent{},
		deployLocks:  map[string]deploymentLock{},
		audit:        []AuditEvent{},
		idempotency:  map[string]any{},
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

func (s *Service) RegisterAgent(projectID, nodeID, fingerprint, credentialHash, version, key string, capabilities map[string]any) (Agent, error) {
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
	now := s.clock()
	agent := Agent{ID: newID("agent"), OrgID: node.OrgID, ProjectID: projectID, RuntimeID: node.RuntimeID, NodeID: node.ID, PublicKeyFingerprint: fingerprint, CredentialHash: credentialHash, Version: version, Capabilities: capabilities, Status: "active", LastSeenAt: &now, CreatedAt: now, UpdatedAt: now}
	node.AgentID = agent.ID
	node.AgentVersion = version
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
	session := BootstrapSession{ID: newID("boot"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, NodeID: node.ID, CreatedBy: createdBy, Role: role, Status: "pending", IdempotencyKey: key, PublicHost: publicHost, SSHPort: sshPort, SSHUsername: username, AuthMethod: authMethod, ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now}
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
	if !validBootstrapStatus(status) {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap status is invalid"}
	}
	now := s.clock()
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
	var selected BootstrapSession
	for _, session := range s.bootstraps {
		if (session.Status != "created" && session.Status != "pending") || session.LeaseTokenHash != "" {
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
	if !validBootstrapStatus(status) {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap status is invalid"}
	}
	now = now.UTC()
	session.Status = status
	session.UpdatedAt = now
	if !isActiveBootstrap(status) {
		session.FinishedAt = &now
		session.LeaseTokenHash = ""
		session.LeaseExpiresAt = nil
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

func (s *Service) RollbackDeployment(projectID, deploymentID, requestedBy, key, requestID string) (DeploymentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.idempotency["rollback:"+projectID+":"+deploymentID+":"+key].(DeploymentJob); ok {
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
		job.Status = DeploymentWaitingAgent
		job.Action = action
		job.AttemptCount++
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		job.LeaseToken = newID("lease")
		job.LeaseExpiresAt = &leaseExpiresAt
		job.UpdatedAt = now
		s.deployments[id] = job
		s.deployEvents[id] = append(s.deployEvents[id], DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: id, ServiceID: job.ServiceID, Level: "info", Step: EventAgentJobAccepted, MessageRedacted: "agent accepted deployment job", ProgressPercent: 20, CreatedAt: job.UpdatedAt})
		return DeploymentLease{Deployment: job, Service: service, Action: action, LeaseToken: job.LeaseToken}, true, nil
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
	if deploymentTerminalStatus(job.Status) {
		return job, nil
	}
	now := s.clock()
	if job.Status != DeploymentWaitingAgent || job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_LEASE_EXPIRED", Message: "deployment lease is not active", NextAction: "poll_for_new_lease", RequestID: requestID}
	}
	if job.LeaseToken != "" && result.LeaseToken != job.LeaseToken {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_STALE_LEASE", Message: "deployment result lease token is stale", NextAction: "discard_result_and_poll", RequestID: requestID}
	}
	if job.IntentHash != "" && result.IntentHash != "" && result.IntentHash != job.IntentHash {
		return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_RESULT_MISMATCH", Message: "deployment result intent hash does not match leased job", RequestID: requestID}
	}
	status := normalizedDeploymentResultStatus(result.Status)
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
	s.deployments[deploymentID] = job
	s.deployEvents[deploymentID] = append(s.deployEvents[deploymentID], deploymentCompletionEvents(job, requestID, now)...)
	if deploymentTerminalStatus(status) {
		delete(s.deployLocks, job.ServiceID)
	}
	return job, nil
}

func (s *Service) expireDeploymentLeasesLocked(projectID string) {
	now := s.clock()
	for id, job := range s.deployments {
		if job.ProjectID != projectID || job.Status != DeploymentWaitingAgent || job.LeaseExpiresAt == nil || job.LeaseExpiresAt.After(now) {
			continue
		}
		job.LeaseToken = ""
		job.LeaseExpiresAt = nil
		job.UpdatedAt = now
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		if job.AttemptCount >= job.MaxAttempts {
			job.Status = DeploymentDeadLetter
			job.FailureCode = "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED"
			job.FailureMessageRedacted = "deployment lease attempts exhausted"
			job.FinishedAt = &now
			delete(s.deployLocks, job.ServiceID)
			s.deployEvents[id] = append(s.deployEvents[id], DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: id, ServiceID: job.ServiceID, Level: "error", Step: EventDeploymentDeadLetter, MessageRedacted: job.FailureMessageRedacted, ProgressPercent: 100, CreatedAt: now})
		} else {
			job.Status = DeploymentQueued
			if job.Action == "rollback" {
				job.Status = DeploymentRollingBack
			}
			s.deployEvents[id] = append(s.deployEvents[id], DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: id, ServiceID: job.ServiceID, Level: "warn", Step: EventAgentLeaseExpired, MessageRedacted: "agent lease expired; job returned to queue", ProgressPercent: 20, CreatedAt: now})
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

func deploymentTerminalStatus(status string) bool {
	switch status {
	case DeploymentSucceeded, DeploymentFailed, DeploymentRolledBack, DeploymentDeadLetter:
		return true
	default:
		return false
	}
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
	for id, session := range s.bootstraps {
		if !isActiveBootstrap(session.Status) || !now.After(session.ExpiresAt) {
			continue
		}
		session.Status = "expired"
		session.UpdatedAt = now
		session.FinishedAt = &now
		session.LeaseTokenHash = ""
		session.LeaseExpiresAt = nil
		s.bootstraps[id] = session
		s.events[id] = append(s.events[id], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: session.ProjectID, SessionID: id, NodeID: session.NodeID, Level: "warn", Step: "expired", MessageRedacted: "bootstrap session expired", ProgressPercent: 100, CreatedAt: now})
		s.refreshProjectLocked(session.ProjectID)
	}
}

func isActiveBootstrap(status string) bool {
	switch status {
	case "created", "pending", "preflight", "validating", "connecting", "installing", "installing_k3s", "installing_agent", "registering_agent", "waiting_agent", "verifying_agent", "verifying":
		return true
	default:
		return false
	}
}

func validBootstrapStatus(status string) bool {
	return isActiveBootstrap(status) || status == "completed" || status == "succeeded" || status == "failed" || status == "cancelled" || status == "expired"
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
	if session.LeaseOwner != workerID {
		return APIError{Status: 403, Code: "BOOTSTRAP_LEASE_OWNER_MISMATCH", Message: "bootstrap lease owner does not match worker"}
	}
	if session.LeaseTokenHash == "" || leaseToken == "" {
		return APIError{Status: 403, Code: "BOOTSTRAP_LEASE_INVALID", Message: "bootstrap lease token is invalid"}
	}
	sum := sha256.Sum256([]byte(leaseToken))
	if subtle.ConstantTimeCompare([]byte(session.LeaseTokenHash), []byte(hex.EncodeToString(sum[:]))) != 1 {
		return APIError{Status: 403, Code: "BOOTSTRAP_LEASE_INVALID", Message: "bootstrap lease token is invalid"}
	}
	if session.LeaseExpiresAt == nil || !now.UTC().Before(*session.LeaseExpiresAt) {
		return APIError{Status: 409, Code: "BOOTSTRAP_LEASE_EXPIRED", Message: "bootstrap lease has expired"}
	}
	return nil
}

func bootstrapProgress(status string) int {
	switch status {
	case "pending", "created":
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
	case "completed", "succeeded", "failed", "cancelled", "expired":
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
