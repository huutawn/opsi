//go:build postgresintegration

package cutover

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
)

func cutoverPostgresDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run cutover postgres integration tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run cutover postgres integration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("cutover_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	searchDSN := dsn
	if strings.Contains(searchDSN, "?") {
		searchDSN += "&search_path=" + schema
	} else {
		searchDSN += "?search_path=" + schema
	}
	db, err := sql.Open("pgx", searchDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db, schema
}

func seedProjectAndEnv(t *testing.T, db *sql.DB, projectID, envID, appID string) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO users(id, email, created_at) VALUES('user-1', 'user-1@example.com', now()) ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO organizations(id, name, slug, status, created_at, updated_at) VALUES('org-1', 'Org', 'org-1', 'active', now(), now()) ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO projects(id, org_id, name, slug, status, created_by, created_at, updated_at) VALUES($1, 'org-1', 'Test Project', $1, 'ready', 'user-1', now(), now()) ON CONFLICT DO NOTHING`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO environments(id, org_id, project_id, name, type, status, created_at, updated_at) VALUES($1, 'org-1', $2, 'dev', 'dev', 'active', now(), now()) ON CONFLICT DO NOTHING`, envID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO runtimes(id, org_id, project_id, environment_id, name, type, status, created_at, updated_at) VALUES('rt-1', 'org-1', $1, $2, 'primary', 'k3s', 'ready', now(), now()) ON CONFLICT DO NOTHING`, projectID, envID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO control_services(id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, namespace, created_at, updated_at) VALUES($1, 'org-1', $2, $3, 'rt-1', 'app', 'application', 'active', 'image', 'default', now(), now()) ON CONFLICT DO NOTHING`, appID, projectID, envID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCutoverStoreLifecycleAndIdempotency(t *testing.T) {
	db, _ := cutoverPostgresDB(t)
	projectID, envID, appID := "proj-cutover-1", "env-cutover-1", "app-cutover-1"
	seedProjectAndEnv(t, db, projectID, envID, appID)

	store := PostgresStore{DB: db}
	ctx := context.Background()
	now := time.Now().UTC()

	review := cutoverv1.ApplicationCutoverReview{
		SchemaVersion:             cutoverv1.SchemaVersion,
		ID:                        "acrv-db-1",
		ProjectID:                 projectID,
		EnvironmentID:             envID,
		ApplicationID:             appID,
		SourceBindingID:           "bind-src-1",
		SourceResourceID:          "res-src-1",
		TargetResourceID:          "res-tgt-1",
		TargetBindingID:           "bind-tgt-1",
		ApplicationConfigRevision: 1,
		ApplicationConfigHash:     strings.Repeat("a", 64),
		SourceBindingRevision:     strings.Repeat("b", 64),
		TargetBindingRevision:     strings.Repeat("c", 64),
		SourceResourceRevision:    1,
		SourceResourceSpecHash:    strings.Repeat("d", 64),
		TargetResourceRevision:    1,
		TargetResourceSpecHash:    strings.Repeat("e", 64),
		TargetRestoreID:           "rst-1",
		TargetRestoreRevision:     strings.Repeat("f", 64),
		BackupID:                  "bak-1",
		ValidationSummary: cutoverv1.ValidationSummary{
			SourceBindingReady: true,
			TargetBindingReady: true,
			TargetRestoreReady: true,
		},
		Warnings:     []string{cutoverv1.WarningNotContinuouslySynchronized},
		Lifecycle:    cutoverv1.ReviewQueued,
		RequestedBy:  "user-test",
		RequestedAt:  now,
		TargetNodeID: "node-1",
	}

	payloadHash := strings.Repeat("1", 64)
	created, reused, err := store.CreateReview(ctx, review, "idemp-1", payloadHash)
	if err != nil || reused {
		t.Fatalf("create review err=%v reused=%t", err, reused)
	}

	// Idempotency replay with same payload
	replayed, reused, err := store.CreateReview(ctx, review, "idemp-1", payloadHash)
	if err != nil || !reused || replayed.ID != created.ID {
		t.Fatalf("replayed err=%v reused=%t id=%s", err, reused, replayed.ID)
	}

	// Idempotency replay with different payload -> 409
	_, _, err = store.CreateReview(ctx, review, "idemp-1", strings.Repeat("2", 64))
	if err == nil {
		t.Fatal("expected conflict on different payload")
	}

	// Claim review
	token := "lease-tok-1"
	claimed, ok, err := store.ClaimReview(ctx, projectID, "node-1", token, now, now.Add(10*time.Minute))
	if err != nil || !ok || claimed.ID != created.ID || claimed.Lifecycle != cutoverv1.ReviewLeased {
		t.Fatalf("claimed err=%v ok=%t id=%s lifecycle=%s", err, ok, claimed.ID, claimed.Lifecycle)
	}

	// Update claimed with success
	claimed.Lifecycle = cutoverv1.ReviewSucceeded
	reviewedAt := now
	claimed.ReviewedAt = &reviewedAt
	claimed.ValidationSummary.SourceSQLPreflight = "PASS"
	claimed.ValidationSummary.TargetSQLPreflight = "PASS"
	claimed.ValidationSummary.TargetRoleAttributes = "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS"
	claimed.EvidenceHash = cutoverv1.EvidenceHash(claimed)

	updated, err := store.UpdateReviewClaimed(ctx, claimed, token)
	if err != nil {
		t.Fatalf("update claimed err=%v", err)
	}
	if updated.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("expected succeeded lifecycle, got %s", updated.Lifecycle)
	}

	// Succeeded review immutability check
	_, err = db.ExecContext(ctx, `UPDATE application_cutover_reviews SET lifecycle='failed' WHERE id=$1`, created.ID)
	if err == nil {
		t.Fatal("expected trigger error updating succeeded review")
	}
}
