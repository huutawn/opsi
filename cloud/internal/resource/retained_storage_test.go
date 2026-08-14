package resource

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestRetainedStorageReviewedDestroyLifecycleAndIdempotency(t *testing.T) {
	service, store := retainedStorageTestService("Delete")
	ctx := context.Background()
	firstReview, err := service.ReviewRetainedStorageDestroy(ctx, "project-1", "rsto-1", "user-1")
	if err != nil || firstReview.ReviewToken == "" || firstReview.Revision != 2 || firstReview.Warning == "" {
		t.Fatalf("first review=%+v err=%v", firstReview, err)
	}
	secondReview, err := service.ReviewRetainedStorageDestroy(ctx, "project-1", "rsto-1", "user-1")
	if err != nil || secondReview.Revision != 3 || secondReview.ReviewToken == firstReview.ReviewToken {
		t.Fatalf("second review=%+v err=%v", secondReview, err)
	}
	if _, _, err := service.RequestRetainedStorageDestroy(ctx, "project-1", "rsto-1", "user-1", "destroy-stale", resourcev1.DestroyRetainedStorageRequest{ReviewToken: firstReview.ReviewToken}); err == nil || !strings.Contains(err.Error(), resourcev1.FailureRetainedStorageStaleReview) {
		t.Fatalf("stale review err=%v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan bool, 2)
	for range 2 {
		go func() {
			defer wg.Done()
			value, reused, err := service.RequestRetainedStorageDestroy(ctx, "project-1", "rsto-1", "user-1", "destroy-once", resourcev1.DestroyRetainedStorageRequest{ReviewToken: secondReview.ReviewToken})
			if err != nil || value.Lifecycle != resourcev1.RetainedStorageDestroying {
				t.Errorf("destroy value=%+v reused=%t err=%v", value, reused, err)
			}
			results <- reused
		}()
	}
	wg.Wait()
	close(results)
	reusedCount := 0
	for reused := range results {
		if reused {
			reusedCount++
		}
	}
	if reusedCount != 1 {
		t.Fatalf("reused requests=%d", reusedCount)
	}
	lease, ok, err := service.LeaseRetainedStorageDestroy(ctx, "project-1", "node-1")
	if err != nil || !ok || lease.Spec.PVCUID != "pvc-uid" || lease.Spec.Operation != "destroy" {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	destroyed, err := service.CompleteRetainedStorageDestroy(ctx, "project-1", "rsto-1", RetainedStorageResult{Status: "destroyed", LeaseToken: lease.LeaseToken, Evidence: &resourcev1.RetainedStorageDestroyEvidence{PVCAbsent: true, PVAbsent: true, ObservedAt: time.Now()}})
	if err != nil || destroyed.Lifecycle != resourcev1.RetainedStorageDestroyed || destroyed.DestroyedAt == nil {
		t.Fatalf("destroyed=%+v err=%v", destroyed, err)
	}
	if _, err := service.GetRetainedStorage(ctx, "project-1", "rsto-1"); err != nil {
		t.Fatalf("destroy tombstone missing: %v", err)
	}
	replayed, reused, err := service.RequestRetainedStorageDestroy(ctx, "project-1", "rsto-1", "user-1", "destroy-once", resourcev1.DestroyRetainedStorageRequest{ReviewToken: secondReview.ReviewToken})
	if err != nil || !reused || replayed.Lifecycle != resourcev1.RetainedStorageDestroyed {
		t.Fatalf("replayed=%+v reused=%t err=%v", replayed, reused, err)
	}
	if len(store.retained) != 1 {
		t.Fatalf("retained rows=%d", len(store.retained))
	}
}

func TestRetainedStorageDestroyRejectsRetainPolicyAndActiveReference(t *testing.T) {
	service, _ := retainedStorageTestService("Retain")
	review, err := service.ReviewRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RequestRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1", "retain-policy", resourcev1.DestroyRetainedStorageRequest{ReviewToken: review.ReviewToken}); err == nil || !strings.Contains(err.Error(), resourcev1.FailureStorageReclaimUnsupported) {
		t.Fatalf("retain policy err=%v", err)
	}

	service, store := retainedStorageTestService("Delete")
	store.resources["res-1"] = resourcev1.Resource{ID: "res-1", ProjectID: "project-1"}
	review, err = service.ReviewRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1")
	if err != nil || !review.ActiveResource {
		t.Fatalf("active review=%+v err=%v", review, err)
	}
	if _, _, err := service.RequestRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1", "active-reference", resourcev1.DestroyRetainedStorageRequest{ReviewToken: review.ReviewToken}); err == nil || !strings.Contains(err.Error(), resourcev1.FailureRetainedStorageActiveReference) {
		t.Fatalf("active reference err=%v", err)
	}
}

func TestRetainedStorageDestroyLeaseRecoversAfterExpiry(t *testing.T) {
	service, store := retainedStorageTestService("Delete")
	now := service.Now()
	review, _ := service.ReviewRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1")
	if _, _, err := service.RequestRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1", "restart", resourcev1.DestroyRetainedStorageRequest{ReviewToken: review.ReviewToken}); err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.ClaimRetainedStorageDestroy(context.Background(), "project-1", "node-1", "lease-1", now, now.Add(time.Minute))
	if err != nil || !ok || first.LeaseToken != "lease-1" {
		t.Fatalf("first=%+v ok=%t err=%v", first, ok, err)
	}
	if _, ok, err := store.ClaimRetainedStorageDestroy(context.Background(), "project-1", "node-1", "lease-2", now.Add(30*time.Second), now.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("premature claim ok=%t err=%v", ok, err)
	}
	recovered, ok, err := store.ClaimRetainedStorageDestroy(context.Background(), "project-1", "node-1", "lease-3", now.Add(time.Minute), now.Add(3*time.Minute))
	if err != nil || !ok || recovered.LeaseToken != "lease-3" || recovered.Lifecycle != resourcev1.RetainedStorageDestroying {
		t.Fatalf("recovered=%+v ok=%t err=%v", recovered, ok, err)
	}
}

func TestRetainedStorageReviewCreatesOneDestroyAuthority(t *testing.T) {
	service, store := retainedStorageTestService("Delete")
	review, err := service.ReviewRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	for _, key := range []string{"destroy-a", "destroy-b"} {
		go func() {
			defer wg.Done()
			_, _, err := service.RequestRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1", key, resourcev1.DestroyRetainedStorageRequest{ReviewToken: review.ReviewToken})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	succeeded, stale := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if strings.Contains(err.Error(), resourcev1.FailureRetainedStorageStaleReview) {
			stale++
		} else {
			t.Fatalf("unexpected destroy error: %v", err)
		}
	}
	if succeeded != 1 || stale != 1 || len(store.replays) != 1 {
		t.Fatalf("succeeded=%d stale=%d intents=%d", succeeded, stale, len(store.replays))
	}
	lease, ok, err := service.LeaseRetainedStorageDestroy(context.Background(), "project-1", "node-1")
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	if _, err := service.CompleteRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", RetainedStorageResult{Status: "failed", LeaseToken: lease.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RequestRetainedStorageDestroy(context.Background(), "project-1", "rsto-1", "user-1", "destroy-c", resourcev1.DestroyRetainedStorageRequest{ReviewToken: review.ReviewToken}); err == nil || !strings.Contains(err.Error(), resourcev1.FailureRetainedStorageStaleReview) {
		t.Fatalf("consumed review err=%v", err)
	}
}

func retainedStorageTestService(reclaimPolicy string) (Service, *MemoryStore) {
	store := NewMemoryStore()
	store.retained["rsto-1"] = resourcev1.RetainedStorage{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, ID: "rsto-1", OriginalResourceID: "res-1", ProjectID: "project-1", EnvironmentID: "env-1",
		ResourceType: resourcev1.TypePostgres, ResourceName: "postgres", Namespace: "opsi-project-1-env-1", PVCName: "postgres-data", PVCUID: "pvc-uid",
		PVName: "pv-1", PVUID: "pv-uid", StorageClass: "local-path", ReclaimPolicy: reclaimPolicy, RequestedBytes: 1 << 30, ActualSize: "1Gi",
		StorageHash: strings.Repeat("a", 64), Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Lifecycle: resourcev1.RetainedStorageRetained, Revision: 1, RetainedAt: time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC),
	}
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	return Service{Store: store, Now: func() time.Time { return now }}, store
}
