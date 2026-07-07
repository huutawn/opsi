package webhookrelay

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestPostgresRelayRetryScheduleSurvivesRestart(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set OPSI_TEST_DATABASE_URL to run Postgres relay retry durability test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(newID(), "_", "-")
	orgID, projectID := "org-relay-"+suffix, "proj-relay-"+suffix
	_, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO organizations (id,name,slug) VALUES ($1,'Relay Retry',$2)`, orgID, "relay-retry-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO projects (id,org_id,name,slug) VALUES ($1,$2,'Relay Retry',$3)`, projectID, orgID, "relay-retry-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID) })

	now := time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC)
	fresh := func() *PostgresQueue {
		q := NewPostgresQueue(db)
		q.now = func() time.Time { return now }
		return q
	}
	forbiddenBody := []string{"app-secret-value", "raw log line", "raw_metric_counter", "apiVersion: v1\nclusters:", "BEGIN OPENSSH PRIVATE KEY", "package main", "pat_live_123"}
	forbiddenError := []string{"app-secret-value", "BEGIN OPENSSH PRIVATE KEY", "pat_live_123"}
	body := `{"commits":[{"modified":["apps/api/main.go"]}],"app_secret":"app-secret-value","logs":"raw log line","metrics":"raw_metric_counter","kubeconfig":"apiVersion: v1\nclusters:","private_key":"BEGIN OPENSSH PRIVATE KEY","source":"package main","pat":"pat_live_123"}`
	env := Envelope{ID: "relay-" + suffix, ProjectID: projectID, ServiceID: "svc-api", ServiceName: "api", Body: body, IdempotencyKey: "delivery-" + suffix, TriggeredBy: "user@example.test", ReceivedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := fresh().Enqueue(env); err != nil {
		t.Fatal(err)
	}

	retryAt := now.Add(2 * time.Minute)
	redactedError := registry.RedactString("delivery failed: password=app-secret-value token=pat_live_123 -----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----")
	if _, err := db.ExecContext(context.Background(), `UPDATE relay_jobs SET status='queued', attempt_count=1, next_retry_at=$1, last_error_redacted=$2, updated_at=$3 WHERE id=$4`, retryAt, redactedError, now, env.ID); err != nil {
		t.Fatal(err)
	}
	if err := insertRelayEvent(db, orgID, projectID, env.ID, "retry_scheduled", "delivery failed; retry scheduled", map[string]any{"next_retry_at": retryAt, "attempt_count": 1, "error": "password=app-secret-value token=pat_live_123"}); err != nil {
		t.Fatal(err)
	}

	if got, err := fresh().Next(context.Background(), projectID, 0); err != nil || got != nil {
		t.Fatalf("future retry should not be delivered after restart: got=%+v err=%v", got, err)
	}
	now = retryAt.Add(time.Second)
	got, err := fresh().Next(context.Background(), projectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != env.ID || got.AttemptCount != 2 || got.NextRetryAt == nil || !got.NextRetryAt.Equal(retryAt) || got.Body != "" {
		t.Fatalf("retry state not recovered from durable relay row: %+v", got)
	}

	var status, stored, lastError string
	var attempts int
	var nextRetry time.Time
	if err := db.QueryRowContext(context.Background(), `SELECT status, attempt_count, next_retry_at, redacted_body::text, COALESCE(last_error_redacted,'') FROM relay_jobs WHERE id=$1`, env.ID).Scan(&status, &attempts, &nextRetry, &stored, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || attempts != 2 || !nextRetry.Equal(retryAt) {
		t.Fatalf("durable retry columns not preserved: status=%s attempts=%d next=%v", status, attempts, nextRetry)
	}
	for _, value := range forbiddenBody {
		if strings.Contains(stored, value) {
			t.Fatalf("forbidden value persisted: %q in body=%s error=%s", value, stored, lastError)
		}
	}
	for _, value := range forbiddenError {
		if strings.Contains(lastError, value) {
			t.Fatalf("forbidden value persisted in last error: %q in %s", value, lastError)
		}
	}
	var evidence int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM relay_events WHERE job_id=$1 AND type IN ('created','retry_scheduled','claimed','delivered')`, env.ID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != 4 {
		t.Fatalf("expected restart-visible relay evidence, got %d", evidence)
	}
}
