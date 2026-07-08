package webhookrelay

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresQueuePersistsSanitizedJobsWhenDatabaseAvailable(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "relay persistence")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	orgID := "org-relay-test"
	projectID := "proj-relay-test"
	_, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO organizations (id,name,slug) VALUES ($1,'Relay Test','relay-test')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO projects (id,org_id,name,slug) VALUES ($1,$2,'Relay Test','relay-test')`, projectID, orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID) })

	q1 := NewPostgresQueue(db)
	env := Envelope{ID: "relay-job-test", ProjectID: projectID, ServiceID: "svc-api", ServiceName: "api", Body: `{"commits":[{"modified":["apps/api/main.go"]}],"password":"hunter2"}`, IdempotencyKey: "delivery-test", ReceivedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := q1.Enqueue(env); err != nil {
		t.Fatal(err)
	}
	q2 := NewPostgresQueue(db)
	got, err := q2.Next(context.Background(), projectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != env.ID || got.Body != "" || len(got.Modified) != 1 || got.AttemptCount != 1 {
		t.Fatalf("bad durable relay job: %+v", got)
	}
	var stored string
	if err := db.QueryRowContext(context.Background(), `SELECT redacted_body::text FROM relay_jobs WHERE id=$1`, env.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "hunter2") || strings.Contains(stored, `"body"`) {
		t.Fatalf("raw/sensitive payload persisted: %s", stored)
	}
	var events int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM relay_events WHERE job_id=$1 AND type IN ('created','claimed','delivered')`, env.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("expected created/claimed/delivered events, got %d", events)
	}
}
