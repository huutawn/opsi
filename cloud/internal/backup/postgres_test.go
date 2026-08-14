//go:build postgresintegration

package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestPostgresBackupAuthorityLifecycleRecoveryAndResourceIndependence(t *testing.T) {
	db, registryStore, projectID, environmentID, userID := backupPostgresFixture(t)
	ctx := context.Background()
	resourceStore := resource.PostgresStore{DB: db}
	resourceValue := readyPostgresResource(projectID, environmentID, userID)
	if _, _, err := resourceStore.Create(ctx, resourceValue, "resource-create", strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := resourceStore.Update(ctx, resourceValue); err != nil {
		t.Fatal(err)
	}
	store := PostgresStore{DB: db}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service := Service{Store: store, Resources: resource.Service{Store: resourceStore, Scopes: registryStore}, Artifacts: testArtifacts(), Now: func() time.Time { return now }}
	type createResult struct {
		value  backupv1.Backup
		reused bool
		err    error
	}
	createdResults := make(chan createResult, 2)
	var createWG sync.WaitGroup
	createWG.Add(2)
	for range 2 {
		go func() {
			defer createWG.Done()
			value, reused, err := service.Create(ctx, projectID, resourceValue.ID, userID, "backup-key")
			createdResults <- createResult{value: value, reused: reused, err: err}
		}()
	}
	createWG.Wait()
	close(createdResults)
	var created backupv1.Backup
	reusedCount := 0
	for result := range createdResults {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if created.ID == "" {
			created = result.value
		} else if result.value.ID != created.ID {
			t.Fatalf("same idempotency key created %s and %s", created.ID, result.value.ID)
		}
		if result.reused {
			reusedCount++
		}
	}
	if reusedCount != 1 {
		t.Fatalf("same-key reuse count=%d", reusedCount)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	for index := range 2 {
		go func() {
			defer wg.Done()
			_, _, err := service.Create(ctx, projectID, resourceValue.ID, userID, fmt.Sprintf("concurrent-%d", index))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if backupCode(err) != backupv1.FailureAlreadyRunning {
			t.Fatalf("concurrent err=%v", err)
		}
	}
	lease, ok, err := service.Lease(ctx, projectID, resourceValue.Runtime.Spec.Assignment.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	if _, err := service.Complete(ctx, projectID, created.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(leaseTTL - time.Minute)
	if _, err := service.Complete(ctx, projectID, created.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: lease.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if premature, ok, err := service.Lease(ctx, projectID, resourceValue.Runtime.Spec.Assignment.NodeID); err != nil || ok {
		t.Fatalf("heartbeat did not retain lease: lease=%+v ok=%t err=%v", premature, ok, err)
	}
	now = now.Add(leaseTTL)
	recovered, ok, err := service.Lease(ctx, projectID, resourceValue.Runtime.Spec.Assignment.NodeID)
	if err != nil || !ok || recovered.Backup.AttemptCount != 2 {
		t.Fatalf("recovered=%+v ok=%t err=%v", recovered, ok, err)
	}
	if _, err := service.Complete(ctx, projectID, created.ID, backupv1.Result{Status: backupv1.LifecycleRunning, LeaseToken: recovered.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	succeeded, err := service.Complete(ctx, projectID, created.ID, backupv1.Result{Status: backupv1.LifecycleSucceeded, LeaseToken: recovered.LeaseToken, SourcePostgresVersion: "18.6", PGDumpVersion: "pg_dump (PostgreSQL) 18.6", ArtifactSize: 256, SHA256: strings.Repeat("a", 64), ObjectETag: "etag-1", ObjectVersionID: "version-1", ArchiveVerified: true})
	if err != nil || succeeded.ValidateSucceeded() != nil {
		t.Fatalf("succeeded=%+v err=%v", succeeded, err)
	}
	if err := resourceStore.Delete(ctx, projectID, resourceValue.ID); err != nil {
		t.Fatal(err)
	}
	restarted := Service{Store: PostgresStore{DB: db}}
	loaded, err := restarted.Get(ctx, projectID, created.ID)
	if err != nil || loaded.SHA256 != succeeded.SHA256 || loaded.ObjectKey != succeeded.ObjectKey {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE backups SET object_key='mutable.dump' WHERE id=$1`, created.ID); err == nil {
		t.Fatal("succeeded backup artifact authority was mutable")
	}
}

func backupPostgresFixture(t *testing.T) (*sql.DB, registry.PostgresService, string, string, string) {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run backup Postgres tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run backup Postgres tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	userID, orgID := "user-backup-"+suffix, "org-backup-"+suffix
	if _, err := db.ExecContext(context.Background(), `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO organizations(id,name,slug) VALUES($1,'Backup',$2)`, orgID, orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})
	registryStore := registry.PostgresService{DB: db}
	project, err := registryStore.CreateProject(orgID, "Backup", "backup-"+suffix, userID, "project-backup-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := registryStore.PlacementFacts(context.Background(), project.ID)
	if err != nil || len(facts.Environments) == 0 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	return db, registryStore, project.ID, facts.Environments[0].ID, userID
}

func readyPostgresResource(projectID, environmentID, actor string) resourcev1.Resource {
	spec := readyResource("res-backup-postgres").Runtime.Spec
	spec.ProjectID, spec.EnvironmentID = projectID, environmentID
	spec.Assignment = resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-backup", NodeID: "node-backup", AgentID: "agent-backup"}
	spec.SpecHash, _ = spec.Hash()
	value := readyResource(spec.ResourceID)
	value.ProjectID, value.EnvironmentID, value.CreatedBy = projectID, environmentID, actor
	value.CreatedAt, value.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	value.Name, value.Provider = "postgres-backup", "opsi"
	value.Managed = &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres, Version: resourcev1.PostgresVersion, Profile: spec.Profile, Replicas: 1, CPUMillicores: spec.CPUMillicores, MemoryBytes: spec.MemoryBytes, Storage: spec.Storage, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}}
	value.Runtime.Spec = spec
	return value
}
