package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	"golang.org/x/crypto/bcrypt"
)

type PostgresService struct {
	DB  *sql.DB
	Now func() time.Time
}

const nodeSelectSQL = `SELECT id, org_id, project_id, environment_id, runtime_id, name, role, status, COALESCE(public_host,''), COALESCE(private_ip,''), COALESCE(provider,''), COALESCE(region,''), COALESCE(os_name,''), COALESCE(os_version,''), COALESCE(arch,''), COALESCE(cpu_cores,0), COALESCE(memory_mb,0), COALESCE(disk_total_gb,0), COALESCE(k3s_role,''), COALESCE(k3s_status,''), COALESCE(k3s_version,''), COALESCE(agent_id,''), COALESCE(agent_version,''), COALESCE(agent_endpoint,''), COALESCE(agent_port,0), COALESCE(agent_tls_server_name,''), COALESCE(agent_cert_sha256,''), last_seen_at, last_inventory_at, COALESCE(failure_code,''), COALESCE(failure_message_redacted,''), created_at, updated_at FROM nodes`

const serviceSelectSQL = `SELECT id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, COALESCE(repo_url,''), COALESCE(image,''), COALESCE(branch,''), COALESCE(git_sha,''), COALESCE(build_method,''), COALESCE(build_context,''), COALESCE(dockerfile,''), COALESCE(manifest_path,''), watch_paths::text, COALESCE(container_port,0), COALESCE(health_path,''), COALESCE(replicas_desired,0), COALESCE(resources->'requests','{}'::jsonb)::text, COALESCE(resources->'limits','{}'::jsonb)::text, COALESCE(bindings,'[]'::jsonb)::text, namespace, created_at, updated_at FROM control_services`

const deploymentSelectSQL = `SELECT id, org_id, project_id, environment_id, runtime_id, service_id, status, COALESCE(action,'deploy'), idempotency_key, COALESCE(deployment_plan_hash,''), COALESCE(manifest_hash,''), COALESCE(intent_hash,''), deployment_intent_json::text, COALESCE(previous_revision_ref,''), COALESCE(rollback_eligible,false), COALESCE(rollback_blocked_reason,''), COALESCE(requested_by,''), COALESCE(agent_id,''), COALESCE(node_id,''), COALESCE(failure_code,''), COALESCE(failure_message_redacted,''), COALESCE(lease_token,''), lease_expires_at, retry_after, COALESCE(attempt_count,0), COALESCE(max_attempts,3), started_at, finished_at, created_at, updated_at, COALESCE(schema_version,''), COALESCE(mode,''), COALESCE(snapshot_json,'{}')::text, COALESCE(spec_hash,''), COALESCE(payload_hash,''), COALESCE(terminal_result_json,'{}')::text, COALESCE(base_deployment_id,''), COALESCE(rollout_intent_json,'{}')::text, COALESCE(rollout_state,''), COALESCE(rollout_state_hash,''), COALESCE(rollout_version,0), COALESCE(desired_digest,''), COALESCE(current_digest,''), COALESCE(previous_digest,''), COALESCE(exposure_spec_json,'{}')::text, COALESCE(known_good_id,''), COALESCE(known_good_hash,''), COALESCE(readiness_evidence_hash,'') FROM deployment_jobs`

const nodeLifecycleSelectSQL = `SELECT id, org_id, project_id, runtime_id, action, status, target_node_id, target_node_name, node_id, COALESCE(agent_id,''), COALESCE(requested_by,''), COALESCE(idempotency_key,''), confirm_remove, COALESCE(lease_token,''), lease_expires_at, COALESCE(attempt_count,0), COALESCE(max_attempts,3), COALESCE(failure_code,''), COALESCE(failure_message_redacted,''), verified, finished_at, created_at, updated_at FROM node_lifecycle_jobs`

const bootstrapSelectSQL = `SELECT id, org_id, project_id, environment_id, runtime_id, COALESCE(node_id,''), COALESCE(created_by,''), role, status, idempotency_key, COALESCE(public_host,''), COALESCE(ssh_port,0), COALESCE(ssh_username,''), COALESCE(auth_method,''), expires_at, started_at, finished_at, COALESCE(lease_owner,''), COALESCE(lease_token_hash,''), lease_expires_at, leased_at, COALESCE(attempt_count,0), COALESCE(max_attempts,3), next_attempt_at, lease_heartbeat_at, COALESCE(last_failure_code,''), COALESCE(last_failure_message_redacted,''), dead_lettered_at, checkpoint_schema_version, checkpoint_plan_version, checkpoint_plan_fingerprint, checkpoint_next_step_index, checkpoint_last_completed_step, checkpoint_updated_at, created_at, updated_at FROM bootstrap_sessions`

func (s PostgresService) CreateProject(orgID, name, slug, createdBy, key string) (Project, error) {
	ctx := context.Background()
	scope := "project:" + orgID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return Project{}, err
		}
		return s.getProject(ctx, id)
	}
	now := s.clock()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	project, err := CreateProjectInTx(ctx, tx, orgID, name, slug, createdBy, now)
	if err != nil {
		return Project{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "project", project.ID); err != nil {
		return Project{}, err
	}
	return project, tx.Commit()
}

// CreateProjectInTx is the canonical Postgres project creation path, including
// the default environment and runtime required by downstream bootstrap flows.
func CreateProjectInTx(ctx context.Context, tx *sql.Tx, orgID, name, slug, createdBy string, now time.Time) (Project, error) {
	project := Project{ID: newID("proj"), OrgID: orgID, Name: name, Slug: slug, Status: ProjectNoNodes, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now}
	if project.Name == "" {
		project.Name = project.ID
	}
	if project.Slug == "" {
		project.Slug = project.ID
	}
	env := Environment{ID: newID("env"), OrgID: orgID, ProjectID: project.ID, Name: "default", Type: "dev", Status: "active", CreatedAt: now, UpdatedAt: now}
	runtime := Runtime{ID: newID("rt"), OrgID: orgID, ProjectID: project.ID, EnvironmentID: env.ID, Name: "default", Type: "k3s", Status: RuntimeNoNodes, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, org_id, name, slug, status, created_by, created_at, updated_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8)`, project.ID, project.OrgID, project.Name, project.Slug, project.Status, project.CreatedBy, project.CreatedAt, project.UpdatedAt); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO environments(id, org_id, project_id, name, type, status, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, env.ID, env.OrgID, env.ProjectID, env.Name, env.Type, env.Status, env.CreatedAt, env.UpdatedAt); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runtimes(id, org_id, project_id, environment_id, name, type, status, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, runtime.ID, runtime.OrgID, runtime.ProjectID, runtime.EnvironmentID, runtime.Name, runtime.Type, runtime.Status, runtime.CreatedAt, runtime.UpdatedAt); err != nil {
		return Project{}, err
	}
	return project, nil
}

func (s PostgresService) ListProjects(orgID string) ([]Project, error) {
	rows, err := s.DB.QueryContext(context.Background(), `SELECT id, org_id, name, COALESCE(slug,''), status, COALESCE(created_by,''), created_at, updated_at FROM projects WHERE org_id = $1 ORDER BY created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Status, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s PostgresService) ProjectReadiness(projectID string) (Readiness, error) {
	ctx := context.Background()
	if err := s.expireBootstraps(ctx); err != nil {
		return Readiness{}, err
	}
	status, err := s.refreshProject(ctx, projectID)
	if err != nil {
		return Readiness{}, err
	}
	ready := Readiness{ProjectID: projectID, Status: status, CanDeploy: status == ProjectReady}
	if !ready.CanDeploy {
		ready.NextAction = "add_first_server"
	}
	if status == ProjectBootstrapping {
		ready.NextAction = "wait_for_bootstrap"
	}
	return ready, nil
}

func (s PostgresService) ListNodes(projectID string) ([]Node, error) {
	if _, _, _, err := s.defaultScope(context.Background(), projectID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(context.Background(), nodeSelectSQL+` WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

func (s PostgresService) ListServices(projectID string) ([]ServiceRecord, error) {
	if _, _, _, err := s.defaultScope(context.Background(), projectID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(context.Background(), serviceSelectSQL+` WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceRecord
	for rows.Next() {
		r, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s PostgresService) ListDeployments(projectID string) ([]DeploymentJob, error) {
	if _, _, _, err := s.defaultScope(context.Background(), projectID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(context.Background(), deploymentSelectSQL+` WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentJob
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s PostgresService) DeploymentEvents(projectID, deploymentID string) ([]DeploymentEvent, error) {
	ctx := context.Background()
	job, err := s.getDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if job.ProjectID != projectID {
		return nil, ErrNotFound
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT COALESCE(schema_version,''), id, org_id, project_id, deployment_id, service_id, level, step, message_redacted, progress_percent, COALESCE(attempt,0), COALESCE(request_id,''), created_at, COALESCE(rollout_id,''), COALESCE(intent_hash,''), COALESCE(state_hash,''), COALESCE(readiness_evidence_hash,'') FROM deployment_events WHERE project_id = $1 AND deployment_id = $2 ORDER BY created_at`, projectID, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentEvent
	for rows.Next() {
		var e DeploymentEvent
		if err := rows.Scan(&e.SchemaVersion, &e.ID, &e.OrgID, &e.ProjectID, &e.DeploymentID, &e.ServiceID, &e.Level, &e.Step, &e.MessageRedacted, &e.ProgressPercent, &e.Attempt, &e.RequestID, &e.CreatedAt, &e.RolloutID, &e.IntentHash, &e.StateHash, &e.EvidenceHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s PostgresService) ListAudit(projectID string) ([]AuditEvent, error) {
	if _, _, _, err := s.defaultScope(context.Background(), projectID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(context.Background(), `SELECT id, org_id, COALESCE(project_id,''), COALESCE(actor_user_id,''), actor_type, action, resource_type, resource_id, result, metadata_redacted::text, created_at FROM cloud_audit_events WHERE project_id = $1 ORDER BY created_at DESC LIMIT 200`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var metadata string
		if err := rows.Scan(&event.ID, &event.OrgID, &event.ProjectID, &event.ActorUserID, &event.ActorType, &event.Action, &event.ResourceType, &event.ResourceID, &event.Result, &metadata, &event.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(metadata), &event.MetadataRedacted)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s PostgresService) NodeDiagnostics(projectID, nodeID string) (NodeDiagnostics, error) {
	ctx := context.Background()
	node, err := s.getNode(ctx, nodeID)
	if err != nil {
		return NodeDiagnostics{}, err
	}
	if node.ProjectID != projectID {
		return NodeDiagnostics{}, ErrNotFound
	}
	diag := NodeDiagnostics{Node: node}
	if node.AgentID != "" {
		agent, err := s.getAgent(ctx, node.AgentID)
		if err == nil {
			diag.Agent = &agent
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT e.id, e.org_id, e.project_id, e.session_id, COALESCE(e.node_id,''), e.level, e.step, e.message_redacted, e.progress_percent, e.created_at FROM bootstrap_events e WHERE e.project_id = $1 AND e.node_id = $2 ORDER BY e.created_at`, projectID, nodeID)
	if err != nil {
		return NodeDiagnostics{}, err
	}
	for rows.Next() {
		var e BootstrapEvent
		if err := rows.Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.SessionID, &e.NodeID, &e.Level, &e.Step, &e.MessageRedacted, &e.ProgressPercent, &e.CreatedAt); err != nil {
			rows.Close()
			return NodeDiagnostics{}, err
		}
		diag.OpenBootstrapEvents = append(diag.OpenBootstrapEvents, e)
	}
	if err := rows.Close(); err != nil {
		return NodeDiagnostics{}, err
	}
	readiness, err := s.ProjectReadiness(projectID)
	if err != nil {
		return NodeDiagnostics{}, err
	}
	diag.Readiness = readiness
	return diag, nil
}

func (s PostgresService) UpsertNode(projectID, name, role, status, publicHost, agentID, key string) (Node, error) {
	ctx := context.Background()
	scope := "node:" + projectID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return Node{}, err
		}
		return s.getNode(ctx, id)
	}
	project, runtime, env, err := s.defaultScope(ctx, projectID)
	if err != nil {
		return Node{}, err
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
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id, org_id, project_id, environment_id, runtime_id, name, role, status, public_host, agent_id, last_seen_at, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, node.ID, node.OrgID, node.ProjectID, node.EnvironmentID, node.RuntimeID, node.Name, node.Role, node.Status, node.PublicHost, node.AgentID, node.LastSeenAt, node.CreatedAt, node.UpdatedAt); err != nil {
		return Node{}, err
	}
	if role == "server" && status == NodeHealthy {
		if _, err := tx.ExecContext(ctx, `UPDATE runtimes SET status = 'ready', server_node_id = $1, updated_at = $2 WHERE id = $3`, node.ID, now, runtime.ID); err != nil {
			return Node{}, err
		}
	}
	if err := insertIdempotency(ctx, tx, scope, key, "node", node.ID); err != nil {
		return Node{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3`, ProjectReady, now, projectID); err != nil {
		return Node{}, err
	}
	return node, tx.Commit()
}

func (s PostgresService) RegisterAgent(projectID, nodeID, fingerprint, credentialHash, version, key string, capabilities map[string]any, endpoints ...AgentEndpoint) (Agent, error) {
	ctx := context.Background()
	scope := "agent:" + projectID + ":" + nodeID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return Agent{}, err
		}
		return s.getAgent(ctx, id)
	}
	node, err := s.getNode(ctx, nodeID)
	if err != nil {
		return Agent{}, err
	}
	if node.ProjectID != projectID {
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
	data, _ := json.Marshal(capabilities)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id, org_id, project_id, runtime_id, node_id, public_key_fingerprint, credential_hash, version, capabilities, status, last_seen_at, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,'active',$10,$11,$12)`, agent.ID, agent.OrgID, agent.ProjectID, agent.RuntimeID, agent.NodeID, agent.PublicKeyFingerprint, agent.CredentialHash, agent.Version, string(data), agent.LastSeenAt, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Agent{}, err
	}
	if len(endpoints) == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET agent_id = $1, agent_version = NULLIF($2,''), agent_endpoint = $3, agent_port = $4, agent_tls_server_name = $5, agent_cert_sha256 = $6, status = 'agent_connecting', last_seen_at = $7, updated_at = $7 WHERE id = $8 AND project_id = $9`, agent.ID, version, endpoint.Address, endpoint.Port, endpoint.TLSServerName, endpoint.CertSHA256, now, nodeID, projectID); err != nil {
			return Agent{}, err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE nodes SET agent_id = $1, agent_version = NULLIF($2,''), status = 'agent_connecting', last_seen_at = $3, updated_at = $3 WHERE id = $4 AND project_id = $5`, agent.ID, version, now, nodeID, projectID); err != nil {
		return Agent{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "agent", agent.ID); err != nil {
		return Agent{}, err
	}
	return agent, tx.Commit()
}

func (s PostgresService) VerifyAgent(projectID, nodeID, token string) (Agent, error) {
	if token == "" {
		return Agent{}, APIError{Status: 401, Code: "AGENT_AUTH_REQUIRED", Message: "agent bearer token is required"}
	}
	ctx := context.Background()
	rows, err := s.DB.QueryContext(ctx, `SELECT id, org_id, project_id, runtime_id, node_id, public_key_fingerprint, COALESCE(credential_hash,''), COALESCE(version,''), capabilities::text, status, last_seen_at, last_rotation_at, created_at, updated_at FROM agents WHERE project_id = $1 AND node_id = $2`, projectID, nodeID)
	if err != nil {
		return Agent{}, err
	}
	defer rows.Close()
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return Agent{}, err
		}
		if bcrypt.CompareHashAndPassword([]byte(agent.CredentialHash), []byte(token)) != nil {
			continue
		}
		if agent.Status != "active" {
			return Agent{}, APIError{Status: 403, Code: "AGENT_REVOKED", Message: "agent is not active"}
		}
		now := s.clock()
		agent.LastSeenAt = &now
		agent.UpdatedAt = now
		_, err = s.DB.ExecContext(ctx, `UPDATE agents SET last_seen_at = $1, updated_at = $1 WHERE id = $2`, now, agent.ID)
		return agent, err
	}
	if err := rows.Err(); err != nil {
		return Agent{}, err
	}
	return Agent{}, APIError{Status: 403, Code: "AGENT_AUTH_INVALID", Message: "agent credential is invalid"}
}

func (s PostgresService) RecordAgentHeartbeat(projectID, nodeID string, heartbeat AgentHeartbeat) (Node, error) {
	ctx := context.Background()
	node, err := s.getNode(ctx, nodeID)
	if err != nil {
		return Node{}, err
	}
	if node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	now := s.clock()
	status := NodeAgentConnecting
	if heartbeat.NodeReady {
		status = NodeHealthy
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, err
	}
	defer tx.Rollback()
	if node.AgentID != "" {
		data, _ := json.Marshal(heartbeat.Capabilities)
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET version = COALESCE(NULLIF($1,''), version), capabilities = COALESCE($2::jsonb, capabilities), last_seen_at = $3, updated_at = $3 WHERE id = $4 AND project_id = $5`, heartbeat.Version, string(data), now, node.AgentID, projectID); err != nil {
			return Node{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status = $1, agent_version = COALESCE(NULLIF($2,''), agent_version), cpu_cores = NULLIF($3,0), memory_mb = NULLIF($4,0), disk_total_gb = NULLIF($5,0), k3s_status = NULLIF($6,''), last_seen_at = $7, last_inventory_at = $7, failure_code = NULL, failure_message_redacted = NULL, updated_at = $7 WHERE id = $8 AND project_id = $9`, status, heartbeat.Version, heartbeat.Capacity.CPUCores, heartbeat.Capacity.MemoryMB, heartbeat.Capacity.DiskTotalGB, heartbeat.K3SStatus, now, nodeID, projectID); err != nil {
		return Node{}, err
	}
	if node.Role == "server" && status == NodeHealthy {
		if _, err := tx.ExecContext(ctx, `UPDATE runtimes SET status = 'ready', server_node_id = $1, updated_at = $2 WHERE id = $3`, nodeID, now, node.RuntimeID); err != nil {
			return Node{}, err
		}
	}
	if status == NodeHealthy {
		if _, err := tx.ExecContext(ctx, `WITH updated AS (UPDATE bootstrap_sessions SET status = 'verifying', updated_at = $1 WHERE project_id = $2 AND node_id = $3 AND status IN ('created','pending','preflight','validating','connecting','installing','installing_k3s','installing_agent','registering_agent','waiting_agent','verifying_agent','verifying') RETURNING org_id, project_id, id, node_id) INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) SELECT $4, org_id, project_id, id, node_id, 'info', 'verifying', 'agent heartbeat marked node healthy; waiting for worker verification', 90, $1 FROM updated`, now, projectID, nodeID, newID("evt")); err != nil {
			return Node{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Node{}, err
	}
	if _, err := s.refreshProject(ctx, projectID); err != nil {
		return Node{}, err
	}
	return s.getNode(ctx, nodeID)
}

func (s PostgresService) RotateAgent(projectID, agentID, credentialHash string) (Agent, error) {
	if credentialHash == "" {
		return Agent{}, APIError{Status: 400, Code: "AGENT_CREDENTIAL_REQUIRED", Message: "agent credential hash is required"}
	}
	ctx := context.Background()
	res, err := s.DB.ExecContext(ctx, `UPDATE agents SET credential_hash = $1, last_rotation_at = $2, updated_at = $2 WHERE id = $3 AND project_id = $4 AND status <> 'revoked'`, credentialHash, s.clock(), agentID, projectID)
	if err != nil {
		return Agent{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Agent{}, ErrNotFound
	}
	return s.getAgent(ctx, agentID)
}

func (s PostgresService) RevokeAgent(projectID, agentID string) (Agent, error) {
	ctx := context.Background()
	res, err := s.DB.ExecContext(ctx, `UPDATE agents SET status = 'revoked', updated_at = $1 WHERE id = $2 AND project_id = $3`, s.clock(), agentID, projectID)
	if err != nil {
		return Agent{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Agent{}, ErrNotFound
	}
	return s.getAgent(ctx, agentID)
}

func (s PostgresService) DrainNode(projectID, nodeID string) (Node, error) {
	ctx := context.Background()
	node, err := s.getNode(ctx, nodeID)
	if err != nil {
		return Node{}, err
	}
	if node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	return Node{}, APIError{Status: 501, Code: "NODE_LIFECYCLE_AGENT_REQUIRED", Message: "node drain must execute through Agent/K3s; registry metadata cannot mark it complete", NextAction: "wire_agent_node_lifecycle_endpoint"}
}

func (s PostgresService) RemoveNode(projectID, nodeID string, force bool) (Node, error) {
	ctx := context.Background()
	node, err := s.getNode(ctx, nodeID)
	if err != nil {
		return Node{}, err
	}
	if node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	if node.Role == "server" && !force {
		var count int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = $1 AND role = 'server' AND status = 'healthy'`, projectID).Scan(&count); err != nil {
			return Node{}, err
		}
		if count <= 1 {
			return Node{}, APIError{Status: 409, Code: "ONLY_SERVER_NODE", Message: "removing the only healthy server would block the runtime", NextAction: "add_or_promote_server_first"}
		}
	}
	return Node{}, APIError{Status: 501, Code: "NODE_LIFECYCLE_AGENT_REQUIRED", Message: "node remove must execute through Agent/K3s; registry metadata cannot mark it complete", NextAction: "wire_agent_node_lifecycle_endpoint"}
}

func (s PostgresService) MarkNodeOffline(projectID, nodeID string) (Node, error) {
	ctx := context.Background()
	node, err := s.getNode(ctx, nodeID)
	if err != nil {
		return Node{}, err
	}
	if node.ProjectID != projectID {
		return Node{}, ErrNotFound
	}
	now := s.clock()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status = $1, failure_code = $2, failure_message_redacted = $3, updated_at = $4 WHERE id = $5 AND project_id = $6`, NodeOffline, "OPERATOR_CONFIRMED_TARGET_RESET", "operator confirmed target reset; record is offline", now, nodeID, projectID); err != nil {
		return Node{}, err
	}
	if node.AgentID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = 'revoked', updated_at = $1 WHERE id = $2 AND project_id = $3`, now, node.AgentID, projectID); err != nil {
			return Node{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtimes SET status = $1, server_node_id = NULL, updated_at = $2 WHERE id = $3 AND server_node_id = $4`, RuntimeNoNodes, now, node.RuntimeID, node.ID); err != nil {
		return Node{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3`, ProjectNoNodes, now, projectID); err != nil {
		return Node{}, err
	}
	if err := tx.Commit(); err != nil {
		return Node{}, err
	}
	return s.getNode(ctx, nodeID)
}

func (s PostgresService) RequestNodeLifecycle(projectID, targetNodeID, action, requestedBy, key, requestID string, confirmRemove, force bool) (NodeLifecycleJob, error) {
	ctx := context.Background()
	if key != "" {
		job, err := scanNodeLifecycle(s.DB.QueryRowContext(ctx, nodeLifecycleSelectSQL+` WHERE project_id = $1 AND target_node_id = $2 AND action = $3 AND idempotency_key = $4`, projectID, targetNodeID, action, key))
		if err == nil {
			return job, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return NodeLifecycleJob{}, err
		}
	}
	if action != "drain" && action != "remove" {
		return NodeLifecycleJob{}, APIError{Status: 400, Code: "NODE_LIFECYCLE_UNSUPPORTED", Message: "node lifecycle action is not supported", RequestID: requestID}
	}
	target, err := s.getNode(ctx, targetNodeID)
	if err != nil {
		return NodeLifecycleJob{}, err
	}
	if target.ProjectID != projectID {
		return NodeLifecycleJob{}, ErrNotFound
	}
	if target.Name == "" {
		return NodeLifecycleJob{}, APIError{Status: 400, Code: "INVALID_NODE_TARGET", Message: "node target name is required", RequestID: requestID}
	}
	if action == "remove" {
		if target.Role == "server" && !force {
			var servers int
			if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = $1 AND role = 'server' AND status = 'healthy'`, projectID).Scan(&servers); err != nil {
				return NodeLifecycleJob{}, err
			}
			if servers <= 1 {
				return NodeLifecycleJob{}, APIError{Status: 409, Code: "ONLY_SERVER_NODE", Message: "removing the only healthy server would block the runtime", NextAction: "add_or_promote_server_first", RequestID: requestID}
			}
		}
		if !confirmRemove {
			return NodeLifecycleJob{}, APIError{Status: 400, Code: "REMOVE_INTENT_REQUIRED", Message: "remove requires explicit intent", RequestID: requestID}
		}
	}
	executor, agent, err := s.lifecycleAgent(ctx, projectID, target.RuntimeID, requestID)
	if err != nil {
		return NodeLifecycleJob{}, err
	}
	now := s.clock()
	job := NodeLifecycleJob{ID: newID("nlj"), OrgID: target.OrgID, ProjectID: projectID, RuntimeID: target.RuntimeID, Action: action, Status: NodeLifecycleRequested, TargetNodeID: target.ID, TargetNodeName: target.Name, NodeID: executor.ID, AgentID: agent.ID, RequestedBy: requestedBy, IdempotencyKey: key, ConfirmRemove: confirmRemove, MaxAttempts: defaultDeploymentMaxAttempts, CreatedAt: now, UpdatedAt: now}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO node_lifecycle_jobs(id, org_id, project_id, runtime_id, action, status, target_node_id, target_node_name, node_id, agent_id, requested_by, idempotency_key, confirm_remove, attempt_count, max_attempts, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),$13,$14,$15,$16,$17)`, job.ID, job.OrgID, job.ProjectID, job.RuntimeID, job.Action, job.Status, job.TargetNodeID, job.TargetNodeName, job.NodeID, job.AgentID, job.RequestedBy, job.IdempotencyKey, job.ConfirmRemove, job.AttemptCount, job.MaxAttempts, job.CreatedAt, job.UpdatedAt)
	return job, err
}

func (s PostgresService) LeaseNodeLifecycle(projectID, nodeID string) (NodeLifecycleLease, bool, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return NodeLifecycleLease{}, false, err
	}
	defer tx.Rollback()
	now := s.clock()
	if err := expireNodeLifecycleLeases(ctx, tx, projectID, now); err != nil {
		return NodeLifecycleLease{}, false, err
	}
	job, err := scanNodeLifecycle(tx.QueryRowContext(ctx, nodeLifecycleSelectSQL+` WHERE project_id = $1 AND node_id = $2 AND status = $3 ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, projectID, nodeID, NodeLifecycleRequested))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeLifecycleLease{}, false, tx.Commit()
	}
	if err != nil {
		return NodeLifecycleLease{}, false, err
	}
	leaseExpiresAt := now.Add(defaultDeploymentLeaseDuration)
	job.Status = NodeLifecycleAccepted
	job.AttemptCount++
	if job.MaxAttempts == 0 {
		job.MaxAttempts = defaultDeploymentMaxAttempts
	}
	job.LeaseToken = newID("lease")
	job.LeaseExpiresAt = &leaseExpiresAt
	job.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE node_lifecycle_jobs SET status = $1, lease_token = $2, lease_expires_at = $3, attempt_count = $4, max_attempts = $5, updated_at = $6 WHERE id = $7`, job.Status, job.LeaseToken, leaseExpiresAt, job.AttemptCount, job.MaxAttempts, now, job.ID); err != nil {
		return NodeLifecycleLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return NodeLifecycleLease{}, false, err
	}
	return NodeLifecycleLease{Job: job, LeaseToken: job.LeaseToken}, true, nil
}

func (s PostgresService) CompleteNodeLifecycle(projectID, nodeID, jobID, requestID string, result NodeLifecycleResult) (NodeLifecycleJob, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return NodeLifecycleJob{}, err
	}
	defer tx.Rollback()
	job, err := scanNodeLifecycle(tx.QueryRowContext(ctx, nodeLifecycleSelectSQL+` WHERE id = $1 FOR UPDATE`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeLifecycleJob{}, ErrNotFound
	}
	if err != nil {
		return NodeLifecycleJob{}, err
	}
	if job.ProjectID != projectID || job.NodeID != nodeID {
		return NodeLifecycleJob{}, ErrNotFound
	}
	if nodeLifecycleTerminal(job.Status) {
		return job, tx.Commit()
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
	if job.Status == NodeLifecycleCompleted && !job.Verified {
		job.Status = NodeLifecycleFailed
		job.FailureCode = "NODE_LIFECYCLE_NOT_VERIFIED"
		job.FailureMessageRedacted = "node lifecycle result was not verified by Agent"
	}
	job.LeaseToken = ""
	job.LeaseExpiresAt = nil
	job.FinishedAt = &now
	job.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE node_lifecycle_jobs SET status = $1, failure_code = NULLIF($2,''), failure_message_redacted = NULLIF($3,''), verified = $4, lease_token = NULL, lease_expires_at = NULL, finished_at = $5, updated_at = $5 WHERE id = $6`, job.Status, job.FailureCode, job.FailureMessageRedacted, job.Verified, now, job.ID); err != nil {
		return NodeLifecycleJob{}, err
	}
	targetStatus := ""
	if job.Status == NodeLifecycleCompleted && job.Action == "drain" {
		targetStatus = NodeDraining
	}
	if job.Status == NodeLifecycleCompleted && job.Action == "remove" {
		targetStatus = NodeRemoved
	}
	if targetStatus != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status = $1, failure_code = NULL, failure_message_redacted = NULL, updated_at = $2 WHERE id = $3 AND project_id = $4`, targetStatus, now, job.TargetNodeID, projectID); err != nil {
			return NodeLifecycleJob{}, err
		}
		if job.Action == "remove" {
			if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = 'revoked', updated_at = $1 WHERE node_id = $2 AND project_id = $3`, now, job.TargetNodeID, projectID); err != nil {
				return NodeLifecycleJob{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return NodeLifecycleJob{}, err
	}
	return job, nil
}

func (s PostgresService) CreateBootstrapSession(projectID, role, publicHost, username, authMethod, createdBy, key string, sshPort int) (BootstrapSession, error) {
	ctx := context.Background()
	scope := "bootstrap:" + projectID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return BootstrapSession{}, err
		}
		return s.GetBootstrapSession(projectID, id)
	}
	project, runtime, env, err := s.defaultScope(ctx, projectID)
	if err != nil {
		return BootstrapSession{}, err
	}
	now := s.clock()
	if role == "" {
		role = "first_server"
		if ok, err := s.hasHealthyServer(ctx, projectID); err != nil {
			return BootstrapSession{}, err
		} else if ok {
			role = "worker"
		}
	}
	if err := s.validateBootstrap(ctx, projectID, role, publicHost); err != nil {
		return BootstrapSession{}, err
	}
	node := Node{ID: newID("node"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: publicHost, Role: roleForNode(role), Status: NodePending, PublicHost: publicHost, K3SRole: k3sRoleForBootstrap(role), CreatedAt: now, UpdatedAt: now}
	var nameTaken bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE runtime_id = $1 AND name = $2)`, runtime.ID, node.Name).Scan(&nameTaken); err != nil {
		return BootstrapSession{}, err
	}
	if nameTaken {
		node.Name = publicHost + "-" + node.ID[len("node-"):]
	}
	session := BootstrapSession{ID: newID("boot"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, NodeID: node.ID, CreatedBy: createdBy, Role: role, Status: BootstrapPending, IdempotencyKey: key, PublicHost: publicHost, SSHPort: sshPort, SSHUsername: username, AuthMethod: authMethod, ExpiresAt: now.Add(30 * time.Minute), MaxAttempts: defaultBootstrapMaxAttempts, CreatedAt: now, UpdatedAt: now}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSession{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id, org_id, project_id, environment_id, runtime_id, name, role, status, public_host, k3s_role, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, node.ID, node.OrgID, node.ProjectID, node.EnvironmentID, node.RuntimeID, node.Name, node.Role, node.Status, node.PublicHost, node.K3SRole, node.CreatedAt, node.UpdatedAt); err != nil {
		return BootstrapSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_sessions(id, org_id, project_id, environment_id, runtime_id, node_id, created_by, role, status, idempotency_key, public_host, ssh_port, ssh_username, auth_method, expires_at, checkpoint_schema_version, checkpoint_plan_version, checkpoint_plan_fingerprint, checkpoint_next_step_index, checkpoint_last_completed_step, checkpoint_updated_at, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`, session.ID, session.OrgID, session.ProjectID, session.EnvironmentID, session.RuntimeID, session.NodeID, session.CreatedBy, session.Role, session.Status, session.IdempotencyKey, session.PublicHost, session.SSHPort, session.SSHUsername, session.AuthMethod, session.ExpiresAt, session.Checkpoint.SchemaVersion, session.Checkpoint.PlanVersion, session.Checkpoint.PlanFingerprint, session.Checkpoint.NextStepIndex, session.Checkpoint.LastCompletedStep, session.Checkpoint.UpdatedAt, session.CreatedAt, session.UpdatedAt); err != nil {
		return BootstrapSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,$5,'info','pending','bootstrap session pending worker',0,$6)`, newID("evt"), session.OrgID, session.ProjectID, session.ID, session.NodeID, now); err != nil {
		return BootstrapSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtimes SET status = 'provisioning', updated_at = $1 WHERE id = $2`, now, runtime.ID); err != nil {
		return BootstrapSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET status = 'bootstrapping', updated_at = $1 WHERE id = $2`, now, projectID); err != nil {
		return BootstrapSession{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "bootstrap_session", session.ID); err != nil {
		return BootstrapSession{}, err
	}
	return session, tx.Commit()
}

func (s PostgresService) UpdateBootstrapSession(projectID, sessionID, status, message string) (BootstrapSession, error) {
	if !validBootstrapStatus(status) {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap status is invalid"}
	}
	ctx := context.Background()
	session, err := s.GetBootstrapSession(projectID, sessionID)
	if err != nil {
		return BootstrapSession{}, err
	}
	if isTerminalBootstrap(session.Status) {
		return BootstrapSession{}, APIError{Status: 409, Code: "BOOTSTRAP_TERMINAL", Message: "terminal bootstrap session cannot change state"}
	}
	now := s.clock()
	if status == "waiting_agent" && (!isLeasedBootstrapStatus(session.Status) || session.LeaseExpiresAt == nil || !session.LeaseExpiresAt.After(now)) {
		return BootstrapSession{}, APIError{Status: 410, Code: "BOOTSTRAP_LEASE_EXPIRED", Message: "bootstrap lease is not active"}
	}
	started := session.StartedAt
	if (status == "validating" || status == "preflight") && started == nil {
		started = &now
	}
	finished := session.FinishedAt
	if !isActiveBootstrap(status) {
		finished = &now
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSession{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status = $1, started_at = $2, finished_at = $3, updated_at = $4 WHERE project_id = $5 AND id = $6`, status, started, finished, now, projectID, sessionID); err != nil {
		return BootstrapSession{}, err
	}
	level := "info"
	if status == "failed" {
		level = "error"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10)`, newID("evt"), session.OrgID, session.ProjectID, session.ID, session.NodeID, level, status, RedactString(message), bootstrapProgress(status), now); err != nil {
		return BootstrapSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return BootstrapSession{}, err
	}
	return s.GetBootstrapSession(projectID, sessionID)
}

func (s PostgresService) LeaseNextBootstrapSession(workerID string, now time.Time, leaseDuration time.Duration) (BootstrapSessionLease, bool, error) {
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
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSessionLease{}, false, err
	}
	defer tx.Rollback()
	now = now.UTC()
	if _, err := recoverExpiredBootstrapLeasesPostgres(ctx, tx, now); err != nil {
		return BootstrapSessionLease{}, false, err
	}
	session, err := scanBootstrapSession(tx.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE lease_owner IS NULL AND expires_at > $1 AND (status IN ('created','pending') OR (status = 'retry_wait' AND next_attempt_at <= $1)) ORDER BY created_at ASC, id ASC FOR UPDATE SKIP LOCKED LIMIT 1`, now))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return BootstrapSessionLease{}, false, err
		}
		return BootstrapSessionLease{}, false, nil
	}
	if err != nil {
		return BootstrapSessionLease{}, false, err
	}
	expiresAt := now.Add(leaseDuration)
	maxAttempts := effectiveBootstrapMaxAttempts(session.MaxAttempts)
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status='validating', lease_owner=$1, lease_token_hash=$2, lease_expires_at=$3, leased_at=$4, lease_heartbeat_at=$4, next_attempt_at=NULL, attempt_count=attempt_count+1, max_attempts=$5, started_at=COALESCE(started_at,$4), updated_at=$4 WHERE id=$6`, workerID, tokenHash, expiresAt, now, maxAttempts, session.ID); err != nil {
		return BootstrapSessionLease{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),'info','validating','bootstrap session leased by worker',10,$6)`, newID("evt"), session.OrgID, session.ProjectID, session.ID, session.NodeID, now); err != nil {
		return BootstrapSessionLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BootstrapSessionLease{}, false, err
	}
	session.Status = "validating"
	session.LeaseOwner = workerID
	session.LeaseTokenHash = tokenHash
	session.LeaseExpiresAt = &expiresAt
	session.LeasedAt = &now
	session.LeaseHeartbeatAt = &now
	session.NextAttemptAt = nil
	session.AttemptCount++
	session.MaxAttempts = maxAttempts
	session.UpdatedAt = now
	if session.StartedAt == nil {
		session.StartedAt = &now
	}
	return BootstrapSessionLease{Session: session, LeaseToken: token, LeaseExpiresAt: expiresAt}, true, nil
}

func (s PostgresService) GetBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken string, now time.Time) (BootstrapSession, error) {
	session, err := s.GetBootstrapSession(projectID, sessionID)
	if err != nil {
		return BootstrapSession{}, err
	}
	if err := validateBootstrapLease(session, workerID, leaseToken, now); err != nil {
		return BootstrapSession{}, err
	}
	return session, nil
}

func (s PostgresService) UpdateBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken, status, message string, now time.Time) (BootstrapSession, error) {
	if !isLeasedBootstrapStatus(status) {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_STATUS", Message: "bootstrap status is invalid"}
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSession{}, err
	}
	defer tx.Rollback()
	session, err := scanBootstrapSession(tx.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE project_id=$1 AND id=$2 FOR UPDATE`, projectID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapSession{}, ErrNotFound
	}
	if err != nil {
		return BootstrapSession{}, err
	}
	if err := validateBootstrapLease(session, workerID, leaseToken, now); err != nil {
		return BootstrapSession{}, err
	}
	now = now.UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status=$1, updated_at=$2 WHERE id=$3`, status, now, sessionID); err != nil {
		return BootstrapSession{}, err
	}
	level := "info"
	if status == "failed" {
		level = "error"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10)`, newID("evt"), session.OrgID, session.ProjectID, session.ID, session.NodeID, level, status, RedactString(message), bootstrapProgress(status), now); err != nil {
		return BootstrapSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return BootstrapSession{}, err
	}
	return s.GetBootstrapSession(projectID, sessionID)
}

func (s PostgresService) GetBootstrapSession(projectID, sessionID string) (BootstrapSession, error) {
	ctx := context.Background()
	if err := s.expireBootstraps(ctx); err != nil {
		return BootstrapSession{}, err
	}
	b, err := scanBootstrapSession(s.DB.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE project_id = $1 AND id = $2`, projectID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapSession{}, ErrNotFound
	}
	if err != nil {
		return BootstrapSession{}, err
	}
	return b, nil
}

func (s PostgresService) ListBootstrapSessions(projectID string) ([]BootstrapSession, error) {
	ctx := context.Background()
	if _, _, _, err := s.defaultScope(ctx, projectID); err != nil {
		return nil, err
	}
	if err := s.expireBootstraps(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, bootstrapSelectSQL+` WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BootstrapSession
	for rows.Next() {
		b, err := scanBootstrapSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s PostgresService) BootstrapEvents(projectID, sessionID string) ([]BootstrapEvent, error) {
	ctx := context.Background()
	if err := s.expireBootstraps(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, org_id, project_id, session_id, COALESCE(node_id,''), level, step, message_redacted, progress_percent, created_at FROM bootstrap_events WHERE project_id = $1 AND session_id = $2 ORDER BY created_at`, projectID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BootstrapEvent
	for rows.Next() {
		var e BootstrapEvent
		if err := rows.Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.SessionID, &e.NodeID, &e.Level, &e.Step, &e.MessageRedacted, &e.ProgressPercent, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s PostgresService) CreateService(projectID string, draft ServiceDraft, key string) (ServiceRecord, error) {
	ctx := context.Background()
	scope := "service:" + projectID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return ServiceRecord{}, err
		}
		return s.getService(ctx, id)
	}
	project, runtime, env, err := s.defaultScope(ctx, projectID)
	if err != nil {
		return ServiceRecord{}, err
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
	record := ServiceRecord{ID: newID("svc"), OrgID: project.OrgID, ProjectID: projectID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: draft.Name, Type: draft.Type, Status: "draft", SourceType: draft.SourceType, RepoURL: draft.RepoURL, Image: draft.Image, Branch: draft.Branch, GitSHA: draft.GitSHA, BuildMethod: draft.BuildMethod, BuildContext: draft.BuildContext, Dockerfile: draft.Dockerfile, ManifestPath: draft.ManifestPath, WatchPaths: draft.WatchPaths, ContainerPort: draft.ContainerPort, HealthPath: draft.HealthPath, Replicas: draft.Replicas, ResourceRequests: cloneStringMap(draft.ResourceRequests), ResourceLimits: cloneStringMap(draft.ResourceLimits), Bindings: cloneServiceBindings(draft.Bindings), Namespace: "default", CreatedAt: now, UpdatedAt: now}
	if record.Name == "" {
		record.Name = record.ID
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ServiceRecord{}, err
	}
	defer tx.Rollback()
	watchPaths, _ := json.Marshal(record.WatchPaths)
	resources, _ := json.Marshal(deploymentIntentResources(record))
	bindings, _ := json.Marshal(record.Bindings)
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_services(id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, repo_url, image, branch, git_sha, build_method, build_context, dockerfile, manifest_path, watch_paths, container_port, health_path, replicas_desired, resources, bindings, namespace, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),NULLIF($14,''),NULLIF($15,''),NULLIF($16,''),NULLIF($17,''),$18::jsonb,NULLIF($19,0),NULLIF($20,''),$21,$22::jsonb,$23::jsonb,$24,$25,$26)`, record.ID, record.OrgID, record.ProjectID, record.EnvironmentID, record.RuntimeID, record.Name, record.Type, record.Status, record.SourceType, record.RepoURL, record.Image, record.Branch, record.GitSHA, record.BuildMethod, record.BuildContext, record.Dockerfile, record.ManifestPath, string(watchPaths), record.ContainerPort, record.HealthPath, record.Replicas, string(resources), string(bindings), record.Namespace, record.CreatedAt, record.UpdatedAt); err != nil {
		return ServiceRecord{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "service", record.ID); err != nil {
		return ServiceRecord{}, err
	}
	return record, tx.Commit()
}

func (s PostgresService) StartImmutableDeployment(snapshot deploymentv1.JobSnapshot, requestedBy, key, requestID string) (DeploymentJob, bool, error) {
	ctx := context.Background()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	// Resolve exact replays before reading mutable authority so a retry remains
	// bound to the original durable job even after routing/policy changes.
	if job, reused, err := s.ReplayImmutableDeployment(snapshot.ProjectID, key, snapshot.PayloadHash); err != nil || reused {
		return job, reused, err
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
	service, err := s.getService(ctx, snapshot.Authority.BuildRecord.ServiceID)
	if err != nil || service.ProjectID != snapshot.ProjectID {
		return DeploymentJob{}, false, ErrNotFound
	}
	if service.EnvironmentID != snapshot.Authority.EnvironmentID || service.RuntimeID != snapshot.Authority.RuntimeID {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "DEPLOYMENT_SERVICE_BINDING_INVALID", Message: "service binding does not match the resolved target", RequestID: requestID}
	}
	node, agent, err := s.deployAgent(ctx, snapshot.ProjectID, snapshot.Authority.RuntimeID, requestID)
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
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	defer tx.Rollback()
	lockKey := "deploy:v1:" + snapshot.ProjectID + ":" + key
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return DeploymentJob{}, false, err
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE scope=$1 AND key=$2`, "deploy:v1:"+snapshot.ProjectID, key).Scan(&existingID)
	if err == nil {
		existing, scanErr := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id=$1 FOR UPDATE`, existingID))
		if scanErr != nil {
			return DeploymentJob{}, false, scanErr
		}
		if existing.PayloadHash != snapshot.PayloadHash {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used with a different deployment payload", RequestID: requestID}
		}
		existing.Reused = true
		return existing, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, false, err
	}
	if err := insertDeployment(ctx, tx, job); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := acquireDeploymentLock(ctx, tx, snapshot.ProjectID, service.ID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, false, err
	}
	event := DeploymentEvent{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: deploymentv1.StateQueued, MessageRedacted: "immutable image deployment queued", ProgressPercent: 0, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now}
	if err := insertDeploymentEvent(ctx, tx, event); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := insertIdempotency(ctx, tx, "deploy:v1:"+snapshot.ProjectID, key, "deployment_job", job.ID); err != nil {
		return DeploymentJob{}, false, err
	}
	return job, false, tx.Commit()
}

func (s PostgresService) ReplayImmutableDeployment(projectID, key, payloadHash string) (DeploymentJob, bool, error) {
	ctx := context.Background()
	var deploymentID string
	err := s.DB.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE scope=$1 AND key=$2`, "deploy:v1:"+projectID, key).Scan(&deploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, false, nil
	}
	if err != nil {
		return DeploymentJob{}, false, err
	}
	job, err := s.getDeployment(ctx, deploymentID)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	if job.ProjectID != projectID {
		return DeploymentJob{}, false, ErrNotFound
	}
	if job.PayloadHash != payloadHash {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used with a different deployment payload"}
	}
	job.Reused = true
	return job, true, nil
}

func (s PostgresService) ImmutableDeploymentCommand(job DeploymentJob) *deploymentv1.AgentCommand {
	if job.Snapshot == nil {
		return nil
	}
	return &deploymentv1.AgentCommand{SchemaVersion: deploymentv1.CommandSchemaVersion, JobID: job.ID, ProjectID: job.ProjectID, EnvironmentID: job.EnvironmentID, RuntimeID: job.RuntimeID, NodeID: job.NodeID, AgentID: job.AgentID, LeaseToken: job.LeaseToken, Attempt: int32(job.AttemptCount), Image: job.Snapshot.Image, Workload: job.Snapshot.Workload, SpecHash: job.SpecHash, Rollout: job.RolloutIntent}
}

func (s PostgresService) RollbackDeployment(projectID, deploymentID, requestedBy, key, requestID string) (DeploymentJob, error) {
	ctx := context.Background()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	scope := "rollback:" + projectID + ":" + deploymentID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return DeploymentJob{}, err
		}
		job, err := s.getDeployment(ctx, id)
		job.Reused = err == nil
		return job, err
	}
	source, err := s.getDeployment(ctx, deploymentID)
	if err != nil {
		return DeploymentJob{}, err
	}
	if source.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	if !isProductionDeploymentMode(source.Mode) {
		return DeploymentJob{}, legacyDeploymentRetiredError(requestID)
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
		exposure.DeploymentJobID, exposure.SpecHash = jobID, ""
		canonical, canonicalErr := exposure.Canonicalize()
		if canonicalErr != nil {
			return DeploymentJob{}, canonicalErr
		}
		now := s.clock()
		intent, intentErr := buildRolloutIntent(source, canonical, source.RolloutIntent.PreviousKnownGoodID, source.RolloutIntent.PreviousKnownGoodHash, source.RolloutIntent.PreviousDigest, source.TerminalResult.KnownGoodID, source.TerminalResult.KnownGoodHash, deploymentv1.RolloutOperationRollback, now)
		if intentErr != nil {
			return DeploymentJob{}, intentErr
		}
		job := rolloutDeploymentJob(source, intent, canonical, requestedBy, key, hashJSON(map[string]string{"source": deploymentID, "operation": "rollback"}), now)
		tx, txErr := s.DB.BeginTx(ctx, nil)
		if txErr != nil {
			return DeploymentJob{}, txErr
		}
		defer tx.Rollback()
		if err := insertDeployment(ctx, tx, job); err != nil {
			return DeploymentJob{}, err
		}
		if err := acquireDeploymentLock(ctx, tx, projectID, job.ServiceID, job.ID, now, requestID); err != nil {
			return DeploymentJob{}, err
		}
		if err := insertDeploymentEvent(ctx, tx, rolloutEvent(job, deploymentv1.RolloutStatePrepared, "explicit rollback prepared", 0, requestID, now, "")); err != nil {
			return DeploymentJob{}, err
		}
		if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
			return DeploymentJob{}, err
		}
		return job, tx.Commit()
	}
	return DeploymentJob{}, APIError{Status: 409, Code: "ROLLBACK_NOT_AVAILABLE", Message: "immutable image jobs require a durable rollout snapshot before rollback", RequestID: requestID}
}

func (s PostgresService) LeaseDeployment(projectID, nodeID string) (DeploymentLease, bool, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentLease{}, false, err
	}
	defer tx.Rollback()
	now := s.clock()
	if err := expireDeploymentLeases(ctx, tx, projectID, now); err != nil {
		return DeploymentLease{}, false, err
	}
	legacy, legacyErr := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE project_id=$1 AND node_id=$2 AND status IN ($3,$4) AND (mode IS NULL OR mode NOT IN ('immutable_image','rollout')) ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, projectID, nodeID, DeploymentQueued, DeploymentRollingBack))
	if legacyErr == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status=$1, failure_code=$2, failure_message_redacted=$3, lease_token=NULL, lease_expires_at=NULL, retry_after=NULL, finished_at=$4, updated_at=$4 WHERE id=$5`, DeploymentFailed, LegacyDeploymentRetired, "legacy deployment jobs are retired", now, legacy.ID); err != nil {
			return DeploymentLease{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM service_deployment_locks WHERE service_id=$1 AND deployment_id=$2`, legacy.ServiceID, legacy.ID); err != nil {
			return DeploymentLease{}, false, err
		}
		if err := insertDeploymentEvent(ctx, tx, DeploymentEvent{ID: newID("depevt"), OrgID: legacy.OrgID, ProjectID: legacy.ProjectID, DeploymentID: legacy.ID, ServiceID: legacy.ServiceID, Level: "error", Step: DeploymentFailed, MessageRedacted: "legacy deployment jobs are retired", ProgressPercent: 100, CreatedAt: now}); err != nil {
			return DeploymentLease{}, false, err
		}
	} else if !errors.Is(legacyErr, sql.ErrNoRows) {
		return DeploymentLease{}, false, legacyErr
	}
	job, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE project_id = $1 AND node_id = $2 AND status IN ($3,$4,$5) AND mode IN ('immutable_image','rollout') AND (retry_after IS NULL OR retry_after <= $6) ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, projectID, nodeID, DeploymentQueued, DeploymentRollingBack, deploymentv1.StateQueued, now))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return DeploymentLease{}, false, err
		}
		return DeploymentLease{}, false, nil
	}
	if err != nil {
		return DeploymentLease{}, false, err
	}
	action := "deploy"
	if job.Status == DeploymentRollingBack {
		action = "rollback"
	}
	if job.Action != "" {
		action = job.Action
	}
	leaseExpiresAt := now.Add(defaultDeploymentLeaseDuration)
	job.Status = deploymentv1.StateLeased
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
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status = $1, action = $2, lease_token = $3, lease_expires_at = $4, retry_after = NULL, attempt_count = $5, max_attempts = $6, started_at = COALESCE(started_at,$7), updated_at = $7 WHERE id = $8`, job.Status, job.Action, job.LeaseToken, leaseExpiresAt, job.AttemptCount, job.MaxAttempts, now, job.ID); err != nil {
		return DeploymentLease{}, false, err
	}
	event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventAgentJobAccepted, MessageRedacted: "agent accepted deployment job", ProgressPercent: 20, CreatedAt: now}
	event.SchemaVersion = deploymentv1.EventSchemaVersion
	event.Step = deploymentv1.StateLeased
	event.ProgressPercent = 10
	event.Attempt = job.AttemptCount
	if err := insertDeploymentEvent(ctx, tx, event); err != nil {
		return DeploymentLease{}, false, err
	}
	service, err := scanService(tx.QueryRowContext(ctx, serviceSelectSQL+` WHERE id = $1`, job.ServiceID))
	if err != nil {
		return DeploymentLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return DeploymentLease{}, false, err
	}
	return DeploymentLease{Deployment: job, Service: service, Action: action, LeaseToken: job.LeaseToken, Command: s.ImmutableDeploymentCommand(job)}, true, nil
}

func (s PostgresService) CompleteDeployment(projectID, nodeID, deploymentID, requestID string, result DeploymentResult) (DeploymentJob, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, err
	}
	defer tx.Rollback()
	job, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id = $1 FOR UPDATE`, deploymentID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeploymentJob{}, ErrNotFound
		}
		return DeploymentJob{}, err
	}
	if job.ProjectID != projectID || job.NodeID != nodeID {
		return DeploymentJob{}, ErrNotFound
	}
	if !isProductionDeploymentMode(job.Mode) {
		return DeploymentJob{}, legacyDeploymentRetiredError(requestID)
	}
	if deploymentTerminalStatus(job.Status) && job.Mode != "rollout" || job.Mode == "rollout" && job.TerminalResult != nil {
		if job.Mode != "rollout" {
			return job, tx.Commit()
		}
		if exactTerminalReplay(job, result) {
			return job, tx.Commit()
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
	job.Status = normalizedDeploymentResultStatus(result.Status)
	if job.Mode == "rollout" {
		job.Status = result.RolloutResult.RolloutState
	}
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
	terminalJSON := any(nil)
	if job.Mode == "rollout" {
		if err := validateRolloutResult(job, result.RolloutResult); err != nil {
			return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_RESULT_MISMATCH", Message: err.Error(), RequestID: requestID}
		}
		copy := *result.RolloutResult
		copy.LeaseToken = ""
		copy.FailureMessageRedacted = RedactString(copy.FailureMessageRedacted)
		job.TerminalResult = &copy
		job.RolloutState, job.RolloutStateHash = copy.RolloutState, copy.StateHash
		job.DesiredDigest, job.CurrentDigest, job.PreviousDigest = copy.DesiredDigest, copy.CurrentDigest, copy.PreviousDigest
		job.KnownGoodID, job.KnownGoodHash, job.ReadinessEvidenceHash = copy.KnownGoodID, copy.KnownGoodHash, copy.ReadinessEvidenceHash
		job.RolloutVersion++
		job.RollbackEligible = copy.RolloutState == deploymentv1.RolloutStateSucceeded && job.RolloutIntent.PreviousKnownGoodID != ""
		encoded, _ := json.Marshal(job.TerminalResult)
		terminalJSON = string(encoded)
	} else if job.Mode == "immutable_image" {
		if result.SchemaVersion != deploymentv1.ResultSchemaVersion || result.SpecHash != job.SpecHash || job.Snapshot == nil || result.ApplicationImage != job.Snapshot.Image.Reference || (job.Status == DeploymentSucceeded && !deploymentResultHasExactDigest(result.ApplicationImageID, job.Snapshot.Image.Digest)) {
			return DeploymentJob{}, APIError{Status: 409, Code: "DEPLOYMENT_RESULT_MISMATCH", Message: "Agent result does not match the immutable deployment command", RequestID: requestID}
		}
		job.TerminalResult = &deploymentv1.AgentResult{SchemaVersion: result.SchemaVersion, LeaseToken: result.LeaseToken, Status: job.Status, SpecHash: result.SpecHash, ApplicationImage: result.ApplicationImage, ApplicationImageID: result.ApplicationImageID, Namespace: result.Namespace, DeploymentName: result.DeploymentName, ServiceName: result.ServiceName, AvailableReplicas: result.AvailableReplicas, FailureCode: result.FailureCode, FailureMessageRedacted: job.FailureMessageRedacted}
		encoded, _ := json.Marshal(job.TerminalResult)
		terminalJSON = string(encoded)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status = $1, manifest_hash = NULLIF($2,''), rollback_eligible = $3, rollback_blocked_reason = NULLIF($4,''), failure_code = NULLIF($5,''), failure_message_redacted = NULLIF($6,''), lease_token = NULL, lease_expires_at = NULL, retry_after = NULL, finished_at = $7, updated_at = $7, terminal_result_json = COALESCE(terminal_result_json,$11::jsonb), rollout_state=NULLIF($12,''), rollout_state_hash=NULLIF($13,''), rollout_version=$14, desired_digest=NULLIF($15,''), current_digest=NULLIF($16,''), previous_digest=NULLIF($17,''), known_good_id=NULLIF($18,''), known_good_hash=NULLIF($19,''), readiness_evidence_hash=NULLIF($20,'') WHERE id = $8 AND project_id = $9 AND node_id = $10`, job.Status, job.ManifestHash, job.RollbackEligible, job.RollbackBlockedReason, job.FailureCode, job.FailureMessageRedacted, now, deploymentID, projectID, nodeID, terminalJSON, job.RolloutState, job.RolloutStateHash, job.RolloutVersion, job.DesiredDigest, job.CurrentDigest, job.PreviousDigest, job.KnownGoodID, job.KnownGoodHash, job.ReadinessEvidenceHash); err != nil {
		return DeploymentJob{}, err
	}
	completionEvents := deploymentCompletionEvents(job, requestID, now)
	if job.Mode == "rollout" {
		event := rolloutEvent(job, job.RolloutState, "Agent reported terminal rollout result", 100, requestID, now, job.RolloutStateHash)
		event.EvidenceHash = job.ReadinessEvidenceHash
		completionEvents = []DeploymentEvent{event}
	}
	for _, event := range completionEvents {
		if err := insertDeploymentEvent(ctx, tx, event); err != nil {
			return DeploymentJob{}, err
		}
	}
	if deploymentTerminalStatus(job.Status) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM service_deployment_locks WHERE service_id = $1 AND deployment_id = $2`, job.ServiceID, job.ID); err != nil {
			return DeploymentJob{}, err
		}
	}
	return job, tx.Commit()
}

func (s PostgresService) ProgressImmutableDeployment(projectID, nodeID, deploymentID, requestID string, progress deploymentv1.Progress) (DeploymentJob, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, err
	}
	defer tx.Rollback()
	job, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id=$1 FOR UPDATE`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && (job.ProjectID != projectID || job.NodeID != nodeID || !isProductionDeploymentMode(job.Mode)) {
		return DeploymentJob{}, ErrNotFound
	}
	if err != nil {
		return DeploymentJob{}, err
	}
	if job.Mode == "rollout" && job.TerminalResult != nil || job.Mode != "rollout" && (deploymentTerminalStatus(job.Status) || job.Status == deploymentv1.StateCancelled) {
		return job, tx.Commit()
	}
	now := s.clock()
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
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_events WHERE deployment_id=$1`, deploymentID).Scan(&count); err != nil {
		return DeploymentJob{}, err
	}
	message := RedactString(progress.MessageRedacted)
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = "deployment progress updated"
	}
	rolloutReplay := job.Mode == "rollout" && job.RolloutState == progress.State && job.RolloutStateHash == progress.StateHash
	leaseExpiresAt := now.Add(defaultDeploymentLeaseDuration)
	rolloutVersion := job.RolloutVersion
	if job.Mode == "rollout" && !rolloutReplay {
		rolloutVersion++
	}
	if rolloutReplay {
		progress.DesiredDigest = job.DesiredDigest
		progress.CurrentDigest = job.CurrentDigest
		progress.PreviousDigest = job.PreviousDigest
		progress.ReadinessEvidenceHash = job.ReadinessEvidenceHash
		progress.FailureCode = job.FailureCode
	}
	nextStatus := progress.State
	if job.Mode == "rollout" && deploymentv1.IsTerminalRolloutState(progress.State) {
		nextStatus = job.Status
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status=$1, lease_expires_at=$2, updated_at=$3, rollout_state=CASE WHEN mode='rollout' THEN $5 ELSE rollout_state END, rollout_state_hash=CASE WHEN mode='rollout' THEN NULLIF($6,'') ELSE rollout_state_hash END, rollout_version=CASE WHEN mode='rollout' THEN $7 ELSE rollout_version END, desired_digest=CASE WHEN mode='rollout' THEN NULLIF($8,'') ELSE desired_digest END, current_digest=CASE WHEN mode='rollout' THEN NULLIF($9,'') ELSE current_digest END, previous_digest=CASE WHEN mode='rollout' THEN NULLIF($10,'') ELSE previous_digest END, readiness_evidence_hash=CASE WHEN mode='rollout' THEN NULLIF($11,'') ELSE readiness_evidence_hash END, failure_code=CASE WHEN mode='rollout' THEN NULLIF($12,'') ELSE failure_code END WHERE id=$4`, nextStatus, leaseExpiresAt, now, deploymentID, progress.State, progress.StateHash, rolloutVersion, progress.DesiredDigest, progress.CurrentDigest, progress.PreviousDigest, progress.ReadinessEvidenceHash, progress.FailureCode); err != nil {
		return DeploymentJob{}, err
	}
	if !rolloutReplay && count < 199 {
		percent := immutableProgressPercent(progress.State)
		if progress.ProgressPercent > 0 {
			percent = int(progress.ProgressPercent)
		}
		event := DeploymentEvent{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: progress.State, MessageRedacted: message, ProgressPercent: percent, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now, RolloutID: progress.RolloutID, IntentHash: progress.IntentHash, StateHash: progress.StateHash, EvidenceHash: progress.ReadinessEvidenceHash}
		if err := insertDeploymentEvent(ctx, tx, event); err != nil {
			return DeploymentJob{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return DeploymentJob{}, err
	}
	return s.getDeployment(ctx, deploymentID)
}

func (s PostgresService) GetDeployment(projectID, deploymentID string) (DeploymentJob, error) {
	job, err := s.getDeployment(context.Background(), deploymentID)
	if err != nil || job.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	return job, nil
}

func (s PostgresService) CancelDeployment(projectID, deploymentID, key, requestID string) (DeploymentJob, bool, error) {
	ctx := context.Background()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	defer tx.Rollback()
	scope := "deploy-cancel:v1:" + projectID
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope+":"+key); err != nil {
		return DeploymentJob{}, false, err
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, key).Scan(&existingID)
	if err == nil {
		if existingID != deploymentID {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used for another deployment cancellation", RequestID: requestID}
		}
		job, scanErr := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id=$1 AND project_id=$2`, deploymentID, projectID))
		if scanErr != nil {
			if errors.Is(scanErr, sql.ErrNoRows) {
				return DeploymentJob{}, false, ErrNotFound
			}
			return DeploymentJob{}, false, scanErr
		}
		job.Reused = true
		return job, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, false, err
	}
	job, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id=$1 FOR UPDATE`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && job.ProjectID != projectID {
		return DeploymentJob{}, false, ErrNotFound
	}
	if err != nil {
		return DeploymentJob{}, false, err
	}
	if deploymentTerminalStatus(job.Status) || job.Status == deploymentv1.StateCancelled {
		if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
			return DeploymentJob{}, false, err
		}
		return job, false, tx.Commit()
	}
	if job.Status != deploymentv1.StateQueued {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "CANCEL_UNSAFE", Message: "deployment has reached an Agent or runtime mutation stage", NextAction: "watch_deployment", RequestID: requestID}
	}
	now := s.clock()
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status=$1, finished_at=$2, updated_at=$2, lease_token=NULL, lease_expires_at=NULL, retry_after=NULL WHERE id=$3`, deploymentv1.StateCancelled, now, deploymentID); err != nil {
		return DeploymentJob{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_deployment_locks WHERE service_id=$1 AND deployment_id=$2`, job.ServiceID, job.ID); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := insertDeploymentEvent(ctx, tx, DeploymentEvent{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: deploymentv1.StateCancelled, MessageRedacted: "deployment cancelled before runtime mutation", ProgressPercent: 100, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now}); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return DeploymentJob{}, false, err
	}
	job, err = s.getDeployment(ctx, deploymentID)
	return job, false, err
}

func (s PostgresService) RetryDeployment(projectID, deploymentID, key, requestID string) (DeploymentJob, bool, error) {
	ctx := context.Background()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "deploy-retry:v1:"+projectID+":"+key); err != nil {
		return DeploymentJob{}, false, err
	}
	job, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id=$1 FOR UPDATE`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && job.ProjectID != projectID {
		return DeploymentJob{}, false, ErrNotFound
	}
	if err != nil {
		return DeploymentJob{}, false, err
	}
	if !isProductionDeploymentMode(job.Mode) {
		return DeploymentJob{}, false, legacyDeploymentRetiredError(requestID)
	}
	scope := "deploy-retry:v1:" + projectID
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, key).Scan(&existingID)
	if err == nil {
		if existingID != deploymentID {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key is bound to another retry operation", RequestID: requestID}
		}
		job.Reused = true
		return job, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, false, err
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
	if err := acquireDeploymentLock(ctx, tx, projectID, job.ServiceID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status=$1, failure_code=NULL, failure_message_redacted=NULL, finished_at=NULL, lease_token=NULL, lease_expires_at=NULL, retry_after=NULL, max_attempts=$2, updated_at=$3 WHERE id=$4`, job.Status, job.MaxAttempts, now, job.ID); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := insertDeploymentEvent(ctx, tx, DeploymentEvent{SchemaVersion: deploymentv1.EventSchemaVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: deploymentv1.StateQueued, MessageRedacted: "lease-exhausted deployment queued for another bounded attempt window", ProgressPercent: 0, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now}); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
		return DeploymentJob{}, false, err
	}
	return job, false, tx.Commit()
}

func (s PostgresService) Audit(orgID, projectID, actorUserID, action, resourceType, resourceID, result string, metadata map[string]any) {
	data, _ := json.Marshal(RedactMap(metadata))
	_, _ = s.DB.ExecContext(context.Background(), `INSERT INTO cloud_audit_events(id, org_id, project_id, actor_user_id, actor_type, action, resource_type, resource_id, result, metadata_redacted, created_at) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),'user',$5,$6,$7,$8,$9,$10)`, newID("aud"), orgID, projectID, actorUserID, action, resourceType, resourceID, result, string(data), s.clock())
}

func (s PostgresService) AuditWorkload(projectID, action, resourceID, result string, metadata map[string]any) {
	data, _ := json.Marshal(RedactMap(metadata))
	var orgID string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COALESCE(org_id,'') FROM projects WHERE id=$1`, projectID).Scan(&orgID); err != nil {
		return
	}
	_, _ = s.DB.ExecContext(context.Background(), `INSERT INTO cloud_audit_events(id, org_id, project_id, actor_user_id, actor_type, action, resource_type, resource_id, result, metadata_redacted, created_at) VALUES($1,$2,$3,NULL,'github_actions',$4,'build_record',$5,$6,$7,$8)`, newID("aud"), orgID, projectID, action, resourceID, result, string(data), s.clock())
}

func (s PostgresService) defaultScope(ctx context.Context, projectID string) (Project, Runtime, Environment, error) {
	project, err := s.getProject(ctx, projectID)
	if err != nil {
		return Project{}, Runtime{}, Environment{}, err
	}
	var runtime Runtime
	err = s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, environment_id, name, type, status, COALESCE(server_node_id,''), created_at, updated_at FROM runtimes WHERE project_id = $1 ORDER BY created_at LIMIT 1`, projectID).Scan(&runtime.ID, &runtime.OrgID, &runtime.ProjectID, &runtime.EnvironmentID, &runtime.Name, &runtime.Type, &runtime.Status, &runtime.ServerNodeID, &runtime.CreatedAt, &runtime.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, Runtime{}, Environment{}, ErrNotFound
	}
	if err != nil {
		return Project{}, Runtime{}, Environment{}, err
	}
	var env Environment
	err = s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, name, type, status, created_at, updated_at FROM environments WHERE id = $1`, runtime.EnvironmentID).Scan(&env.ID, &env.OrgID, &env.ProjectID, &env.Name, &env.Type, &env.Status, &env.CreatedAt, &env.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, Runtime{}, Environment{}, ErrNotFound
	}
	return project, runtime, env, err
}

func (s PostgresService) getProject(ctx context.Context, id string) (Project, error) {
	var p Project
	err := s.DB.QueryRowContext(ctx, `SELECT id, COALESCE(org_id,''), name, COALESCE(slug,''), status, COALESCE(created_by,''), created_at, updated_at FROM projects WHERE id = $1`, id).Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Status, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

func (s PostgresService) getNode(ctx context.Context, id string) (Node, error) {
	row := s.DB.QueryRowContext(ctx, nodeSelectSQL+` WHERE id = $1`, id)
	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return node, err
}

func (s PostgresService) getAgent(ctx context.Context, id string) (Agent, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, runtime_id, node_id, public_key_fingerprint, COALESCE(credential_hash,''), COALESCE(version,''), capabilities::text, status, last_seen_at, last_rotation_at, created_at, updated_at FROM agents WHERE id = $1`, id)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return a, nil
}

func (s PostgresService) getService(ctx context.Context, id string) (ServiceRecord, error) {
	r, err := scanService(s.DB.QueryRowContext(ctx, serviceSelectSQL+` WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceRecord{}, ErrNotFound
	}
	return r, err
}

func (s PostgresService) getDeployment(ctx context.Context, id string) (DeploymentJob, error) {
	d, err := scanDeployment(s.DB.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, ErrNotFound
	}
	return d, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanBootstrapSession(row rowScanner) (BootstrapSession, error) {
	var b BootstrapSession
	var started, finished, leaseExpiresAt, leasedAt, nextAttemptAt, leaseHeartbeatAt, deadLetteredAt, checkpointUpdatedAt sql.NullTime
	err := row.Scan(&b.ID, &b.OrgID, &b.ProjectID, &b.EnvironmentID, &b.RuntimeID, &b.NodeID, &b.CreatedBy, &b.Role, &b.Status, &b.IdempotencyKey, &b.PublicHost, &b.SSHPort, &b.SSHUsername, &b.AuthMethod, &b.ExpiresAt, &started, &finished, &b.LeaseOwner, &b.LeaseTokenHash, &leaseExpiresAt, &leasedAt, &b.AttemptCount, &b.MaxAttempts, &nextAttemptAt, &leaseHeartbeatAt, &b.LastFailureCode, &b.LastFailureRedacted, &deadLetteredAt, &b.Checkpoint.SchemaVersion, &b.Checkpoint.PlanVersion, &b.Checkpoint.PlanFingerprint, &b.Checkpoint.NextStepIndex, &b.Checkpoint.LastCompletedStep, &checkpointUpdatedAt, &b.CreatedAt, &b.UpdatedAt)
	b.StartedAt = nullTimePtr(started)
	b.FinishedAt = nullTimePtr(finished)
	b.LeaseExpiresAt = nullTimePtr(leaseExpiresAt)
	b.LeasedAt = nullTimePtr(leasedAt)
	b.NextAttemptAt = nullTimePtr(nextAttemptAt)
	b.LeaseHeartbeatAt = nullTimePtr(leaseHeartbeatAt)
	b.DeadLetteredAt = nullTimePtr(deadLetteredAt)
	b.Checkpoint.UpdatedAt = nullTimePtr(checkpointUpdatedAt)
	return b, err
}

func scanService(row rowScanner) (ServiceRecord, error) {
	var r ServiceRecord
	var watchPaths, requests, limits, bindings string
	err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.RuntimeID, &r.Name, &r.Type, &r.Status, &r.SourceType, &r.RepoURL, &r.Image, &r.Branch, &r.GitSHA, &r.BuildMethod, &r.BuildContext, &r.Dockerfile, &r.ManifestPath, &watchPaths, &r.ContainerPort, &r.HealthPath, &r.Replicas, &requests, &limits, &bindings, &r.Namespace, &r.CreatedAt, &r.UpdatedAt)
	_ = json.Unmarshal([]byte(watchPaths), &r.WatchPaths)
	_ = json.Unmarshal([]byte(requests), &r.ResourceRequests)
	_ = json.Unmarshal([]byte(limits), &r.ResourceLimits)
	_ = json.Unmarshal([]byte(bindings), &r.Bindings)
	return r, err
}

func scanDeployment(row rowScanner) (DeploymentJob, error) {
	var d DeploymentJob
	var leaseExpiresAt, retryAfter, started, finished sql.NullTime
	var intentJSON, snapshotJSON, terminalJSON, rolloutJSON, exposureJSON string
	err := row.Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.EnvironmentID, &d.RuntimeID, &d.ServiceID, &d.Status, &d.Action, &d.IdempotencyKey, &d.DeploymentPlanHash, &d.ManifestHash, &d.IntentHash, &intentJSON, &d.PreviousRevisionRef, &d.RollbackEligible, &d.RollbackBlockedReason, &d.RequestedBy, &d.AgentID, &d.NodeID, &d.FailureCode, &d.FailureMessageRedacted, &d.LeaseToken, &leaseExpiresAt, &retryAfter, &d.AttemptCount, &d.MaxAttempts, &started, &finished, &d.CreatedAt, &d.UpdatedAt, &d.SchemaVersion, &d.Mode, &snapshotJSON, &d.SpecHash, &d.PayloadHash, &terminalJSON, &d.BaseDeploymentID, &rolloutJSON, &d.RolloutState, &d.RolloutStateHash, &d.RolloutVersion, &d.DesiredDigest, &d.CurrentDigest, &d.PreviousDigest, &exposureJSON, &d.KnownGoodID, &d.KnownGoodHash, &d.ReadinessEvidenceHash)
	if intentJSON != "" {
		var intent DeploymentIntent
		if json.Unmarshal([]byte(intentJSON), &intent) == nil {
			d.DeploymentIntent = &intent
		}
	}
	if snapshotJSON != "" && snapshotJSON != "{}" {
		var snapshot deploymentv1.JobSnapshot
		if json.Unmarshal([]byte(snapshotJSON), &snapshot) == nil && snapshot.SchemaVersion != "" {
			d.Snapshot = &snapshot
		}
	}
	if terminalJSON != "" && terminalJSON != "{}" {
		var result deploymentv1.AgentResult
		if json.Unmarshal([]byte(terminalJSON), &result) == nil && result.SchemaVersion != "" {
			d.TerminalResult = &result
		}
	}
	if rolloutJSON != "" && rolloutJSON != "{}" {
		var intent deploymentv1.RolloutIntent
		if json.Unmarshal([]byte(rolloutJSON), &intent) == nil && intent.SchemaVersion != "" {
			d.RolloutIntent = &intent
		}
	}
	if exposureJSON != "" && exposureJSON != "{}" {
		var exposure exposurev1.ExposureSpec
		if json.Unmarshal([]byte(exposureJSON), &exposure) == nil && exposure.SchemaVersion != "" {
			d.ExposureSpec = &exposure
		}
	}
	d.LeaseExpiresAt = nullTimePtr(leaseExpiresAt)
	d.RetryAfter = nullTimePtr(retryAfter)
	d.StartedAt = nullTimePtr(started)
	d.FinishedAt = nullTimePtr(finished)
	return d, err
}

func scanNodeLifecycle(row rowScanner) (NodeLifecycleJob, error) {
	var j NodeLifecycleJob
	var leaseExpiresAt, finished sql.NullTime
	err := row.Scan(&j.ID, &j.OrgID, &j.ProjectID, &j.RuntimeID, &j.Action, &j.Status, &j.TargetNodeID, &j.TargetNodeName, &j.NodeID, &j.AgentID, &j.RequestedBy, &j.IdempotencyKey, &j.ConfirmRemove, &j.LeaseToken, &leaseExpiresAt, &j.AttemptCount, &j.MaxAttempts, &j.FailureCode, &j.FailureMessageRedacted, &j.Verified, &finished, &j.CreatedAt, &j.UpdatedAt)
	j.LeaseExpiresAt = nullTimePtr(leaseExpiresAt)
	j.FinishedAt = nullTimePtr(finished)
	return j, err
}

func (s PostgresService) deployAgent(ctx context.Context, projectID, runtimeID, requestID string) (Node, Agent, error) {
	rows, err := s.DB.QueryContext(ctx, nodeSelectSQL+` WHERE project_id = $1 AND runtime_id = $2 AND role = 'server' AND status = 'healthy' AND agent_id IS NOT NULL ORDER BY updated_at DESC`, projectID, runtimeID)
	if err != nil {
		return Node{}, Agent{}, err
	}
	defer rows.Close()
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return Node{}, Agent{}, err
		}
		agent, err := s.getAgent(ctx, node.AgentID)
		if err == nil && agent.Status == "active" && capabilityEnabled(agent.Capabilities, "deploy") {
			return node, agent, nil
		}
	}
	if err := rows.Err(); err != nil {
		return Node{}, Agent{}, err
	}
	return Node{}, Agent{}, APIError{Status: 409, Code: "AGENT_NOT_READY", Message: "A healthy server with an online deploy-capable agent is required.", NextAction: "wait_for_agent", RequestID: requestID}
}

func (s PostgresService) lifecycleAgent(ctx context.Context, projectID, runtimeID, requestID string) (Node, Agent, error) {
	rows, err := s.DB.QueryContext(ctx, nodeSelectSQL+` WHERE project_id = $1 AND runtime_id = $2 AND status = 'healthy' AND agent_id IS NOT NULL ORDER BY updated_at DESC`, projectID, runtimeID)
	if err != nil {
		return Node{}, Agent{}, err
	}
	defer rows.Close()
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return Node{}, Agent{}, err
		}
		agent, err := s.getAgent(ctx, node.AgentID)
		if err == nil && agent.Status == "active" && capabilityEnabled(agent.Capabilities, "node_lifecycle") {
			return node, agent, nil
		}
	}
	if err := rows.Err(); err != nil {
		return Node{}, Agent{}, err
	}
	return Node{}, Agent{}, APIError{Status: 409, Code: "AGENT_NOT_READY", Message: "A healthy node-lifecycle-capable Agent is required.", NextAction: "wait_for_agent", RequestID: requestID}
}

func acquireDeploymentLock(ctx context.Context, tx *sql.Tx, projectID, serviceID, deploymentID string, now time.Time, requestID string) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO service_deployment_locks(service_id, project_id, deployment_id, expires_at, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT (service_id) DO UPDATE SET deployment_id = EXCLUDED.deployment_id, expires_at = EXCLUDED.expires_at, updated_at = EXCLUDED.updated_at WHERE service_deployment_locks.expires_at <= $5`, serviceID, projectID, deploymentID, now.Add(30*time.Minute), now)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return APIError{Status: 409, Code: "DEPLOYMENT_LOCKED", Message: "Another deployment is already active for this service.", NextAction: "watch_existing_deployment", RequestID: requestID}
	}
	return nil
}

func insertDeployment(ctx context.Context, tx *sql.Tx, job DeploymentJob) error {
	intent, _ := json.Marshal(job.DeploymentIntent)
	snapshot, _ := json.Marshal(job.Snapshot)
	terminal, _ := json.Marshal(job.TerminalResult)
	rollout, _ := json.Marshal(job.RolloutIntent)
	exposure, _ := json.Marshal(job.ExposureSpec)
	if job.MaxAttempts == 0 {
		job.MaxAttempts = defaultDeploymentMaxAttempts
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO deployment_jobs(id, org_id, project_id, environment_id, runtime_id, service_id, status, action, idempotency_key, deployment_plan_hash, manifest_hash, intent_hash, deployment_intent_json, previous_revision_ref, rollback_eligible, rollback_blocked_reason, requested_by, agent_id, node_id, failure_code, failure_message_redacted, attempt_count, max_attempts, created_at, updated_at, schema_version, mode, snapshot_json, spec_hash, payload_hash, terminal_result_json, retry_after, base_deployment_id, rollout_intent_json, rollout_state, rollout_state_hash, rollout_version, desired_digest, current_digest, previous_digest, exposure_spec_json, known_good_id, known_good_hash, readiness_evidence_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),$13::jsonb,NULLIF($14,''),$15,NULLIF($16,''),NULLIF($17,''),NULLIF($18,''),NULLIF($19,''),NULLIF($20,''),NULLIF($21,''),$22,$23,$24,$25,NULLIF($26,''),NULLIF($27,''),NULLIF($28::jsonb,'null'::jsonb),NULLIF($29,''),NULLIF($30,''),NULLIF($31::jsonb,'null'::jsonb),$32,NULLIF($33,''),NULLIF($34::jsonb,'null'::jsonb),NULLIF($35,''),NULLIF($36,''),$37,NULLIF($38,''),NULLIF($39,''),NULLIF($40,''),NULLIF($41::jsonb,'null'::jsonb),NULLIF($42,''),NULLIF($43,''),NULLIF($44,''))`, job.ID, job.OrgID, job.ProjectID, job.EnvironmentID, job.RuntimeID, job.ServiceID, job.Status, job.Action, job.IdempotencyKey, job.DeploymentPlanHash, job.ManifestHash, job.IntentHash, string(intent), job.PreviousRevisionRef, job.RollbackEligible, job.RollbackBlockedReason, job.RequestedBy, job.AgentID, job.NodeID, job.FailureCode, job.FailureMessageRedacted, job.AttemptCount, job.MaxAttempts, job.CreatedAt, job.UpdatedAt, job.SchemaVersion, job.Mode, string(snapshot), job.SpecHash, job.PayloadHash, string(terminal), job.RetryAfter, job.BaseDeploymentID, string(rollout), job.RolloutState, job.RolloutStateHash, job.RolloutVersion, job.DesiredDigest, job.CurrentDigest, job.PreviousDigest, string(exposure), job.KnownGoodID, job.KnownGoodHash, job.ReadinessEvidenceHash)
	return err
}

func insertDeploymentEvent(ctx context.Context, tx *sql.Tx, event DeploymentEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO deployment_events(id, org_id, project_id, deployment_id, service_id, level, step, message_redacted, progress_percent, request_id, created_at, schema_version, attempt, rollout_id, intent_hash, state_hash, readiness_evidence_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,NULLIF($12,''),$13,NULLIF($14,''),NULLIF($15,''),NULLIF($16,''),NULLIF($17,''))`, event.ID, event.OrgID, event.ProjectID, event.DeploymentID, event.ServiceID, event.Level, event.Step, event.MessageRedacted, event.ProgressPercent, event.RequestID, event.CreatedAt, event.SchemaVersion, event.Attempt, event.RolloutID, event.IntentHash, event.StateHash, event.EvidenceHash)
	return err
}

func expireDeploymentLeases(ctx context.Context, tx *sql.Tx, projectID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, deploymentSelectSQL+` WHERE project_id = $1 AND status IN ($2,$3,$4,$5,$6,$7,$8,$9,$10) AND lease_expires_at <= $11 FOR UPDATE`, projectID, DeploymentWaitingAgent, deploymentv1.StateLeased, deploymentv1.StatePulling, deploymentv1.StateApplying, deploymentv1.StateWaitingReady, deploymentv1.RolloutStatePrepared, deploymentv1.RolloutStateWaiting, deploymentv1.RolloutStateFailed, deploymentv1.RolloutStateRollingBack, now)
	if err != nil {
		return err
	}
	var expired []DeploymentJob
	for rows.Next() {
		job, err := scanDeployment(rows)
		if err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, job)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, job := range expired {
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		nextStatus := DeploymentQueued
		if isProductionDeploymentMode(job.Mode) {
			nextStatus = deploymentv1.StateQueued
		}
		if job.Action == "rollback" {
			nextStatus = DeploymentRollingBack
		}
		level, step, message, progress := "warn", EventAgentLeaseExpired, "agent lease expired; job returned to queue", 20
		finishedAt := any(nil)
		retryAfter := any(nil)
		if job.AttemptCount >= job.MaxAttempts {
			nextStatus, level, step, message, progress = DeploymentDeadLetter, "error", EventDeploymentDeadLetter, "deployment lease attempts exhausted", 100
			if isProductionDeploymentMode(job.Mode) {
				nextStatus, step = deploymentv1.StateFailed, deploymentv1.StateFailed
			}
			finishedAt = now
			if _, err := tx.ExecContext(ctx, `DELETE FROM service_deployment_locks WHERE service_id = $1 AND deployment_id = $2`, job.ServiceID, job.ID); err != nil {
				return err
			}
		} else {
			if isProductionDeploymentMode(job.Mode) {
				retryAfter = now.Add(deploymentRetryBackoff(job.AttemptCount))
				message = "agent lease expired; job queued with bounded retry backoff"
			}
		}
		terminalFailure := nextStatus == DeploymentDeadLetter || nextStatus == deploymentv1.StateFailed
		if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status = $1, lease_token = NULL, lease_expires_at = NULL, retry_after = $6, failure_code = CASE WHEN $2 THEN 'DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED' ELSE failure_code END, failure_message_redacted = CASE WHEN $2 THEN $3 ELSE failure_message_redacted END, finished_at = $4, updated_at = $5 WHERE id = $7`, nextStatus, terminalFailure, message, finishedAt, now, retryAfter, job.ID); err != nil {
			return err
		}
		event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: level, Step: step, MessageRedacted: message, ProgressPercent: progress, CreatedAt: now}
		if isProductionDeploymentMode(job.Mode) {
			event.SchemaVersion = deploymentv1.EventSchemaVersion
			event.Attempt = job.AttemptCount
		}
		if err := insertDeploymentEvent(ctx, tx, event); err != nil {
			return err
		}
		action := "DEPLOYMENT_RETRY_SCHEDULED"
		result := "success"
		if terminalFailure {
			action = "DEPLOYMENT_DEAD_LETTERED"
			result = "failure"
		}
		if err := insertCloudAudit(ctx, tx, job.OrgID, projectID, "agent", action, "deployment_job", job.ID, result, map[string]any{"status": nextStatus, "attempt_count": job.AttemptCount, "max_attempts": job.MaxAttempts}); err != nil {
			return err
		}
	}
	return nil
}

func expireNodeLifecycleLeases(ctx context.Context, tx *sql.Tx, projectID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, nodeLifecycleSelectSQL+` WHERE project_id = $1 AND status = $2 AND lease_expires_at <= $3 FOR UPDATE`, projectID, NodeLifecycleAccepted, now)
	if err != nil {
		return err
	}
	var expired []NodeLifecycleJob
	for rows.Next() {
		job, err := scanNodeLifecycle(rows)
		if err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, job)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, job := range expired {
		if job.MaxAttempts == 0 {
			job.MaxAttempts = defaultDeploymentMaxAttempts
		}
		nextStatus := NodeLifecycleRequested
		finishedAt := any(nil)
		failureCode := job.FailureCode
		failureMessage := job.FailureMessageRedacted
		if job.AttemptCount >= job.MaxAttempts {
			nextStatus = NodeLifecycleFailed
			finishedAt = now
			failureCode = "NODE_LIFECYCLE_LEASE_ATTEMPTS_EXHAUSTED"
			failureMessage = "node lifecycle lease attempts exhausted"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE node_lifecycle_jobs SET status = $1, lease_token = NULL, lease_expires_at = NULL, failure_code = NULLIF($2,''), failure_message_redacted = NULLIF($3,''), finished_at = $4, updated_at = $5 WHERE id = $6`, nextStatus, failureCode, failureMessage, finishedAt, now, job.ID); err != nil {
			return err
		}
	}
	return nil
}

func insertCloudAudit(ctx context.Context, tx *sql.Tx, orgID, projectID, actorUserID, action, resourceType, resourceID, result string, metadata map[string]any) error {
	data, _ := json.Marshal(RedactMap(metadata))
	actorType := "user"
	if actorUserID == "agent" || actorUserID == "worker" {
		actorType = actorUserID
		actorUserID = ""
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO cloud_audit_events(id, org_id, project_id, actor_user_id, actor_type, action, resource_type, resource_id, result, metadata_redacted, created_at) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,now())`, newID("aud"), orgID, projectID, actorUserID, actorType, action, resourceType, resourceID, result, string(data))
	return err
}

func (s PostgresService) refreshProject(ctx context.Context, projectID string) (string, error) {
	if _, err := s.getProject(ctx, projectID); err != nil {
		return "", err
	}
	var healthy int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = $1 AND role = 'server' AND status = 'healthy'`, projectID).Scan(&healthy); err != nil {
		return "", err
	}
	status := ProjectNoNodes
	if healthy > 0 {
		status = ProjectReady
	} else {
		var active int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bootstrap_sessions WHERE project_id = $1 AND status IN ('created','pending','preflight','validating','connecting','installing','installing_k3s','installing_agent','registering_agent','waiting_agent','verifying_agent','verifying')`, projectID).Scan(&active); err != nil {
			return "", err
		}
		if active > 0 {
			status = ProjectBootstrapping
		}
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3`, status, s.clock(), projectID)
	return status, err
}

func (s PostgresService) expireBootstraps(ctx context.Context) error {
	_, err := s.RecoverExpiredBootstrapLeases(s.clock())
	return err
}

func (s PostgresService) validateBootstrap(ctx context.Context, projectID, role, publicHost string) error {
	if publicHost == "" {
		return APIError{Status: 400, Code: "PUBLIC_HOST_REQUIRED", Message: "public_host is required"}
	}
	if role != "first_server" && role != "worker" {
		return APIError{Status: 400, Code: "INVALID_NODE_ROLE", Message: "role must be first_server or worker"}
	}
	hasServer, err := s.hasHealthyServer(ctx, projectID)
	if err != nil {
		return err
	}
	if role == "first_server" && hasServer {
		return APIError{Status: 409, Code: "SERVER_NODE_EXISTS", Message: "this runtime already has a healthy server", NextAction: "add_worker"}
	}
	if role == "worker" && !hasServer {
		return APIError{Status: 409, Code: "SERVER_NODE_REQUIRED", Message: "add a healthy first server before adding workers", NextAction: "add_first_server"}
	}
	var active int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bootstrap_sessions WHERE project_id = $1 AND public_host = $2 AND status IN ('created','pending','preflight','validating','connecting','installing','installing_k3s','installing_agent','registering_agent','waiting_agent','verifying_agent','verifying')`, projectID, publicHost).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return APIError{Status: 409, Code: "ACTIVE_BOOTSTRAP_EXISTS", Message: "an active bootstrap session already targets this host", NextAction: "watch_existing_session"}
	}
	return nil
}

func (s PostgresService) hasHealthyServer(ctx context.Context, projectID string) (bool, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = $1 AND role = 'server' AND status = 'healthy'`, projectID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s PostgresService) idempotentResource(ctx context.Context, scope, key string) (string, bool, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE scope = $1 AND key = $2`, scope, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func (s PostgresService) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type nodeScanner interface {
	Scan(dest ...any) error
}

func scanAgent(row nodeScanner) (Agent, error) {
	var a Agent
	var capabilities string
	var seen, rotated sql.NullTime
	if err := row.Scan(&a.ID, &a.OrgID, &a.ProjectID, &a.RuntimeID, &a.NodeID, &a.PublicKeyFingerprint, &a.CredentialHash, &a.Version, &capabilities, &a.Status, &seen, &rotated, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Agent{}, err
	}
	_ = json.Unmarshal([]byte(capabilities), &a.Capabilities)
	a.LastSeenAt = nullTimePtr(seen)
	a.LastRotationAt = nullTimePtr(rotated)
	return a, nil
}

func scanNode(row nodeScanner) (Node, error) {
	var node Node
	var lastSeen, lastInventory sql.NullTime
	if err := row.Scan(&node.ID, &node.OrgID, &node.ProjectID, &node.EnvironmentID, &node.RuntimeID, &node.Name, &node.Role, &node.Status, &node.PublicHost, &node.PrivateIP, &node.Provider, &node.Region, &node.OSName, &node.OSVersion, &node.Arch, &node.CPUCores, &node.MemoryMB, &node.DiskTotalGB, &node.K3SRole, &node.K3SStatus, &node.K3SVersion, &node.AgentID, &node.AgentVersion, &node.AgentEndpoint, &node.AgentPort, &node.AgentTLSServerName, &node.AgentCertSHA256, &lastSeen, &lastInventory, &node.FailureCode, &node.FailureMessageRedacted, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return Node{}, err
	}
	node.LastSeenAt = nullTimePtr(lastSeen)
	node.LastInventoryAt = nullTimePtr(lastInventory)
	return node, nil
}

func validateAgentEndpoint(publicHost string, endpoint AgentEndpoint) error {
	if endpoint.Address == "" || endpoint.TLSServerName == "" || endpoint.Port < 1 || endpoint.Port > 65535 {
		return APIError{Status: 400, Code: "AGENT_ENDPOINT_INVALID", Message: "agent endpoint, port, and TLS server name are required"}
	}
	if !strings.EqualFold(endpoint.Address, publicHost) || !strings.EqualFold(endpoint.TLSServerName, publicHost) {
		return APIError{Status: 400, Code: "AGENT_ENDPOINT_MISMATCH", Message: "agent endpoint must match the bootstrap public host"}
	}
	if ip := net.ParseIP(endpoint.Address); ip != nil && ip.IsLoopback() {
		return APIError{Status: 400, Code: "AGENT_ENDPOINT_INVALID", Message: "agent endpoint must not be loopback"}
	}
	if len(endpoint.CertSHA256) != 64 {
		return APIError{Status: 400, Code: "AGENT_CERT_PIN_INVALID", Message: "agent certificate SHA-256 is invalid"}
	}
	for _, c := range endpoint.CertSHA256 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return APIError{Status: 400, Code: "AGENT_CERT_PIN_INVALID", Message: "agent certificate SHA-256 is invalid"}
		}
	}
	if host, _, err := net.SplitHostPort(endpoint.Address); err == nil && host != "" {
		return fmt.Errorf("agent endpoint must not include a port")
	}
	return nil
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func insertIdempotency(ctx context.Context, tx *sql.Tx, scope, key, resourceType, resourceID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(scope, key, resource_type, resource_id) VALUES($1,$2,$3,$4)`, scope, key, resourceType, resourceID)
	return err
}
