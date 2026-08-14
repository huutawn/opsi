//go:build postgresintegration

package resource

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestPostgresRetainedStorageAtomicHandoffDestroyAndRecovery(t *testing.T) {
	db, registryStore, project, userID := newPostgresResourceFixture(t)
	ctx := context.Background()
	facts, err := registryStore.PlacementFacts(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := registryStore.UpsertNode(project.ID, "retained-node", "server", "healthy", "127.0.0.1", "", "retained-node-"+fmt.Sprint(time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := registryStore.RegisterAgent(project.ID, node.ID, "sha256:retained", "hash", "test", "retained-agent-"+fmt.Sprint(time.Now().UnixNano()), map[string]any{"managed_resources": true})
	if err != nil {
		t.Fatal(err)
	}
	store := PostgresStore{DB: db}
	service := Service{Store: store, Scopes: registryStore, Now: func() time.Time { return time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC) }}
	request := managedRequest(resourcev1.TypePostgres)
	request.EnvironmentID = facts.Environments[0].ID
	created, _, err := service.Create(ctx, project.ID, userID, "retained-resource", request)
	if err != nil {
		t.Fatal(err)
	}
	created.Lifecycle = resourcev1.LifecycleDeleting
	created.Runtime = &resourcev1.ManagedResourceRuntime{Spec: resourcev1.ManagedResourceSpec{
		ResourceID: created.ID, ProjectID: project.ID, EnvironmentID: created.EnvironmentID, ResourceType: resourcev1.TypePostgres,
		Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: facts.Runtimes[0].ID, NodeID: node.ID, AgentID: agent.ID}, Storage: resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault}, SpecHash: "spec-hash",
	}, DeleteActor: userID}
	if _, err := store.Update(ctx, created); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimManaged(ctx, project.ID, node.ID, "resource-delete-lease", service.clock(), service.clock().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claimed=%+v ok=%t err=%v", claimed, ok, err)
	}
	retained := resourcev1.RetainedStorage{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, ID: "rsto-" + fmt.Sprint(time.Now().UnixNano()), OriginalResourceID: created.ID,
		ProjectID: project.ID, EnvironmentID: created.EnvironmentID, ResourceType: resourcev1.TypePostgres, ResourceName: created.Name,
		Namespace: "opsi-retained", PVCName: "postgres-data", PVCUID: "pvc-uid", PVName: "pv-1", PVUID: "pv-uid", StorageClass: "local-path", ReclaimPolicy: "Delete",
		RequestedBytes: 1 << 30, ActualSize: "1Gi", StorageHash: "storage-hash", Assignment: claimed.Runtime.Spec.Assignment,
		Lifecycle: resourcev1.RetainedStorageRetained, Revision: 1, OriginalCreatedBy: userID, RetainedBy: userID, RetainedAt: service.clock(),
	}
	if err := store.RetainAndDeleteClaimed(ctx, claimed, retained, claimed.Runtime.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, project.ID, created.ID); err != ErrNotFound {
		t.Fatalf("resource survived atomic handoff: %v", err)
	}
	stored, err := store.GetRetainedStorage(ctx, project.ID, retained.ID)
	if err != nil || stored.PVCUID != retained.PVCUID || stored.PVUID != retained.PVUID || stored.RetainedBy != userID {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}

	review, err := service.ReviewRetainedStorageDestroy(ctx, project.ID, retained.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	reused := make(chan bool, 2)
	for range 2 {
		go func() {
			defer wg.Done()
			value, replay, err := service.RequestRetainedStorageDestroy(ctx, project.ID, retained.ID, userID, "destroy-postgres", resourcev1.DestroyRetainedStorageRequest{ReviewToken: review.ReviewToken})
			if err != nil || value.Lifecycle != resourcev1.RetainedStorageDestroying {
				t.Errorf("value=%+v replay=%t err=%v", value, replay, err)
			}
			reused <- replay
		}()
	}
	wg.Wait()
	close(reused)
	replayCount := 0
	for value := range reused {
		if value {
			replayCount++
		}
	}
	if replayCount != 1 {
		t.Fatalf("replay count=%d", replayCount)
	}
	first, ok, err := store.ClaimRetainedStorageDestroy(ctx, project.ID, node.ID, "destroy-lease-1", service.clock(), service.clock().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("first=%+v ok=%t err=%v", first, ok, err)
	}
	recovered, ok, err := store.ClaimRetainedStorageDestroy(ctx, project.ID, node.ID, "destroy-lease-2", service.clock().Add(time.Minute), service.clock().Add(2*time.Minute))
	if err != nil || !ok || recovered.LeaseToken != "destroy-lease-2" {
		t.Fatalf("recovered=%+v ok=%t err=%v", recovered, ok, err)
	}
	recovered.Revision++
	recovered.Lifecycle = resourcev1.RetainedStorageDestroyed
	destroyedAt := service.clock()
	recovered.DestroyedAt = &destroyedAt
	destroyed, err := store.UpdateRetainedStorageClaimed(ctx, recovered, recovered.LeaseToken)
	if err != nil || destroyed.Lifecycle != resourcev1.RetainedStorageDestroyed || destroyed.DestroyedAt == nil {
		t.Fatalf("destroyed=%+v err=%v", destroyed, err)
	}
}
