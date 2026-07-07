package dr

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/svcatalog"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	_ "modernc.org/sqlite"
)

func TestAgentBackupRestoreSanitizesTelemetryAndPreservesCriticalMetadata(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := StatePaths{
		DeployDB:         filepath.Join(dir, "src", "deploy.sqlite"),
		ServiceCatalogDB: filepath.Join(dir, "src", "catalog.sqlite"),
		TelemetryDB:      filepath.Join(dir, "src", "telemetry.sqlite"),
	}
	seedAgentState(t, src)

	backupDir := filepath.Join(dir, "backup")
	if err := BackupAgentState(ctx, backupDir, src); err != nil {
		t.Fatalf("backup: %v", err)
	}
	artifactBytes := readAllFiles(t, filepath.Join(backupDir, "agent"))
	for _, forbidden := range []string{"app-secret-plaintext", "PAT-plaintext", "-----BEGIN OPENSSH PRIVATE KEY-----", "raw log contains secret"} {
		if strings.Contains(artifactBytes, forbidden) {
			t.Fatalf("backup artifact leaked forbidden plaintext %q", forbidden)
		}
	}

	restored := StatePaths{
		DeployDB:         filepath.Join(dir, "restored", "deploy.sqlite"),
		ServiceCatalogDB: filepath.Join(dir, "restored", "catalog.sqlite"),
		TelemetryDB:      filepath.Join(dir, "restored", "telemetry.sqlite"),
	}
	if err := RestoreAgentState(ctx, backupDir, restored); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertAgentStateRestored(t, restored)
	assertTableCount(t, restored.TelemetryDB, "logs", 0)
	assertTableCount(t, restored.TelemetryDB, "metrics", 0)
	assertTableCount(t, restored.TelemetryDB, "incidents", 1)
	assertTableCount(t, restored.TelemetryDB, "audit_log", 1)
}

func TestAgentRestoreMissingOrCorruptBackupFailsClearly(t *testing.T) {
	dir := t.TempDir()
	err := RestoreAgentState(context.Background(), filepath.Join(dir, "missing"), StatePaths{DeployDB: filepath.Join(dir, "deploy.sqlite")})
	if err == nil || !strings.Contains(err.Error(), "backup artifact") {
		t.Fatalf("missing backup error = %v", err)
	}
	corrupt := filepath.Join(dir, "backup", "agent")
	if err := os.MkdirAll(corrupt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "deploy.sqlite"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = RestoreAgentState(context.Background(), filepath.Join(dir, "backup"), StatePaths{DeployDB: filepath.Join(dir, "restore.sqlite")})
	if err == nil || !strings.Contains(err.Error(), "verify restored deploy db") {
		t.Fatalf("corrupt backup error = %v", err)
	}
}

func seedAgentState(t *testing.T, paths StatePaths) {
	t.Helper()
	for _, path := range []string{paths.DeployDB, paths.ServiceCatalogDB, paths.TelemetryDB} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	ds, err := deploy.OpenSQLiteStore(paths.DeployDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := ds.UpsertService(context.Background(), deploy.ServiceRecord{ID: "svc-1", ProjectID: "proj-1", Name: "api", Type: "application", Namespace: "default", RepoURL: "https://example.test/repo.git", Branch: "main", BuildContext: "services/api", Dockerfile: "Dockerfile", ManifestPath: "deploy/api.yaml", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := ds.Insert(context.Background(), deploy.Record{DeployID: "dep-1", ProjectID: "proj-1", ServiceID: "svc-1", ServiceName: "api", StartedAt: now, GitSHA: "0123456789abcdef", ImageTag: "api:sha", Status: deploy.StatusSuccess, TriggeredBy: "user-1", MigrationRan: true, RollbackSafe: true}); err != nil {
		t.Fatal(err)
	}
	_ = ds.Close()

	cs, err := svcatalog.OpenStore(paths.ServiceCatalogDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.UpsertManagedService(context.Background(), svcatalog.ManagedService{ID: "pg-1", ProjectID: "proj-1", Name: "postgres", Type: "postgres", Namespace: "default", Mode: "managed", Status: "healthy", Host: "postgres.default.svc", Port: "5432", Version: "16", SecretName: "pg-secret", ConfigMapName: "pg-config", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	_ = cs.Close()

	ts, err := telemetry.OpenSQLiteStore(paths.TelemetryDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.InsertMetric(context.Background(), telemetry.MetricRecord{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "svc-1", Name: "cpu", Value: 1, Unit: "pct", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := ts.InsertLog(context.Background(), telemetry.LogRecord{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "svc-1", Namespace: "default", Level: "error", Message: "raw log contains secret app-secret-plaintext PAT-plaintext", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := ts.InsertIncident(context.Background(), telemetry.IncidentRecord{ID: "inc-1", ProjectID: "proj-1", NodeID: "node-1", ServiceID: "svc-1", Severity: "p2", Status: "open", ContextJSON: `{"summary":"redacted"}`, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := ts.InsertAudit(context.Background(), secret.AuditRecord{ID: "audit-1", ProjectID: "proj-1", Actor: "user-1", Action: "secret.rotate", ResourceType: "secret", ResourceID: "sec-1", Result: "success", MetadataJSON: `{"secret_name":"api-key"}`, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	_ = ts.Close()
}

func assertAgentStateRestored(t *testing.T, paths StatePaths) {
	t.Helper()
	ds, err := deploy.OpenSQLiteStore(paths.DeployDB)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ds.FindSuccessful(context.Background(), "proj-1", "svc-1", "0123456789abcdef")
	if err != nil || rec == nil || !rec.RollbackSafe || !rec.MigrationRan {
		t.Fatalf("restored deployment metadata = %+v err=%v", rec, err)
	}
	_ = ds.Close()
	cs, err := svcatalog.OpenStore(paths.ServiceCatalogDB)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := cs.GetManagedService(context.Background(), "proj-1", "pg-1")
	if err != nil || managed == nil || managed.SecretName != "pg-secret" {
		t.Fatalf("restored managed service = %+v err=%v", managed, err)
	}
	_ = cs.Close()
}

func assertTableCount(t *testing.T, dbPath, table string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d want %d", table, got, want)
	}
}

func readAllFiles(t *testing.T, root string) string {
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
