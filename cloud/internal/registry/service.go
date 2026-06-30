package registry

import (
	"crypto/rand"
	"encoding/hex"
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

	DeploymentQueued = "queued"
)

var ErrNotFound = errors.New("not found")

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
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
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
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID string    `json:"environment_id"`
	RuntimeID     string    `json:"runtime_id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	SourceType    string    `json:"source_type"`
	RepoURL       string    `json:"repo_url,omitempty"`
	Image         string    `json:"image,omitempty"`
	Namespace     string    `json:"namespace"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type DeploymentJob struct {
	ID             string     `json:"id"`
	OrgID          string     `json:"org_id"`
	ProjectID      string     `json:"project_id"`
	EnvironmentID  string     `json:"environment_id"`
	RuntimeID      string     `json:"runtime_id"`
	ServiceID      string     `json:"service_id"`
	Status         string     `json:"status"`
	IdempotencyKey string     `json:"idempotency_key"`
	RequestedBy    string     `json:"requested_by,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
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
	deployEvents map[string][]DeploymentEvent
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
	CreateBootstrapSession(projectID, role, publicHost, username, authMethod, createdBy, key string, sshPort int) (BootstrapSession, error)
	UpdateBootstrapSession(projectID, sessionID, status, message string) (BootstrapSession, error)
	GetBootstrapSession(projectID, sessionID string) (BootstrapSession, error)
	ListBootstrapSessions(projectID string) ([]BootstrapSession, error)
	BootstrapEvents(projectID, sessionID string) ([]BootstrapEvent, error)
	CreateService(projectID, name, serviceType, sourceType, repoURL, image, key string) (ServiceRecord, error)
	ListServices(projectID string) ([]ServiceRecord, error)
	StartDeployment(projectID, serviceID, requestedBy, key, requestID string) (DeploymentJob, error)
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
		deployEvents: map[string][]DeploymentEvent{},
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
			session.Status = "succeeded"
			session.FinishedAt = &now
			session.UpdatedAt = now
			s.bootstraps[id] = session
			s.events[id] = append(s.events[id], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: projectID, SessionID: id, NodeID: nodeID, Level: "info", Step: "succeeded", MessageRedacted: "agent heartbeat marked node healthy", ProgressPercent: 100, CreatedAt: now})
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
	if node.Status == NodeRemoved {
		return Node{}, APIError{Status: 409, Code: "NODE_REMOVED", Message: "removed nodes cannot be drained"}
	}
	now := s.clock()
	node.Status = NodeDraining
	node.UpdatedAt = now
	s.nodes[nodeID] = node
	s.refreshProjectLocked(projectID)
	return node, nil
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
	now := s.clock()
	node.Status = NodeRemoved
	node.UpdatedAt = now
	if node.AgentID != "" {
		agent := s.agents[node.AgentID]
		agent.Status = "revoked"
		agent.UpdatedAt = now
		s.agents[agent.ID] = agent
	}
	s.nodes[nodeID] = node
	s.refreshProjectLocked(projectID)
	return node, nil
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
	session := BootstrapSession{ID: newID("boot"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, NodeID: node.ID, CreatedBy: createdBy, Role: role, Status: "created", IdempotencyKey: key, PublicHost: publicHost, SSHPort: sshPort, SSHUsername: username, AuthMethod: authMethod, ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now}
	event := BootstrapEvent{ID: newID("evt"), OrgID: project.OrgID, ProjectID: project.ID, SessionID: session.ID, NodeID: node.ID, Level: "info", Step: "created", MessageRedacted: "bootstrap session created", ProgressPercent: 0, CreatedAt: now}
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
	if status == "preflight" && session.StartedAt == nil {
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

func (s *Service) CreateService(projectID, name, serviceType, sourceType, repoURL, image, key string) (ServiceRecord, error) {
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
	if serviceType == "" {
		serviceType = "application"
	}
	if sourceType == "" {
		sourceType = "git"
	}
	record := ServiceRecord{ID: newID("svc"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: name, Type: serviceType, Status: "draft", SourceType: sourceType, RepoURL: repoURL, Image: image, Namespace: "default", CreatedAt: now, UpdatedAt: now}
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
	now := s.clock()
	job := DeploymentJob{ID: newID("dep"), OrgID: service.OrgID, ProjectID: projectID, EnvironmentID: service.EnvironmentID, RuntimeID: service.RuntimeID, ServiceID: serviceID, Status: DeploymentQueued, IdempotencyKey: key, RequestedBy: requestedBy, CreatedAt: now, UpdatedAt: now}
	s.deployments[job.ID] = job
	s.deployEvents[job.ID] = []DeploymentEvent{{ID: newID("depevt"), OrgID: service.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: serviceID, Level: "info", Step: DeploymentQueued, MessageRedacted: "deployment queued", ProgressPercent: 0, RequestID: requestID, CreatedAt: now}}
	s.idempotency["deploy:"+projectID+":"+serviceID+":"+key] = job
	return job, nil
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
		if session.ProjectID == projectID && session.Status != "failed" && session.Status != "cancelled" && session.Status != "expired" && session.Status != "succeeded" {
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
		s.bootstraps[id] = session
		s.events[id] = append(s.events[id], BootstrapEvent{ID: newID("evt"), OrgID: session.OrgID, ProjectID: session.ProjectID, SessionID: id, NodeID: session.NodeID, Level: "warn", Step: "expired", MessageRedacted: "bootstrap session expired", ProgressPercent: 100, CreatedAt: now})
		s.refreshProjectLocked(session.ProjectID)
	}
}

func isActiveBootstrap(status string) bool {
	return status == "created" || status == "preflight" || status == "installing" || status == "waiting_agent"
}

func validBootstrapStatus(status string) bool {
	return status == "preflight" || status == "installing" || status == "waiting_agent" || status == "succeeded" || status == "failed" || status == "cancelled"
}

func bootstrapProgress(status string) int {
	switch status {
	case "preflight":
		return 10
	case "installing":
		return 45
	case "waiting_agent":
		return 80
	case "succeeded", "failed", "cancelled":
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
	regexp.MustCompile(`(?i)(password=)[^\s&]+`),
	regexp.MustCompile(`(?i)(token=)[^\s&]+`),
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
