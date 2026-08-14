//go:build postgresintegration

package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestPostgresResourceStorePersistsReferencesAndIdempotency(t *testing.T) {
	db, registryStore, project, userID := newPostgresResourceFixture(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	facts, err := registryStore.PlacementFacts(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	application, err := registryStore.CreateService(project.ID, registry.ServiceDraft{Name: "api"}, "application-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: PostgresStore{DB: db}, Scopes: registryStore, Credentials: NewMemoryCredentialAuthority()}
	request := resourcev1.CreateRequest{EnvironmentID: facts.Environments[0].ID, Name: "postgres", Kind: resourcev1.KindManagedService, Type: resourcev1.TypePostgres, Managed: &resourcev1.ManagedSpec{
		Type: resourcev1.TypePostgres, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"},
	}}
	created, reused, err := service.Create(context.Background(), project.ID, userID, "resource-"+suffix, request)
	if err != nil || reused {
		t.Fatalf("created=%+v reused=%t err=%v", created, reused, err)
	}
	replay, reused, err := service.Create(context.Background(), project.ID, userID, "resource-"+suffix, request)
	if err != nil || !reused || replay.ID != created.ID {
		t.Fatalf("replay=%+v reused=%t err=%v", replay, reused, err)
	}
	created.Lifecycle = resourcev1.LifecycleReady
	spec := resourcev1.ManagedResourceSpec{ResourceType: resourcev1.TypePostgres, Image: resourcev1.PostgresImage, Replicas: 1, SpecHash: "ready", Connection: resourcev1.ManagedResourceConnection{Host: "postgres.internal", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"}}
	created.Runtime = &resourcev1.ManagedResourceRuntime{Spec: spec, Evidence: &resourcev1.ManagedResourceEvidence{ObservedSpecHash: spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, SecretReady: true, AuthReady: true, StorageReady: true, VolumeMounted: true, PVCName: "pvc", PVName: "pv", Image: spec.Image, ImageID: spec.Image, AvailableReplicas: 1}}
	if _, err := service.Store.Update(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	binding, reused, err := service.CreateBinding(context.Background(), project.ID, "binding-"+suffix, resourcev1.CreateBindingRequest{
		EnvironmentID: facts.Environments[0].ID, Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: application.ID},
		Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: created.ID}, Protocol: resourcev1.ProtocolPostgres, LogicalName: "DATABASE",
	})
	if err != nil || reused {
		t.Fatalf("binding=%+v reused=%t err=%v", binding, reused, err)
	}
	var stored, lifecycle, credentialID, roleName, database string
	if err := db.QueryRowContext(context.Background(), `SELECT runtime_references::text,lifecycle,credential_id,role_name,database_name FROM resource_bindings WHERE id=$1`, binding.ID).Scan(&stored, &lifecycle, &credentialID, &roleName, &database); err != nil {
		t.Fatal(err)
	}
	var references []resourcev1.RuntimeConnectionReference
	if err := json.Unmarshal([]byte(stored), &references); err != nil || len(references) != 6 || lifecycle != string(resourcev1.LifecycleProvisioning) || credentialID == "" || roleName == "" || database != "opsi" {
		t.Fatalf("stored references=%s lifecycle=%s credential=%s role=%s database=%s", stored, lifecycle, credentialID, roleName, database)
	}
}

func TestPostgresManagedResourceLeaseIsAtomicAndRecoversAfterExpiry(t *testing.T) {
	db, registryStore, project, userID := newPostgresResourceFixture(t)
	facts, err := registryStore.PlacementFacts(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := registryStore.UpsertNode(project.ID, "lease-node", "server", registry.NodeHealthy, "127.0.0.1", "", "lease-node")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := registryStore.RegisterAgent(project.ID, node.ID, "sha256:lease", "hash", "test", "lease-agent", map[string]any{"managed_resources": true})
	if err != nil {
		t.Fatal(err)
	}
	store := PostgresStore{DB: db}
	service := Service{Store: store, Scopes: registryStore}
	request := managedRequest(resourcev1.TypePostgres)
	request.EnvironmentID = facts.Environments[0].ID
	created, _, err := service.Create(context.Background(), project.ID, userID, "lease-resource", request)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), project.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	value.Lifecycle = resourcev1.LifecyclePlanned
	value.Runtime = &resourcev1.ManagedResourceRuntime{Spec: resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: value.ID, ProjectID: project.ID, EnvironmentID: facts.Environments[0].ID,
		ResourceType: resourcev1.TypePostgres, Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: facts.Runtimes[0].ID, NodeID: node.ID, AgentID: agent.ID}, TopologyRevision: 7, SpecHash: "lease-spec-hash",
	}}
	if _, err := store.Update(context.Background(), value); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 13, 6, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	wg.Add(2)
	claimed := make(chan resourcev1.Resource, 2)
	for attempt := range 2 {
		go func() {
			defer wg.Done()
			lease, ok, err := store.ClaimManaged(context.Background(), project.ID, node.ID, fmt.Sprintf("lease-%d", attempt), now, now.Add(2*time.Minute))
			if err != nil {
				t.Errorf("claim managed: %v", err)
			} else if ok {
				claimed <- lease
			}
		}()
	}
	wg.Wait()
	close(claimed)
	if len(claimed) != 1 {
		t.Fatalf("claimed leases=%d", len(claimed))
	}
	first := <-claimed
	if first.ID != created.ID || first.ProjectID != project.ID || first.EnvironmentID != facts.Environments[0].ID || first.Lifecycle != resourcev1.LifecycleProvisioning || first.Runtime == nil || first.Runtime.Spec.Assignment.NodeID != node.ID || first.Runtime.Spec.TopologyRevision != 7 || first.Runtime.LeaseToken == "" || !first.Runtime.LeaseExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("first lease=%+v", first)
	}

	recovered, ok, err := store.ClaimManaged(context.Background(), project.ID, node.ID, "lease-recovered", now.Add(2*time.Minute+time.Second), now.Add(4*time.Minute+time.Second))
	if err != nil || !ok || recovered.ID != created.ID || recovered.Lifecycle != resourcev1.LifecycleProvisioning || recovered.Runtime == nil || recovered.Runtime.Spec.TopologyRevision != 7 || recovered.Runtime.LeaseToken != "lease-recovered" || !recovered.Runtime.LeaseExpiresAt.Equal(now.Add(4*time.Minute+time.Second)) {
		t.Fatalf("recovered=%+v ok=%t err=%v", recovered, ok, err)
	}
}

func newPostgresResourceFixture(t *testing.T) (*sql.DB, registry.PostgresService, registry.Project, string) {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		message := "set OPSI_TEST_DATABASE_URL to run resource Postgres tests"
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal(message)
		}
		t.Skip(message)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
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
	return db, registryStore, project, userID
}
