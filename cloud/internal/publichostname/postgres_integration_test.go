package publichostname_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	cloudpostgres "github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
)

func TestPostgresConcurrentQuotaAndGlobalUniqueness(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run public hostname PostgreSQL tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run public hostname PostgreSQL tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := cloudpostgres.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("ph-%d", time.Now().UnixNano())
	user1, user2, org := prefix+"-u1", prefix+"-u2", prefix+"-org"
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES($1,$2),($3,$4)`, user1, user1+"@example.test", user2, user2+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, org, org, org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM organizations WHERE id=$1`, org)
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1,$2)`, user1, user2)
	})
	type scope struct{ project, environment, runtime string }
	scopes := make([]scope, 20)
	for i := range scopes {
		scopes[i] = scope{prefix + fmt.Sprintf("-p%d", i), prefix + fmt.Sprintf("-e%d", i), prefix + fmt.Sprintf("-r%d", i)}
		if _, err := db.Exec(`INSERT INTO projects(id,org_id,name,slug,created_by) VALUES($1,$2,$1,$1,$3)`, scopes[i].project, org, user1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO environments(id,org_id,project_id,name,type) VALUES($1,$2,$3,'default','dev')`, scopes[i].environment, org, scopes[i].project); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO runtimes(id,org_id,project_id,environment_id,name) VALUES($1,$2,$3,$4,'default')`, scopes[i].runtime, org, scopes[i].project, scopes[i].environment); err != nil {
			t.Fatal(err)
		}
	}
	store := publichostname.PostgresStore{DB: db}
	service := publichostname.Service{Store: store, Limit: 3}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := range scopes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := service.Reserve(context.Background(), publichostname.ReserveRequest{Hostname: fmt.Sprintf("%s-%d.test.example.com", prefix, i), OwnerUserID: user1, ProjectID: scopes[i].project, EnvironmentID: scopes[i].environment, RuntimeID: scopes[i].runtime})
			if err == nil {
				success.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if success.Load() != 3 {
		t.Fatalf("successful reservations=%d", success.Load())
	}
	quota, err := service.Quota(t.Context(), user1)
	if err != nil || quota.Used != 3 {
		t.Fatalf("quota=%+v err=%v", quota, err)
	}
	if _, _, err := service.Reserve(t.Context(), publichostname.ReserveRequest{Hostname: quota.Allocations[0].Hostname, OwnerUserID: user2, ProjectID: scopes[10].project, EnvironmentID: scopes[10].environment, RuntimeID: scopes[10].runtime}); err == nil {
		t.Fatal("global duplicate hostname was accepted")
	}
}
