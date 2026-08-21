package svcatalog

import (
	"context"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestRetainedStorageDestroyVerifiesIdentityAndIsIdempotent(t *testing.T) {
	spec, credential := postgresSpec(t)
	runner := &postgresRunner{objects: map[string]map[string]any{}}
	reconciler := ManagedResourceReconciler{Runner: runner, Timeout: time.Second, PollInterval: time.Millisecond}
	ready := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "apply", Spec: spec, Credential: credential})
	if ready.Status != "ready" {
		t.Fatalf("ready=%+v", ready)
	}
	retained := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "retain", Spec: spec})
	if retained.Status != "deleted" || retained.Evidence == nil || retained.Evidence.PVCUID == "" || retained.Evidence.PVUID == "" || retained.Evidence.ReclaimPolicy != "Delete" {
		t.Fatalf("retained=%+v", retained)
	}
	destroySpec := destroySpecFromEvidence(spec, retained.Evidence)
	destroyed := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "destroy", Spec: destroySpec})
	if destroyed.Status != "destroyed" || destroyed.Evidence == nil || !destroyed.Evidence.PVCAbsent || !destroyed.Evidence.PVAbsent || runner.objects["persistentvolumeclaim/"+destroySpec.PVCName] != nil || runner.objects["persistentvolume/"+destroySpec.PVName] != nil {
		t.Fatalf("destroyed=%+v objects=%v", destroyed, runner.objects)
	}
	replayed := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "destroy-replay", Spec: destroySpec})
	if replayed.Status != "destroyed" || replayed.Evidence == nil || !replayed.Evidence.PVCAbsent || !replayed.Evidence.PVAbsent {
		t.Fatalf("replayed=%+v", replayed)
	}
}

func TestRetainedStorageDestroyRejectsRetainAndOwnershipMismatch(t *testing.T) {
	spec, credential := postgresSpec(t)
	runner := &postgresRunner{objects: map[string]map[string]any{}}
	reconciler := ManagedResourceReconciler{Runner: runner, Timeout: time.Second, PollInterval: time.Millisecond}
	if ready := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "apply", Spec: spec, Credential: credential}); ready.Status != "ready" {
		t.Fatalf("ready=%+v", ready)
	}
	retained := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "retain", Spec: spec})
	destroySpec := destroySpecFromEvidence(spec, retained.Evidence)
	destroySpec.ReclaimPolicy = "Retain"
	unsupported := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "unsupported", Spec: destroySpec})
	if unsupported.Status != "failed" || unsupported.FailureCode != resourcev1.FailureStorageReclaimUnsupported || runner.objects["persistentvolumeclaim/"+destroySpec.PVCName] == nil {
		t.Fatalf("unsupported=%+v", unsupported)
	}
	destroySpec.ReclaimPolicy = "Delete"
	pvc := runner.objects["persistentvolumeclaim/"+destroySpec.PVCName]
	pvc["metadata"].(map[string]any)["labels"].(map[string]any)["opsi.dev/project"] = "unrelated"
	mismatch := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "mismatch", Spec: destroySpec})
	if mismatch.Status != "failed" || mismatch.FailureCode != resourcev1.FailureRetainedStorageOwnership || runner.objects["persistentvolumeclaim/"+destroySpec.PVCName] == nil {
		t.Fatalf("mismatch=%+v", mismatch)
	}
}

func TestRetainedStorageDestroyRecoversWhenPVCAndPVAlreadyGone(t *testing.T) {
	spec, credential := postgresSpec(t)
	runner := &postgresRunner{objects: map[string]map[string]any{}}
	reconciler := ManagedResourceReconciler{Runner: runner, Timeout: time.Second, PollInterval: time.Millisecond}
	if ready := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "apply", Spec: spec, Credential: credential}); ready.Status != "ready" {
		t.Fatalf("ready=%+v", ready)
	}
	retained := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "retain", Spec: spec})
	destroySpec := destroySpecFromEvidence(spec, retained.Evidence)
	delete(runner.objects, "persistentvolumeclaim/"+destroySpec.PVCName)
	delete(runner.objects, "persistentvolume/"+destroySpec.PVName)
	recovered := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "recovered", Spec: destroySpec})
	if recovered.Status != "destroyed" || recovered.Evidence == nil || !recovered.Evidence.PVCAbsent || !recovered.Evidence.PVAbsent {
		t.Fatalf("recovered=%+v", recovered)
	}
}

func destroySpecFromEvidence(spec resourcev1.ManagedResourceSpec, evidence *resourcev1.ManagedResourceEvidence) resourcev1.RetainedStorageDestroySpec {
	return resourcev1.RetainedStorageDestroySpec{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, RetainedStorageID: "rsto-1", OriginalResourceID: spec.ResourceID,
		ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, ResourceType: spec.ResourceType, Namespace: evidence.Namespace,
		PVCName: evidence.PVCName, PVCUID: evidence.PVCUID, PVName: evidence.PVName, PVUID: evidence.PVUID,
		StorageClass: evidence.StorageClass, ReclaimPolicy: evidence.ReclaimPolicy, StorageHash: evidence.StorageHash,
		Assignment: spec.Assignment, Revision: 2, Operation: "destroy",
	}
}
