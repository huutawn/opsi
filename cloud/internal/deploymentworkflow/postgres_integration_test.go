//go:build postgresintegration

package deploymentworkflow

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresDeploymentRunPersistsLeaseAndTimelineAcrossRestart(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run deployment workflow Postgres tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run deployment workflow Postgres tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, orgID, projectID := "user-workflow-"+suffix, "org-workflow-"+suffix, "project-workflow-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,'Workflow',$1)`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,org_id,name,slug,status,created_by) VALUES($1,$2,'Workflow',$1,'ready',$3)`, projectID, orgID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	run := Run{SchemaVersion: RunSchemaVersion, ID: "run-" + suffix, ProjectID: projectID, CreatedBy: userID, State: StateProvisioning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	event := Event{ID: "event-" + suffix, ProjectID: projectID, RunID: run.ID, State: run.State, Level: "info", Message: "started", CreatedAt: now}
	store := PostgresStore{DB: db}
	created, reused, err := store.Create(ctx, run, event, "create-"+suffix)
	if err != nil || reused || created.ID != run.ID {
		t.Fatalf("create run=%+v reused=%t err=%v", created, reused, err)
	}

	leased, ok, err := store.AcquireLease(ctx, projectID, run.ID, "worker-a", now, time.Minute)
	if err != nil || !ok || leased.ID != run.ID {
		t.Fatalf("lease run=%+v ok=%t err=%v", leased, ok, err)
	}
	if _, ok, err := store.AcquireLease(ctx, projectID, run.ID, "worker-b", now, time.Minute); err != nil || ok {
		t.Fatalf("concurrent lease ok=%t err=%v", ok, err)
	}
	if err := store.ReleaseLease(ctx, projectID, run.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}

	run.State = StateBuilding
	run.UpdatedAt = now.Add(time.Second)
	saved, err := store.Save(ctx, run, 1, Event{ID: "event-next-" + suffix, ProjectID: projectID, RunID: run.ID, State: run.State, Level: "info", Message: "building", CreatedAt: run.UpdatedAt})
	if err != nil || saved.Revision != 2 {
		t.Fatalf("save run=%+v err=%v", saved, err)
	}

	// A newly constructed store represents a restarted Cloud process.
	restarted := PostgresStore{DB: db}
	resumed, err := restarted.Get(ctx, projectID, run.ID)
	if err != nil || resumed.State != StateBuilding || resumed.Revision != 2 {
		t.Fatalf("resumed run=%+v err=%v", resumed, err)
	}
	events, err := restarted.Events(ctx, projectID, run.ID)
	if err != nil || len(events) != 2 || events[1].Message != "building" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	runnable, err := restarted.Runnable(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range runnable {
		found = found || candidate.ID == run.ID
	}
	if !found {
		t.Fatal("persisted building run was not resumable")
	}

	var wg sync.WaitGroup
	results := make(chan struct {
		run    Run
		reused bool
		err    error
	}, 2)
	for attempt := 1; attempt <= 2; attempt++ {
		wg.Add(1)
		go func(attempt int) {
			defer wg.Done()
			candidate := run
			candidate.ID = fmt.Sprintf("run-concurrent-%d-%s", attempt, suffix)
			candidate.Revision = 1
			candidate.State = StateProvisioning
			candidateEvent := event
			candidateEvent.ID = fmt.Sprintf("event-concurrent-%d-%s", attempt, suffix)
			candidateEvent.RunID = candidate.ID
			value, wasReused, createErr := (PostgresStore{DB: db}).Create(ctx, candidate, candidateEvent, "concurrent-"+suffix)
			results <- struct {
				run    Run
				reused bool
				err    error
			}{value, wasReused, createErr}
		}(attempt)
	}
	wg.Wait()
	close(results)
	createdCount, reusedCount, authoritativeID := 0, 0, ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if authoritativeID == "" {
			authoritativeID = result.run.ID
		} else if result.run.ID != authoritativeID {
			t.Fatalf("concurrent idempotency returned two runs: %s and %s", authoritativeID, result.run.ID)
		}
		if result.reused {
			reusedCount++
		} else {
			createdCount++
		}
	}
	if createdCount != 1 || reusedCount != 1 {
		t.Fatalf("created=%d reused=%d", createdCount, reusedCount)
	}
}
