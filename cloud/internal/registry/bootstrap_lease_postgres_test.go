package registry

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresBootstrapLeaseIsAtomicAcrossWorkers(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "bootstrap lease")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("bootlease"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug) VALUES($1,'Bootstrap Lease',$2)`, orgID, "bootstrap-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	service := PostgresService{DB: db, Now: func() time.Time { return now }}
	project, err := service.CreateProject(orgID, "Bootstrap Lease", "bootstrap-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", userID, "boot-key", 22)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		lease BootstrapSessionLease
		ok    bool
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, workerID := range []string{"worker-1", "worker-2"} {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			lease, ok, err := (PostgresService{DB: db, Now: func() time.Time { return now }}).LeaseNextBootstrapSession(workerID, now, 15*time.Minute)
			results <- result{lease: lease, ok: ok, err: err}
		}(workerID)
	}
	wg.Wait()
	close(results)
	successes, empty := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.ok {
			empty++
			continue
		}
		successes++
		if result.lease.Session.ID != session.ID || result.lease.LeaseToken == "" {
			t.Fatalf("unexpected lease=%+v", result.lease)
		}
	}
	if successes != 1 || empty != 1 {
		t.Fatalf("successes=%d empty=%d", successes, empty)
	}
}

func TestPostgresBootstrapLeaseHeartbeatRetryDeadLetterSurvivesRestart(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "bootstrap heartbeat retry restart")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("bootdurable"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug) VALUES($1,'Bootstrap Durable',$2)`, orgID, "bootstrap-durable-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	repoA := PostgresService{DB: db, Now: func() time.Time { return now }}
	project, err := repoA.CreateProject(orgID, "Bootstrap Durable", "bootstrap-durable-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	session, err := repoA.CreateBootstrapSession(project.ID, "first_server", "203.0.113.20", "root", "password", userID, "boot-key", 22)
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := repoA.LeaseNextBootstrapSession("worker-a", now, 90*time.Second)
	if err != nil || !ok || first.Session.AttemptCount != 1 {
		t.Fatalf("first=%+v ok=%v err=%v", first, ok, err)
	}
	heartbeatAt := now.Add(20 * time.Second)
	renewed, err := repoA.RenewBootstrapLease(project.ID, session.ID, "worker-a", first.LeaseToken, heartbeatAt, 90*time.Second)
	if err != nil || renewed.LeaseHeartbeatAt == nil || renewed.AttemptCount != 1 {
		t.Fatalf("renewed=%+v err=%v", renewed, err)
	}
	recoverAt := renewed.LeaseExpiresAt.Add(time.Nanosecond)
	repoB := PostgresService{DB: db, Now: func() time.Time { return recoverAt }}
	summary, err := repoB.RecoverExpiredBootstrapLeases(recoverAt)
	if err != nil || len(summary.Recovered) != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	retryAt := *summary.Recovered[0].NextAttemptAt
	second, ok, err := (PostgresService{DB: db, Now: func() time.Time { return retryAt }}).LeaseNextBootstrapSession("worker-b", retryAt, 90*time.Second)
	if err != nil || !ok || second.Session.AttemptCount != 2 {
		t.Fatalf("second=%+v ok=%v err=%v", second, ok, err)
	}
	completed, err := (PostgresService{DB: db, Now: func() time.Time { return retryAt.Add(time.Second) }}).FinishBootstrapSessionForLease(project.ID, session.ID, "worker-b", second.LeaseToken, BootstrapFinishResult{Status: "completed"}, retryAt.Add(time.Second))
	if err != nil || completed.Status != "completed" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	repoC := PostgresService{DB: db, Now: func() time.Time { return retryAt.Add(2 * time.Second) }}
	persisted, err := repoC.GetBootstrapSession(project.ID, session.ID)
	if err != nil || persisted.Status != "completed" || persisted.AttemptCount != 2 || persisted.LeaseTokenHash != "" {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}

	deadSession, err := repoC.CreateBootstrapSession(project.ID, "first_server", "203.0.113.21", "root", "password", userID, "dead-key", 22)
	if err != nil {
		t.Fatal(err)
	}
	deadLease, ok, err := repoC.LeaseNextBootstrapSession("worker-c", retryAt.Add(2*time.Second), 90*time.Second)
	if err != nil || !ok || deadLease.Session.ID != deadSession.ID {
		t.Fatalf("dead lease=%+v ok=%v err=%v", deadLease, ok, err)
	}
	dead, err := repoC.FinishBootstrapSessionForLease(project.ID, deadSession.ID, "worker-c", deadLease.LeaseToken, BootstrapFinishResult{Status: "failed", FailureCode: "TARGET_OS_UNSUPPORTED", MessageRedacted: "unsupported target"}, retryAt.Add(3*time.Second))
	if err != nil || dead.Status != BootstrapDeadLetter || dead.DeadLetteredAt == nil {
		t.Fatalf("dead=%+v err=%v", dead, err)
	}
}

func TestPostgresBootstrapConcurrentRecoveryIsAtomic(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "bootstrap concurrent recovery")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("bootrecover"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug) VALUES($1,'Bootstrap Recover',$2)`, orgID, "bootstrap-recover-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	repo := PostgresService{DB: db, Now: func() time.Time { return now }}
	project, _ := repo.CreateProject(orgID, "Bootstrap Recover", "bootstrap-recover-"+suffix, userID, "project-key")
	session, _ := repo.CreateBootstrapSession(project.ID, "first_server", "203.0.113.30", "root", "password", userID, "boot-key", 22)
	lease, ok, err := repo.LeaseNextBootstrapSession("worker-a", now, time.Second)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	results := make(chan BootstrapRecoverySummary, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			summary, recoverErr := (PostgresService{DB: db}).RecoverExpiredBootstrapLeases(lease.LeaseExpiresAt)
			results <- summary
			errs <- recoverErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for recoverErr := range errs {
		if recoverErr != nil {
			t.Fatal(recoverErr)
		}
	}
	transitions := 0
	for summary := range results {
		transitions += len(summary.Recovered)
	}
	if transitions != 1 {
		t.Fatalf("recovery transitions=%d want=1", transitions)
	}
	stored, err := repo.GetBootstrapSession(project.ID, session.ID)
	if err != nil || stored.Status != BootstrapRetryWait || stored.AttemptCount != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}
