//go:build postgresintegration

package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestPostgresResourceStorePersistsReferencesAndIdempotency(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set OPSI_TEST_DATABASE_URL to run resource Postgres tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UTC().Format("150405000000000")
	userID, orgID := "user-p07a-"+suffix, "org-p07a-"+suffix
	if _, err := db.ExecContext(context.Background(), `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO organizations(id,name,slug) VALUES($1,'P07A',$2)`, orgID, orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})
	registryStore := registry.PostgresService{DB: db}
	project, err := registryStore.CreateProject(orgID, "P07A", "p07a-"+suffix, userID, "project-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DELETE FROM projects WHERE id=$1`, project.ID) })
	facts, err := registryStore.PlacementFacts(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	application, err := registryStore.CreateService(project.ID, registry.ServiceDraft{Name: "api"}, "application-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: PostgresStore{DB: db}, Scopes: registryStore}
	request := resourcev1.CreateRequest{EnvironmentID: facts.Environments[0].ID, Name: "postgres", Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres, Managed: &resourcev1.ManagedSpec{
		Type: resourcev1.TypePostgres, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30},
		CredentialRefs: []resourcev1.SecretReference{{SecretID: "vault-postgres"}}, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"},
	}}
	created, reused, err := service.Create(context.Background(), project.ID, userID, "resource-"+suffix, request)
	if err != nil || reused {
		t.Fatalf("created=%+v reused=%t err=%v", created, reused, err)
	}
	replay, reused, err := service.Create(context.Background(), project.ID, userID, "resource-"+suffix, request)
	if err != nil || !reused || replay.ID != created.ID {
		t.Fatalf("replay=%+v reused=%t err=%v", replay, reused, err)
	}
	binding, reused, err := service.CreateBinding(context.Background(), project.ID, "binding-"+suffix, resourcev1.CreateBindingRequest{
		EnvironmentID: facts.Environments[0].ID, Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: application.ID},
		Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: created.ID}, Protocol: resourcev1.ProtocolPostgres, LogicalName: "DATABASE",
	})
	if err != nil || reused {
		t.Fatalf("binding=%+v reused=%t err=%v", binding, reused, err)
	}
	var stored string
	if err := db.QueryRowContext(context.Background(), `SELECT runtime_references::text FROM resource_bindings WHERE id=$1`, binding.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var references []resourcev1.RuntimeConnectionReference
	if err := json.Unmarshal([]byte(stored), &references); err != nil || len(references) == 0 {
		t.Fatalf("stored references=%s", stored)
	}
	for _, reference := range references {
		if reference.Sensitivity == resourcev1.ValueSecret && (reference.Value != "" || reference.SecretRef == nil) {
			t.Fatalf("plaintext secret reference persisted: %+v", reference)
		}
	}
}
