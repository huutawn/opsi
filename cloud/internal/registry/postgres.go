package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type PostgresService struct {
	DB  *sql.DB
	Now func() time.Time
}

const nodeSelectSQL = `SELECT id, org_id, project_id, environment_id, runtime_id, name, role, status, COALESCE(public_host,''), COALESCE(private_ip,''), COALESCE(provider,''), COALESCE(region,''), COALESCE(os_name,''), COALESCE(os_version,''), COALESCE(arch,''), COALESCE(cpu_cores,0), COALESCE(memory_mb,0), COALESCE(disk_total_gb,0), COALESCE(k3s_role,''), COALESCE(k3s_status,''), COALESCE(k3s_version,''), COALESCE(agent_id,''), COALESCE(agent_version,''), last_seen_at, last_inventory_at, COALESCE(failure_code,''), COALESCE(failure_message_redacted,''), created_at, updated_at FROM nodes`

const serviceSelectSQL = `SELECT id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, COALESCE(repo_url,''), COALESCE(image,''), COALESCE(branch,''), COALESCE(git_sha,''), COALESCE(build_method,''), COALESCE(build_context,''), COALESCE(dockerfile,''), COALESCE(manifest_path,''), watch_paths::text, COALESCE(container_port,0), COALESCE(health_path,''), COALESCE(replicas_desired,0), COALESCE(resources->'requests','{}'::jsonb)::text, COALESCE(resources->'limits','{}'::jsonb)::text, COALESCE(bindings,'[]'::jsonb)::text, namespace, created_at, updated_at FROM control_services`

const deploymentSelectSQL = `SELECT id, org_id, project_id, environment_id, runtime_id, service_id, status, COALESCE(action,'deploy'), idempotency_key, COALESCE(deployment_plan_hash,''), COALESCE(manifest_hash,''), COALESCE(intent_hash,''), deployment_intent_json::text, COALESCE(previous_revision_ref,''), COALESCE(rollback_eligible,false), COALESCE(rollback_blocked_reason,''), COALESCE(requested_by,''), COALESCE(agent_id,''), COALESCE(node_id,''), COALESCE(failure_code,''), COALESCE(failure_message_redacted,''), COALESCE(lease_token,''), lease_expires_at, COALESCE(attempt_count,0), COALESCE(max_attempts,3), started_at, finished_at, created_at, updated_at FROM deployment_jobs`

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
	project := Project{ID: newID("proj"), OrgID: orgID, Name: name, Slug: slug, Status: ProjectNoNodes, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now}
	if project.Name == "" {
		project.Name = project.ID
	}
	if project.Slug == "" {
		project.Slug = project.ID
	}
	env := Environment{ID: newID("env"), OrgID: orgID, ProjectID: project.ID, Name: "default", Type: "dev", Status: "active", CreatedAt: now, UpdatedAt: now}
	runtime := Runtime{ID: newID("rt"), OrgID: orgID, ProjectID: project.ID, EnvironmentID: env.ID, Name: "default", Type: "k3s", Status: RuntimeNoNodes, CreatedAt: now, UpdatedAt: now}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, org_id, name, slug, status, created_by, created_at, updated_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8)`, project.ID, project.OrgID, project.Name, project.Slug, project.Status, project.CreatedBy, project.CreatedAt, project.UpdatedAt); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO environments(id, org_id, project_id, name, type, status, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, env.ID, env.OrgID, env.ProjectID, env.Name, env.Type, env.Status, env.CreatedAt, env.UpdatedAt); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runtimes(id, org_id, project_id, environment_id, name, type, status, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, runtime.ID, runtime.OrgID, runtime.ProjectID, runtime.EnvironmentID, runtime.Name, runtime.Type, runtime.Status, runtime.CreatedAt, runtime.UpdatedAt); err != nil {
		return Project{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "project", project.ID); err != nil {
		return Project{}, err
	}
	return project, tx.Commit()
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
	rows, err := s.DB.QueryContext(ctx, `SELECT id, org_id, project_id, deployment_id, service_id, level, step, message_redacted, progress_percent, COALESCE(request_id,''), created_at FROM deployment_events WHERE project_id = $1 AND deployment_id = $2 ORDER BY created_at`, projectID, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentEvent
	for rows.Next() {
		var e DeploymentEvent
		if err := rows.Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.DeploymentID, &e.ServiceID, &e.Level, &e.Step, &e.MessageRedacted, &e.ProgressPercent, &e.RequestID, &e.CreatedAt); err != nil {
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

func (s PostgresService) RegisterAgent(projectID, nodeID, fingerprint, credentialHash, version, key string, capabilities map[string]any) (Agent, error) {
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
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET agent_id = $1, agent_version = NULLIF($2,''), status = 'agent_connecting', last_seen_at = $3, updated_at = $3 WHERE id = $4 AND project_id = $5`, agent.ID, version, now, nodeID, projectID); err != nil {
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
		if _, err := tx.ExecContext(ctx, `WITH closed AS (UPDATE bootstrap_sessions SET status = 'succeeded', finished_at = $1, updated_at = $1 WHERE project_id = $2 AND node_id = $3 AND status IN ('created','preflight','installing','waiting_agent') RETURNING org_id, project_id, id, node_id) INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) SELECT $4, org_id, project_id, id, node_id, 'info', 'succeeded', 'agent heartbeat marked node healthy', 100, $1 FROM closed`, now, projectID, nodeID, newID("evt")); err != nil {
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
	session := BootstrapSession{ID: newID("boot"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, NodeID: node.ID, CreatedBy: createdBy, Role: role, Status: "created", IdempotencyKey: key, PublicHost: publicHost, SSHPort: sshPort, SSHUsername: username, AuthMethod: authMethod, ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSession{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id, org_id, project_id, environment_id, runtime_id, name, role, status, public_host, k3s_role, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, node.ID, node.OrgID, node.ProjectID, node.EnvironmentID, node.RuntimeID, node.Name, node.Role, node.Status, node.PublicHost, node.K3SRole, node.CreatedAt, node.UpdatedAt); err != nil {
		return BootstrapSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_sessions(id, org_id, project_id, environment_id, runtime_id, node_id, created_by, role, status, idempotency_key, public_host, ssh_port, ssh_username, auth_method, expires_at, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, session.ID, session.OrgID, session.ProjectID, session.EnvironmentID, session.RuntimeID, session.NodeID, session.CreatedBy, session.Role, session.Status, session.IdempotencyKey, session.PublicHost, session.SSHPort, session.SSHUsername, session.AuthMethod, session.ExpiresAt, session.CreatedAt, session.UpdatedAt); err != nil {
		return BootstrapSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,$5,'info','created','bootstrap session created',0,$6)`, newID("evt"), session.OrgID, session.ProjectID, session.ID, session.NodeID, now); err != nil {
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
	now := s.clock()
	started := session.StartedAt
	if status == "preflight" && started == nil {
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

func (s PostgresService) GetBootstrapSession(projectID, sessionID string) (BootstrapSession, error) {
	ctx := context.Background()
	if err := s.expireBootstraps(ctx); err != nil {
		return BootstrapSession{}, err
	}
	var b BootstrapSession
	var started, finished sql.NullTime
	err := s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, environment_id, runtime_id, COALESCE(node_id,''), COALESCE(created_by,''), role, status, idempotency_key, COALESCE(public_host,''), COALESCE(ssh_port,0), COALESCE(ssh_username,''), COALESCE(auth_method,''), expires_at, started_at, finished_at, created_at, updated_at FROM bootstrap_sessions WHERE project_id = $1 AND id = $2`, projectID, sessionID).Scan(&b.ID, &b.OrgID, &b.ProjectID, &b.EnvironmentID, &b.RuntimeID, &b.NodeID, &b.CreatedBy, &b.Role, &b.Status, &b.IdempotencyKey, &b.PublicHost, &b.SSHPort, &b.SSHUsername, &b.AuthMethod, &b.ExpiresAt, &started, &finished, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapSession{}, ErrNotFound
	}
	if err != nil {
		return BootstrapSession{}, err
	}
	b.StartedAt = nullTimePtr(started)
	b.FinishedAt = nullTimePtr(finished)
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
	rows, err := s.DB.QueryContext(ctx, `SELECT id, org_id, project_id, environment_id, runtime_id, COALESCE(node_id,''), COALESCE(created_by,''), role, status, idempotency_key, COALESCE(public_host,''), COALESCE(ssh_port,0), COALESCE(ssh_username,''), COALESCE(auth_method,''), expires_at, started_at, finished_at, created_at, updated_at FROM bootstrap_sessions WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BootstrapSession
	for rows.Next() {
		var b BootstrapSession
		var started, finished sql.NullTime
		if err := rows.Scan(&b.ID, &b.OrgID, &b.ProjectID, &b.EnvironmentID, &b.RuntimeID, &b.NodeID, &b.CreatedBy, &b.Role, &b.Status, &b.IdempotencyKey, &b.PublicHost, &b.SSHPort, &b.SSHUsername, &b.AuthMethod, &b.ExpiresAt, &started, &finished, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.StartedAt = nullTimePtr(started)
		b.FinishedAt = nullTimePtr(finished)
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

func (s PostgresService) StartDeployment(projectID, serviceID, requestedBy, key, requestID string) (DeploymentJob, error) {
	ctx := context.Background()
	scope := "deploy:" + projectID + ":" + serviceID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return DeploymentJob{}, err
		}
		return s.getDeployment(ctx, id)
	}
	readiness, err := s.ProjectReadiness(projectID)
	if err != nil {
		return DeploymentJob{}, err
	}
	if !readiness.CanDeploy {
		return DeploymentJob{}, APIError{Status: 409, Code: "PROJECT_NOT_READY", Message: "Add a healthy server before deploying services.", NextAction: readiness.NextAction, RequestID: requestID}
	}
	service, err := s.getService(ctx, serviceID)
	if err != nil {
		return DeploymentJob{}, err
	}
	if service.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	if err := validateServiceForDeploy(service, requestID); err != nil {
		return DeploymentJob{}, err
	}
	node, agent, err := s.deployAgent(ctx, projectID, service.RuntimeID, requestID)
	if err != nil {
		return DeploymentJob{}, err
	}
	now := s.clock()
	previous, err := s.previousSuccessful(ctx, projectID, serviceID)
	if err != nil {
		return DeploymentJob{}, err
	}
	job := deploymentJobForPlan(service, previous, node, agent, key, requestedBy, now)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, err
	}
	defer tx.Rollback()
	if err := acquireDeploymentLock(ctx, tx, projectID, serviceID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, err
	}
	if err := insertDeployment(ctx, tx, job); err != nil {
		return DeploymentJob{}, err
	}
	for _, event := range deploymentQueuedEvents(job, requestID, now) {
		if err := insertDeploymentEvent(ctx, tx, event); err != nil {
			return DeploymentJob{}, err
		}
	}
	if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
		return DeploymentJob{}, err
	}
	return job, tx.Commit()
}

func (s PostgresService) RollbackDeployment(projectID, deploymentID, requestedBy, key, requestID string) (DeploymentJob, error) {
	ctx := context.Background()
	scope := "rollback:" + projectID + ":" + deploymentID
	if id, ok, err := s.idempotentResource(ctx, scope, key); err != nil || ok {
		if err != nil {
			return DeploymentJob{}, err
		}
		return s.getDeployment(ctx, id)
	}
	source, err := s.getDeployment(ctx, deploymentID)
	if err != nil {
		return DeploymentJob{}, err
	}
	if source.ProjectID != projectID {
		return DeploymentJob{}, ErrNotFound
	}
	if !source.RollbackEligible {
		reason := source.RollbackBlockedReason
		if reason == "" {
			reason = "deployment is not rollback eligible"
		}
		return DeploymentJob{}, APIError{Status: 409, Code: "ROLLBACK_NOT_AVAILABLE", Message: reason, RequestID: requestID}
	}
	service, err := s.getService(ctx, source.ServiceID)
	if err != nil {
		return DeploymentJob{}, err
	}
	node, agent, err := s.deployAgent(ctx, projectID, service.RuntimeID, requestID)
	if err != nil {
		return DeploymentJob{}, err
	}
	now := s.clock()
	job := deploymentJobForPlan(service, source, node, agent, key, requestedBy, now)
	job.Status = DeploymentRollingBack
	job.Action = "rollback"
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, err
	}
	defer tx.Rollback()
	if err := acquireDeploymentLock(ctx, tx, projectID, service.ID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, err
	}
	if err := insertDeployment(ctx, tx, job); err != nil {
		return DeploymentJob{}, err
	}
	event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: DeploymentRollingBack, MessageRedacted: "rollback queued", ProgressPercent: 0, RequestID: requestID, CreatedAt: now}
	if err := insertDeploymentEvent(ctx, tx, event); err != nil {
		return DeploymentJob{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
		return DeploymentJob{}, err
	}
	return job, tx.Commit()
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
	job, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE project_id = $1 AND node_id = $2 AND status IN ($3,$4) ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, projectID, nodeID, DeploymentQueued, DeploymentRollingBack))
	if errors.Is(err, sql.ErrNoRows) {
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
	job.Status = DeploymentWaitingAgent
	job.Action = action
	job.AttemptCount++
	if job.MaxAttempts == 0 {
		job.MaxAttempts = defaultDeploymentMaxAttempts
	}
	job.LeaseToken = newID("lease")
	job.LeaseExpiresAt = &leaseExpiresAt
	job.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status = $1, action = $2, lease_token = $3, lease_expires_at = $4, attempt_count = $5, max_attempts = $6, updated_at = $7 WHERE id = $8`, job.Status, job.Action, job.LeaseToken, leaseExpiresAt, job.AttemptCount, job.MaxAttempts, now, job.ID); err != nil {
		return DeploymentLease{}, false, err
	}
	event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: EventAgentJobAccepted, MessageRedacted: "agent accepted deployment job", ProgressPercent: 20, CreatedAt: now}
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
	return DeploymentLease{Deployment: job, Service: service, Action: action, LeaseToken: job.LeaseToken}, true, nil
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
	if deploymentTerminalStatus(job.Status) {
		return job, tx.Commit()
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
	job.Status = normalizedDeploymentResultStatus(result.Status)
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
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status = $1, manifest_hash = NULLIF($2,''), rollback_eligible = $3, rollback_blocked_reason = NULLIF($4,''), failure_code = NULLIF($5,''), failure_message_redacted = NULLIF($6,''), lease_token = NULL, lease_expires_at = NULL, finished_at = $7, updated_at = $7 WHERE id = $8 AND project_id = $9 AND node_id = $10`, job.Status, job.ManifestHash, job.RollbackEligible, job.RollbackBlockedReason, job.FailureCode, job.FailureMessageRedacted, now, deploymentID, projectID, nodeID); err != nil {
		return DeploymentJob{}, err
	}
	for _, event := range deploymentCompletionEvents(job, requestID, now) {
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

func (s PostgresService) Audit(orgID, projectID, actorUserID, action, resourceType, resourceID, result string, metadata map[string]any) {
	data, _ := json.Marshal(RedactMap(metadata))
	_, _ = s.DB.ExecContext(context.Background(), `INSERT INTO cloud_audit_events(id, org_id, project_id, actor_user_id, actor_type, action, resource_type, resource_id, result, metadata_redacted, created_at) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),'user',$5,$6,$7,$8,$9,$10)`, newID("aud"), orgID, projectID, actorUserID, action, resourceType, resourceID, result, string(data), s.clock())
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
	var leaseExpiresAt, started, finished sql.NullTime
	var intentJSON string
	err := row.Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.EnvironmentID, &d.RuntimeID, &d.ServiceID, &d.Status, &d.Action, &d.IdempotencyKey, &d.DeploymentPlanHash, &d.ManifestHash, &d.IntentHash, &intentJSON, &d.PreviousRevisionRef, &d.RollbackEligible, &d.RollbackBlockedReason, &d.RequestedBy, &d.AgentID, &d.NodeID, &d.FailureCode, &d.FailureMessageRedacted, &d.LeaseToken, &leaseExpiresAt, &d.AttemptCount, &d.MaxAttempts, &started, &finished, &d.CreatedAt, &d.UpdatedAt)
	if intentJSON != "" {
		var intent DeploymentIntent
		if json.Unmarshal([]byte(intentJSON), &intent) == nil {
			d.DeploymentIntent = &intent
		}
	}
	d.LeaseExpiresAt = nullTimePtr(leaseExpiresAt)
	d.StartedAt = nullTimePtr(started)
	d.FinishedAt = nullTimePtr(finished)
	return d, err
}

func (s PostgresService) previousSuccessful(ctx context.Context, projectID, serviceID string) (DeploymentJob, error) {
	d, err := scanDeployment(s.DB.QueryRowContext(ctx, deploymentSelectSQL+` WHERE project_id = $1 AND service_id = $2 AND status = $3 ORDER BY created_at DESC LIMIT 1`, projectID, serviceID, DeploymentSucceeded))
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, nil
	}
	return d, err
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
	if job.MaxAttempts == 0 {
		job.MaxAttempts = defaultDeploymentMaxAttempts
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO deployment_jobs(id, org_id, project_id, environment_id, runtime_id, service_id, status, action, idempotency_key, deployment_plan_hash, manifest_hash, intent_hash, deployment_intent_json, previous_revision_ref, rollback_eligible, rollback_blocked_reason, requested_by, agent_id, node_id, failure_code, failure_message_redacted, attempt_count, max_attempts, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),$13::jsonb,NULLIF($14,''),$15,NULLIF($16,''),NULLIF($17,''),NULLIF($18,''),NULLIF($19,''),NULLIF($20,''),NULLIF($21,''),$22,$23,$24,$25)`, job.ID, job.OrgID, job.ProjectID, job.EnvironmentID, job.RuntimeID, job.ServiceID, job.Status, job.Action, job.IdempotencyKey, job.DeploymentPlanHash, job.ManifestHash, job.IntentHash, string(intent), job.PreviousRevisionRef, job.RollbackEligible, job.RollbackBlockedReason, job.RequestedBy, job.AgentID, job.NodeID, job.FailureCode, job.FailureMessageRedacted, job.AttemptCount, job.MaxAttempts, job.CreatedAt, job.UpdatedAt)
	return err
}

func insertDeploymentEvent(ctx context.Context, tx *sql.Tx, event DeploymentEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO deployment_events(id, org_id, project_id, deployment_id, service_id, level, step, message_redacted, progress_percent, request_id, created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11)`, event.ID, event.OrgID, event.ProjectID, event.DeploymentID, event.ServiceID, event.Level, event.Step, event.MessageRedacted, event.ProgressPercent, event.RequestID, event.CreatedAt)
	return err
}

func expireDeploymentLeases(ctx context.Context, tx *sql.Tx, projectID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, deploymentSelectSQL+` WHERE project_id = $1 AND status = $2 AND lease_expires_at <= $3 FOR UPDATE`, projectID, DeploymentWaitingAgent, now)
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
		if job.Action == "rollback" {
			nextStatus = DeploymentRollingBack
		}
		level, step, message, progress := "warn", EventAgentLeaseExpired, "agent lease expired; job returned to queue", 20
		finishedAt := any(nil)
		if job.AttemptCount >= job.MaxAttempts {
			nextStatus, level, step, message, progress = DeploymentDeadLetter, "error", EventDeploymentDeadLetter, "deployment lease attempts exhausted", 100
			finishedAt = now
			if _, err := tx.ExecContext(ctx, `DELETE FROM service_deployment_locks WHERE service_id = $1 AND deployment_id = $2`, job.ServiceID, job.ID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deployment_jobs SET status = $1, lease_token = NULL, lease_expires_at = NULL, failure_code = CASE WHEN $1 = $2 THEN 'DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED' ELSE failure_code END, failure_message_redacted = CASE WHEN $1 = $2 THEN $3 ELSE failure_message_redacted END, finished_at = $4, updated_at = $5 WHERE id = $6`, nextStatus, DeploymentDeadLetter, message, finishedAt, now, job.ID); err != nil {
			return err
		}
		event := DeploymentEvent{ID: newID("depevt"), OrgID: job.OrgID, ProjectID: projectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: level, Step: step, MessageRedacted: message, ProgressPercent: progress, CreatedAt: now}
		if err := insertDeploymentEvent(ctx, tx, event); err != nil {
			return err
		}
	}
	return nil
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
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bootstrap_sessions WHERE project_id = $1 AND status IN ('created','preflight','installing','waiting_agent')`, projectID).Scan(&active); err != nil {
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
	now := s.clock()
	rows, err := s.DB.QueryContext(ctx, `SELECT id, org_id, project_id, COALESCE(node_id,'') FROM bootstrap_sessions WHERE status IN ('created','preflight','installing','waiting_agent') AND expires_at < $1`, now)
	if err != nil {
		return err
	}
	type expired struct{ id, orgID, projectID, nodeID string }
	var sessions []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.id, &e.orgID, &e.projectID, &e.nodeID); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, e)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, e := range sessions {
		if _, err := s.DB.ExecContext(ctx, `UPDATE bootstrap_sessions SET status = 'expired', finished_at = $1, updated_at = $1 WHERE id = $2`, now, e.id); err != nil {
			return err
		}
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),'warn','expired','bootstrap session expired',100,$6)`, newID("evt"), e.orgID, e.projectID, e.id, e.nodeID, now); err != nil {
			return err
		}
		if _, err := s.refreshProject(ctx, e.projectID); err != nil {
			return err
		}
	}
	return nil
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
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bootstrap_sessions WHERE project_id = $1 AND public_host = $2 AND status IN ('created','preflight','installing','waiting_agent')`, projectID, publicHost).Scan(&active); err != nil {
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
	if err := row.Scan(&node.ID, &node.OrgID, &node.ProjectID, &node.EnvironmentID, &node.RuntimeID, &node.Name, &node.Role, &node.Status, &node.PublicHost, &node.PrivateIP, &node.Provider, &node.Region, &node.OSName, &node.OSVersion, &node.Arch, &node.CPUCores, &node.MemoryMB, &node.DiskTotalGB, &node.K3SRole, &node.K3SStatus, &node.K3SVersion, &node.AgentID, &node.AgentVersion, &lastSeen, &lastInventory, &node.FailureCode, &node.FailureMessageRedacted, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return Node{}, err
	}
	node.LastSeenAt = nullTimePtr(lastSeen)
	node.LastInventoryAt = nullTimePtr(lastInventory)
	return node, nil
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
