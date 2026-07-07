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
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
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
		"OPSI_BACKUP_ARTIFACT":    artifact,
		"OPSI_RESTORE_DIR":        filepath.Join(dir, "restore-stage"),
		"OPSI_CLOUD_DATABASE_URL": restoreBackupURL,
		"OPSI_PGRESTORE_CMD":      os.Getenv("OPSI_DR_PGRESTORE_CMD"),
		"OPSI_CLOUD_ONLY_RESTORE": "1",
	})
	if strings.Contains(backupOut+restoreOut, seeded.secret) {
		t.Fatalf("backup/restore logs leaked forbidden plaintext")
	}
	if strings.Contains(readAll(t, filepath.Join(dir, "restore-stage")), seeded.secret) {
		t.Fatalf("backup artifact leaked forbidden plaintext")
	}
	if err := postgres.Migrate(context.Background(), target); err != nil {
		t.Fatalf("restored DB is not migration-compatible: %v", err)
	}

	svc := registry.PostgresService{DB: target, Now: func() time.Time { return seeded.now }}
	projects, err := svc.ListProjects(seeded.orgID)
	if err != nil || len(projects) != 1 || projects[0].ID != seeded.projectID {
		t.Fatalf("restored projects unreadable: projects=%+v err=%v", projects, err)
	}
	nodes, err := svc.ListNodes(seeded.projectID)
	if err != nil || len(nodes) != 1 || nodes[0].Status != registry.NodeHealthy {
		t.Fatalf("restored node metadata unreadable: nodes=%+v err=%v", nodes, err)
	}
	deployments, err := svc.ListDeployments(seeded.projectID)
	if err != nil || len(deployments) != 1 || deployments[0].ID != seeded.deploymentID || deployments[0].IntentHash == "" {
		t.Fatalf("restored deployment metadata unreadable: deployments=%+v err=%v", deployments, err)
	}
	same, err := svc.StartDeployment(seeded.projectID, seeded.serviceID, seeded.userID, "deploy-key", "req-restored")
	if err != nil || same.ID != seeded.deploymentID {
		t.Fatalf("restored idempotency unusable: same=%+v err=%v", same, err)
	}
	audit, err := svc.ListAudit(seeded.projectID)
	if err != nil || !hasAudit(audit, "DR_SECRET_REDACTION_CHECK") {
		t.Fatalf("restored audit metadata missing: audit=%+v err=%v", audit, err)
	}
	assertCount(t, target, `SELECT COUNT(*) FROM relay_jobs WHERE project_id=$1`, seeded.projectID, 1)
	assertCount(t, target, `SELECT COUNT(*) FROM relay_events WHERE project_id=$1`, seeded.projectID, 1)
}

func TestCloudRestoreMissingOrCorruptBackupFailsClearly(t *testing.T) {
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
}

type seededCloud struct {
	orgID, userID, projectID, serviceID, deploymentID string
	secret                                            string
	now                                               time.Time
}

func seedCloudDRState(t *testing.T, db *sql.DB) seededCloud {
	t.Helper()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	seed := seededCloud{orgID: "org-dr-proof", userID: "user-dr-proof", secret: "app-secret-plaintext-PAT-private-key-kubeconfig", now: now}
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
	svc := registry.PostgresService{DB: db, Now: func() time.Time { return now }}
	project, err := svc.CreateProject(seed.orgID, "DR Project", "dr-project", seed.userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	seed.projectID = project.ID
	node, err := svc.UpsertNode(project.ID, "server-1", "server", registry.NodeHealthy, "203.0.113.20", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
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
	job, err := svc.StartDeployment(project.ID, service.ID, seed.userID, "deploy-key", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	seed.deploymentID = job.ID
	svc.Audit(seed.orgID, project.ID, seed.userID, "DR_SECRET_REDACTION_CHECK", "deployment", job.ID, "success", map[string]any{"raw": seed.secret})
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
	if err := postgres.Migrate(context.Background(), db); err != nil {
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

func hasAudit(events []registry.AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
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
