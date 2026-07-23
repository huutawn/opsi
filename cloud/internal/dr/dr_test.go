package dr

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	authstore "github.com/opsi-dev/opsi/cloud/internal/auth"
	cloudpostgres "github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestCloudBackupRestoreDRProof(t *testing.T) {
	sourceURL := os.Getenv("OPSI_DR_SOURCE_DATABASE_URL")
	restoreURL := os.Getenv("OPSI_DR_RESTORE_DATABASE_URL")
	if sourceURL == "" || restoreURL == "" {
		t.Skip("set OPSI_DR_SOURCE_DATABASE_URL and OPSI_DR_RESTORE_DATABASE_URL")
	}
	sourceBackupURL := firstNonEmpty(os.Getenv("OPSI_DR_SOURCE_BACKUP_URL"), sourceURL)
	restoreBackupURL := firstNonEmpty(os.Getenv("OPSI_DR_RESTORE_BACKUP_URL"), restoreURL)
	source := openMigrated(t, sourceURL)
	defer source.Close()
	target := openMigrated(t, restoreURL)
	defer target.Close()

	seeded := seedCloudDRState(t, source)
	assertCount(t, target, `SELECT COUNT(*) FROM projects WHERE id=$1`, seeded.projectID, 0)

	dir := t.TempDir()
	artifact := filepath.Join(dir, "backup", "opsi-dr-backup.tar.gz")
	backupOut := runRepoScript(t, "opsi-backup.sh", map[string]string{
		"OPSI_BACKUP_DIR":         filepath.Join(dir, "backup"),
		"OPSI_BACKUP_ARTIFACT":    artifact,
		"OPSI_CLOUD_DATABASE_URL": sourceBackupURL,
		"OPSI_PGDUMP_CMD":         os.Getenv("OPSI_DR_PGDUMP_CMD"),
	})
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("backup artifact missing: %v", err)
	}
	restoreOut := runRepoScript(t, "opsi-restore.sh", map[string]string{
		"OPSI_BACKUP_ARTIFACT":           artifact,
		"OPSI_RESTORE_DIR":               filepath.Join(dir, "restore-stage"),
		"OPSI_CLOUD_DATABASE_URL":        restoreBackupURL,
		"OPSI_PGRESTORE_CMD":             os.Getenv("OPSI_DR_PGRESTORE_CMD"),
		"OPSI_DR_KEY_MATERIAL_CONFIRMED": "1",
		"OPSI_CLOUD_ONLY_RESTORE":        "1",
	})
	inspectOut := runRepoScript(t, "opsi-inspect-backup.sh", map[string]string{
		"OPSI_BACKUP_ARTIFACT": artifact,
	})
	logs := backupOut + restoreOut + inspectOut
	if strings.Contains(logs, seeded.secret) {
		t.Fatalf("backup/restore logs leaked forbidden secret plaintext")
	}
	if strings.Contains(logs, seeded.rawPAT) {
		t.Fatalf("backup/restore logs leaked forbidden PAT plaintext")
	}
	artifactText := readAll(t, filepath.Join(dir, "restore-stage"))
	if strings.Contains(artifactText, seeded.secret) {
		t.Fatalf("backup artifact leaked forbidden secret plaintext")
	}
	if strings.Contains(artifactText, seeded.rawPAT) {
		t.Fatalf("backup artifact leaked forbidden PAT plaintext")
	}
	if err := cloudpostgres.Migrate(context.Background(), target); err != nil {
		t.Fatalf("restored DB is not migration-compatible: %v", err)
	}

	svc := registry.PostgresService{DB: target, Now: func() time.Time { return seeded.now }}
	projects, err := svc.ListProjects(seeded.orgID)
	if err != nil || len(projects) != 1 || projects[0].ID != seeded.projectID {
		t.Fatalf("restored projects unreadable: projects=%+v err=%v", projects, err)
	}
	nodes, err := svc.ListNodes(seeded.projectID)
	if err != nil || !hasNodeStatus(nodes, seeded.nodeID, registry.NodeHealthy) {
		t.Fatalf("restored node metadata unreadable: nodes=%+v err=%v", nodes, err)
	}
	diag, err := svc.NodeDiagnostics(seeded.projectID, seeded.nodeID)
	if err != nil || diag.Agent == nil || diag.Agent.ID == "" {
		t.Fatalf("restored node/agent diagnostics unusable: diag=%+v err=%v", diag, err)
	}
	bootstraps, err := svc.ListBootstrapSessions(seeded.projectID)
	if err != nil || len(bootstraps) != 1 || bootstraps[0].ID != seeded.bootstrapID {
		t.Fatalf("restored bootstrap session unreadable: bootstraps=%+v err=%v", bootstraps, err)
	}
	events, err := svc.BootstrapEvents(seeded.projectID, seeded.bootstrapID)
	if err != nil || len(events) < 2 {
		t.Fatalf("restored bootstrap events unreadable: events=%+v err=%v", events, err)
	}
	deployments, err := svc.ListDeployments(seeded.projectID)
	if err != nil || len(deployments) != 1 || deployments[0].ID != seeded.deploymentID || deployments[0].IntentHash == "" {
		t.Fatalf("restored deployment metadata unreadable: deployments=%+v err=%v", deployments, err)
	}
	assertPATHashMetadata(t, target, seeded.userID, seeded.rawPAT)
	assertCount(t, target, `SELECT COUNT(*) FROM project_memberships WHERE project_id=$1`, seeded.projectID, 1)
	if _, _, err := svc.RetryDeployment(seeded.projectID, seeded.deploymentID, "retry-restored", "req-restored"); err == nil || !strings.Contains(err.Error(), registry.LegacyDeploymentRetired) {
		t.Fatalf("restored legacy retry was not rejected: err=%v", err)
	}
	audit, err := svc.ListAudit(seeded.projectID)
	if err != nil || !hasAudit(audit, "DR_SECRET_REDACTION_CHECK") {
		t.Fatalf("restored audit metadata missing: audit=%+v err=%v", audit, err)
	}
	assertCount(t, target, `SELECT COUNT(*) FROM relay_jobs WHERE project_id=$1`, seeded.projectID, 1)
	assertCount(t, target, `SELECT COUNT(*) FROM relay_events WHERE project_id=$1`, seeded.projectID, 1)
	assertCount(t, target, `SELECT COUNT(*) FROM bootstrap_credentials WHERE session_id=$1`, seeded.bootstrapID, 1)
	assertCount(t, target, `SELECT COUNT(*) FROM bootstrap_registration_tokens WHERE session_id=$1`, seeded.bootstrapID, 1)
}

func TestCloudRestoreMissingCorruptConfigAndSchemaFailuresAreClear(t *testing.T) {
	dir := t.TempDir()
	out, err := runRepoScriptAllowError("opsi-restore.sh", map[string]string{
		"OPSI_BACKUP_ARTIFACT": filepath.Join(dir, "missing.tar.gz"),
	})
	if err == nil || !strings.Contains(out, "backup artifact") {
		t.Fatalf("missing artifact err=%v out=%s", err, out)
	}
	corrupt := filepath.Join(dir, "corrupt.tar.gz")
	if err := os.WriteFile(corrupt, []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runRepoScriptAllowError("opsi-restore.sh", map[string]string{
		"OPSI_BACKUP_ARTIFACT":    corrupt,
		"OPSI_CLOUD_ONLY_RESTORE": "1",
	})
	if err == nil || !strings.Contains(out, "not in gzip format") {
		t.Fatalf("corrupt artifact err=%v out=%s", err, out)
	}
	badSchema := filepath.Join(dir, "bad-schema.tar.gz")
	stage := filepath.Join(dir, "bad-schema")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), []byte(`{"format":"opsi-dr-backup-v2","min_restore_schema_version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if tarOut, err := exec.Command("tar", "-C", stage, "-czf", badSchema, ".").CombinedOutput(); err != nil {
		t.Fatalf("create bad schema artifact: %v %s", err, tarOut)
	}
	out, err = runRepoScriptAllowError("opsi-inspect-backup.sh", map[string]string{
		"OPSI_BACKUP_ARTIFACT": badSchema,
	})
	if err == nil || !strings.Contains(out, "requires newer restore schema") {
		t.Fatalf("schema mismatch err=%v out=%s", err, out)
	}
	withCloud := filepath.Join(dir, "cloud-no-key.tar.gz")
	cloudStage := filepath.Join(dir, "cloud-no-key")
	if err := os.MkdirAll(filepath.Join(cloudStage, "cloud"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudStage, "manifest.json"), []byte(`{"format":"opsi-dr-backup-v2","min_restore_schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloudStage, "cloud", "cloud.dump"), []byte("not a real dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tarOut, err := exec.Command("tar", "-C", cloudStage, "-czf", withCloud, ".").CombinedOutput(); err != nil {
		t.Fatalf("create no-key artifact: %v %s", err, tarOut)
	}
	out, err = runRepoScriptAllowError("opsi-restore.sh", map[string]string{
		"OPSI_BACKUP_ARTIFACT":    withCloud,
		"OPSI_CLOUD_ONLY_RESTORE": "1",
	})
	if err == nil || !strings.Contains(out, "OPSI_DR_KEY_MATERIAL_CONFIRMED=1") {
		t.Fatalf("missing key confirmation err=%v out=%s", err, out)
	}
}

type seededCloud struct {
	orgID, userID, projectID, serviceID, deploymentID, bootstrapID, nodeID string
	secret, rawPAT                                                         string
	now                                                                    time.Time
}

func seedCloudDRState(t *testing.T, db *sql.DB) seededCloud {
	t.Helper()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	seed := seededCloud{orgID: "org-dr-proof", userID: "user-dr-proof", secret: "app-secret-plaintext-PAT-private-key-kubeconfig", rawPAT: "raw-token-value-should-not-appear", now: now}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, seed.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, seed.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO users(id,email) VALUES($1,'dr@example.test')`, seed.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO organizations(id,name,slug) VALUES($1,'DR Org','dr-org')`, seed.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO user_memberships(id,org_id,user_id,role,status) VALUES('member-dr-proof',$1,$2,'owner','active')`, seed.orgID, seed.userID); err != nil {
		t.Fatal(err)
	}
	svc := registry.PostgresService{DB: db, Now: func() time.Time { return now }}
	project, err := svc.CreateProject(seed.orgID, "DR Project", "dr-project", seed.userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	seed.projectID = project.ID
	if _, err := db.ExecContext(context.Background(), `INSERT INTO project_memberships(project_id,user_id,role) VALUES($1,$2,'Owner') ON CONFLICT (project_id,user_id) DO UPDATE SET role=EXCLUDED.role`, project.ID, seed.userID); err != nil {
		t.Fatal(err)
	}
	patHash, err := authstore.HashPAT(seed.rawPAT)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO personal_access_tokens(id,user_id,token_hash,expires_at,revoked) VALUES('pat-dr-proof',$1,$2,$3,false)`, seed.userID, patHash, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := svc.CreateBootstrapSession(project.ID, "first_server", "198.51.100.10", "ubuntu", "ssh_password", seed.userID, "bootstrap-key", 22)
	if err != nil {
		t.Fatal(err)
	}
	seed.bootstrapID = bootstrap.ID
	if _, err := svc.UpdateBootstrapSession(project.ID, bootstrap.ID, "preflight", "preflight ok; redacted"); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(context.Background(), `INSERT INTO bootstrap_credentials(session_id,ciphertext,nonce,expires_at) VALUES($1,decode('DEADBEEF','hex'),decode('CAFE','hex'),$2)`, bootstrap.ID, now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(context.Background(), `INSERT INTO bootstrap_registration_tokens(session_id,org_id,project_id,node_id,token_hash,token_ciphertext,token_nonce,expires_at) VALUES($1,$2,$3,$4,'sha256:tokenhash',decode('ABCD','hex'),decode('1234','hex'),$5)`, bootstrap.ID, seed.orgID, project.ID, bootstrap.NodeID, now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	node, err := svc.UpsertNode(project.ID, "server-1", "server", registry.NodeHealthy, "203.0.113.20", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	seed.nodeID = node.ID
	agent, err := svc.RegisterAgent(project.ID, node.ID, "sha256:test", "$2a$10$redactedcredentialhash", "v1", "agent-key", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordAgentHeartbeat(project.ID, node.ID, registry.AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	service, err := svc.CreateService(project.ID, registry.ServiceDraft{Name: "api", Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: "0123456789abcdef", BuildContext: "services/api", Dockerfile: "Dockerfile", ManifestPath: "deploy/api.yaml"}, "service-key")
	if err != nil {
		t.Fatal(err)
	}
	seed.serviceID = service.ID
	seed.deploymentID = "dep-dr-legacy"
	if _, err := db.ExecContext(context.Background(), `INSERT INTO deployment_jobs(id,org_id,project_id,environment_id,runtime_id,service_id,status,idempotency_key,intent_hash,requested_by,agent_id,node_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'queued','deploy-key','legacy-intent',$7,$8,$9,$10,$10)`, seed.deploymentID, seed.orgID, project.ID, service.EnvironmentID, service.RuntimeID, service.ID, seed.userID, agent.ID, node.ID, now); err != nil {
		t.Fatal(err)
	}
	svc.Audit(seed.orgID, project.ID, seed.userID, "DR_SECRET_REDACTION_CHECK", "deployment", seed.deploymentID, "success", map[string]any{"raw": "redacted"})
	_, err = db.ExecContext(context.Background(), `
INSERT INTO relay_jobs(id, org_id, project_id, runtime_id, agent_id, target_service_id, target_service_name, target_service_type, type, status, body_hash, redacted_body, idempotency_key, created_by, expires_at)
VALUES('relay-dr-proof', $1, $2, $3, $4, $5, 'api', 'application', 'deploy', 'queued', 'sha256:redacted', '{"event":"deploy"}', 'relay-key', $6, $7)
ON CONFLICT (project_id, idempotency_key) DO NOTHING`, seed.orgID, project.ID, service.RuntimeID, agent.ID, service.ID, seed.userID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(context.Background(), `
INSERT INTO relay_events(id, org_id, project_id, job_id, type, message_redacted, metadata_redacted)
VALUES('relayevt-dr-proof', $1, $2, 'relay-dr-proof', 'queued', 'relay queued', '{"safe":true}')
ON CONFLICT DO NOTHING`, seed.orgID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

func openMigrated(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := cloudpostgres.Migrate(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

func runRepoScript(t *testing.T, name string, env map[string]string) string {
	t.Helper()
	out, err := runRepoScriptAllowError(name, env)
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	return out
}

func runRepoScriptAllowError(name string, env map[string]string) (string, error) {
	cmd := exec.Command("bash", filepath.Join("..", "..", "..", "scripts", name))
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/opsi-go-cache", "GOTOOLCHAIN=local")
	for k, v := range env {
		if v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func assertCount(t *testing.T, db *sql.DB, query string, arg any, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(context.Background(), query, arg).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count = %d want %d for %s", got, want, query)
	}
}

func assertPATHashMetadata(t *testing.T, db *sql.DB, userID, rawPAT string) {
	t.Helper()
	var hash string
	if err := db.QueryRowContext(context.Background(), `SELECT token_hash FROM personal_access_tokens WHERE user_id=$1`, userID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == "" || hash == rawPAT || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("PAT metadata did not restore as bcrypt hash: %q", hash)
	}
}

func hasAudit(events []registry.AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func hasNodeStatus(nodes []registry.Node, id, status string) bool {
	for _, node := range nodes {
		if node.ID == id && node.Status == status {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func readAll(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
