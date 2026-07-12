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
