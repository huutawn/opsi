package registry

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func requirePostgresTestDSN(t *testing.T, name string) string {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		msg := "set OPSI_TEST_DATABASE_URL to run Postgres " + name + " test"
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return dsn
}

func TestPostgresBootstrapCheckpointMigrationUpgrade(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "bootstrap checkpoint migration upgrade")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	schema := fmt.Sprintf("p04_checkpoint_%d", time.Now().UnixNano())
	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	}()
	if _, err := conn.ExecContext(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE bootstrap_sessions (id TEXT PRIMARY KEY, status TEXT NOT NULL, attempt_count INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO bootstrap_sessions(id,status,attempt_count) VALUES('boot-old','retry_wait',2)`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.MigrateBootstrapCheckpoint(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var status, planVersion, fingerprint, lastStep string
	var attempts, schemaVersion, nextIndex int
	var updatedAt sql.NullTime
	if err := conn.QueryRowContext(ctx, `SELECT status, attempt_count, checkpoint_schema_version, checkpoint_plan_version, checkpoint_plan_fingerprint, checkpoint_next_step_index, checkpoint_last_completed_step, checkpoint_updated_at FROM bootstrap_sessions WHERE id='boot-old'`).Scan(&status, &attempts, &schemaVersion, &planVersion, &fingerprint, &nextIndex, &lastStep, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "retry_wait" || attempts != 2 || schemaVersion != 0 || planVersion != "" || fingerprint != "" || nextIndex != 0 || lastStep != "" || updatedAt.Valid {
		t.Fatalf("upgraded row status=%q attempts=%d checkpoint=%d/%q/%q/%d/%q/%v", status, attempts, schemaVersion, planVersion, fingerprint, nextIndex, lastStep, updatedAt)
	}
	var columns, constraints int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='bootstrap_sessions' AND column_name LIKE 'checkpoint_%'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='bootstrap_sessions'::regclass AND conname IN ('bootstrap_sessions_checkpoint_schema_version_nonnegative','bootstrap_sessions_checkpoint_next_step_index_nonnegative')`).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if columns != 6 || constraints != 2 {
		t.Fatalf("columns=%d constraints=%d", columns, constraints)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE bootstrap_sessions SET checkpoint_next_step_index=-1 WHERE id='boot-old'`); err == nil {
		t.Fatal("negative checkpoint index passed database constraint")
	}
	stamp := time.Date(2026, 7, 13, 17, 30, 0, 0, time.FixedZone("ICT", 7*60*60))
	if _, err := conn.ExecContext(ctx, `UPDATE bootstrap_sessions SET checkpoint_updated_at=$1 WHERE id='boot-old'`, stamp); err != nil {
		t.Fatal(err)
	}
	var stored time.Time
	if err := conn.QueryRowContext(ctx, `SELECT checkpoint_updated_at FROM bootstrap_sessions WHERE id='boot-old'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(stamp.UTC()) {
		t.Fatalf("stored timestamp=%s", stored)
	}
}
