package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type PostgresService struct {
	DB  *sql.DB
	Now func() time.Time
}

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
	rows, err := s.DB.QueryContext(context.Background(), `SELECT id, org_id, project_id, environment_id, runtime_id, name, role, status, COALESCE(public_host,''), COALESCE(agent_id,''), COALESCE(agent_version,''), last_seen_at, created_at, updated_at FROM nodes WHERE project_id = $1 ORDER BY created_at`, projectID)
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

func (s PostgresService) RegisterAgent(projectID, nodeID, fingerprint, version, key string, capabilities map[string]any) (Agent, error) {
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
	now := s.clock()
	agent := Agent{ID: newID("agent"), OrgID: node.OrgID, ProjectID: projectID, RuntimeID: node.RuntimeID, NodeID: node.ID, PublicKeyFingerprint: fingerprint, Version: version, Capabilities: capabilities, Status: "active", LastSeenAt: &now, CreatedAt: now, UpdatedAt: now}
	data, _ := json.Marshal(capabilities)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id, org_id, project_id, runtime_id, node_id, public_key_fingerprint, version, capabilities, status, last_seen_at, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,'active',$9,$10,$11)`, agent.ID, agent.OrgID, agent.ProjectID, agent.RuntimeID, agent.NodeID, agent.PublicKeyFingerprint, agent.Version, string(data), agent.LastSeenAt, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET agent_id = $1, agent_version = NULLIF($2,''), last_seen_at = $3, updated_at = $3 WHERE id = $4 AND project_id = $5`, agent.ID, version, now, nodeID, projectID); err != nil {
		return Agent{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "agent", agent.ID); err != nil {
		return Agent{}, err
	}
	return agent, tx.Commit()
}

func (s PostgresService) RotateAgent(projectID, agentID string) (Agent, error) {
	ctx := context.Background()
	res, err := s.DB.ExecContext(ctx, `UPDATE agents SET last_rotation_at = $1, updated_at = $1 WHERE id = $2 AND project_id = $3 AND status <> 'revoked'`, s.clock(), agentID, projectID)
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
		role = "worker"
	}
	node := Node{ID: newID("node"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: publicHost, Role: roleForNode(role), Status: "pending", PublicHost: publicHost, CreatedAt: now, UpdatedAt: now}
	session := BootstrapSession{ID: newID("boot"), OrgID: project.OrgID, ProjectID: project.ID, EnvironmentID: env.ID, RuntimeID: runtime.ID, NodeID: node.ID, CreatedBy: createdBy, Role: role, Status: "created", IdempotencyKey: key, PublicHost: publicHost, SSHPort: sshPort, SSHUsername: username, AuthMethod: authMethod, ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSession{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id, org_id, project_id, environment_id, runtime_id, name, role, status, public_host, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, node.ID, node.OrgID, node.ProjectID, node.EnvironmentID, node.RuntimeID, node.Name, node.Role, node.Status, node.PublicHost, node.CreatedAt, node.UpdatedAt); err != nil {
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

func (s PostgresService) CreateService(projectID, name, serviceType, sourceType, repoURL, image, key string) (ServiceRecord, error) {
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
	if serviceType == "" {
		serviceType = "application"
	}
	if sourceType == "" {
		sourceType = "git"
	}
	record := ServiceRecord{ID: newID("svc"), OrgID: project.OrgID, ProjectID: projectID, EnvironmentID: env.ID, RuntimeID: runtime.ID, Name: name, Type: serviceType, Status: "draft", SourceType: sourceType, RepoURL: repoURL, Image: image, Namespace: "default", CreatedAt: now, UpdatedAt: now}
	if record.Name == "" {
		record.Name = record.ID
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ServiceRecord{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_services(id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, repo_url, image, namespace, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, record.ID, record.OrgID, record.ProjectID, record.EnvironmentID, record.RuntimeID, record.Name, record.Type, record.Status, record.SourceType, record.RepoURL, record.Image, record.Namespace, record.CreatedAt, record.UpdatedAt); err != nil {
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
	now := s.clock()
	job := DeploymentJob{ID: newID("dep"), OrgID: service.OrgID, ProjectID: projectID, EnvironmentID: service.EnvironmentID, RuntimeID: service.RuntimeID, ServiceID: serviceID, Status: DeploymentQueued, IdempotencyKey: key, RequestedBy: requestedBy, CreatedAt: now, UpdatedAt: now}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_jobs(id, org_id, project_id, environment_id, runtime_id, service_id, status, idempotency_key, requested_by, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11)`, job.ID, job.OrgID, job.ProjectID, job.EnvironmentID, job.RuntimeID, job.ServiceID, job.Status, job.IdempotencyKey, job.RequestedBy, job.CreatedAt, job.UpdatedAt); err != nil {
		return DeploymentJob{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
		return DeploymentJob{}, err
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
	row := s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, environment_id, runtime_id, name, role, status, COALESCE(public_host,''), COALESCE(agent_id,''), COALESCE(agent_version,''), last_seen_at, created_at, updated_at FROM nodes WHERE id = $1`, id)
	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return node, err
}

func (s PostgresService) getAgent(ctx context.Context, id string) (Agent, error) {
	var a Agent
	var capabilities string
	var seen, rotated sql.NullTime
	err := s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, runtime_id, node_id, public_key_fingerprint, COALESCE(version,''), capabilities::text, status, last_seen_at, last_rotation_at, created_at, updated_at FROM agents WHERE id = $1`, id).Scan(&a.ID, &a.OrgID, &a.ProjectID, &a.RuntimeID, &a.NodeID, &a.PublicKeyFingerprint, &a.Version, &capabilities, &a.Status, &seen, &rotated, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	_ = json.Unmarshal([]byte(capabilities), &a.Capabilities)
	a.LastSeenAt = nullTimePtr(seen)
	a.LastRotationAt = nullTimePtr(rotated)
	return a, nil
}

func (s PostgresService) getService(ctx context.Context, id string) (ServiceRecord, error) {
	var r ServiceRecord
	err := s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, COALESCE(repo_url,''), COALESCE(image,''), namespace, created_at, updated_at FROM control_services WHERE id = $1`, id).Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.RuntimeID, &r.Name, &r.Type, &r.Status, &r.SourceType, &r.RepoURL, &r.Image, &r.Namespace, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceRecord{}, ErrNotFound
	}
	return r, err
}

func (s PostgresService) getDeployment(ctx context.Context, id string) (DeploymentJob, error) {
	var d DeploymentJob
	var started, finished sql.NullTime
	err := s.DB.QueryRowContext(ctx, `SELECT id, org_id, project_id, environment_id, runtime_id, service_id, status, idempotency_key, COALESCE(requested_by,''), started_at, finished_at, created_at, updated_at FROM deployment_jobs WHERE id = $1`, id).Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.EnvironmentID, &d.RuntimeID, &d.ServiceID, &d.Status, &d.IdempotencyKey, &d.RequestedBy, &started, &finished, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, ErrNotFound
	}
	d.StartedAt = nullTimePtr(started)
	d.FinishedAt = nullTimePtr(finished)
	return d, err
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

func scanNode(row nodeScanner) (Node, error) {
	var node Node
	var lastSeen sql.NullTime
	if err := row.Scan(&node.ID, &node.OrgID, &node.ProjectID, &node.EnvironmentID, &node.RuntimeID, &node.Name, &node.Role, &node.Status, &node.PublicHost, &node.AgentID, &node.AgentVersion, &lastSeen, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return Node{}, err
	}
	node.LastSeenAt = nullTimePtr(lastSeen)
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
