package svcatalog

import (
	"context"
	"errors"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func (r ManagedResourceReconciler) ReconcileRetainedStorage(ctx context.Context, lease cloudrelay.RetainedStorageLease) cloudrelay.RetainedStorageResult {
	result := cloudrelay.RetainedStorageResult{LeaseToken: lease.LeaseToken}
	if err := lease.Spec.Validate(); err != nil {
		result.Status, result.FailureCode, result.FailureMessageRedacted = "failed", resourcev1.FailureRetainedStorageIdentityMismatch, err.Error()
		return result
	}
	if lease.Spec.ReclaimPolicy != "Delete" {
		result.Status, result.FailureCode, result.FailureMessageRedacted = "failed", resourcev1.FailureStorageReclaimUnsupported, "retained storage reclaim policy is unsupported"
		return result
	}
	evidence, status, err := r.destroyRetainedStorage(ctx, lease.Spec)
	result.Status, result.Evidence = status, evidence
	if err != nil {
		result.FailureCode, result.FailureMessageRedacted = failureCodeOr(err, resourcev1.FailureStorageDestroyFailed), err.Error()
	}
	return result
}

func (r ManagedResourceReconciler) destroyRetainedStorage(ctx context.Context, spec resourcev1.RetainedStorageDestroySpec) (*resourcev1.RetainedStorageDestroyEvidence, string, error) {
	pvc, err := r.get(ctx, "persistentvolumeclaim", spec.PVCName, spec.Namespace)
	if err != nil {
		return nil, "unknown", err
	}
	if pvc != nil {
		if err := retainedPVCMatches(pvc, spec); err != nil {
			return nil, "failed", err
		}
		pv, err := r.get(ctx, "persistentvolume", spec.PVName, "")
		if err != nil {
			return nil, "unknown", err
		}
		if err := retainedPVMatches(pv, spec); err != nil {
			return nil, "failed", err
		}
		if _, err := r.run(ctx, nil, "delete", "persistentvolumeclaim", spec.PVCName, "-n", spec.Namespace, "--wait=true", "--timeout=2m", "--ignore-not-found"); err != nil {
			return nil, "failed", managedResourceError{resourcev1.FailureStorageDeleteFailed, "Kubernetes PVC deletion failed"}
		}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	interval := r.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		pvc, pvcErr := r.get(ctx, "persistentvolumeclaim", spec.PVCName, spec.Namespace)
		pv, pvErr := r.get(ctx, "persistentvolume", spec.PVName, "")
		if pvcErr != nil || pvErr != nil {
			return nil, "unknown", errors.Join(pvcErr, pvErr)
		}
		if pvc != nil {
			if err := retainedPVCMatches(pvc, spec); err != nil {
				return nil, "failed", err
			}
		}
		if pv != nil {
			if err := retainedPVMatches(pv, spec); err != nil {
				return nil, "failed", err
			}
		}
		if pvc == nil && pv == nil {
			return &resourcev1.RetainedStorageDestroyEvidence{PVCAbsent: true, PVAbsent: true, ObservedAt: time.Now().UTC()}, "destroyed", nil
		}
		select {
		case <-ctx.Done():
			return nil, "unknown", ctx.Err()
		case <-deadline.C:
			return nil, "failed", managedResourceError{resourcev1.FailureStorageReclaimTimeout, "Kubernetes did not prove persistent volume reclamation before timeout"}
		case <-ticker.C:
		}
	}
}

func retainedPVCMatches(pvc map[string]any, spec resourcev1.RetainedStorageDestroySpec) error {
	identity := resourcev1.ManagedResourceSpec{ResourceID: spec.OriginalResourceID, ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, ResourceType: spec.ResourceType}
	name, _ := nested(pvc, "metadata", "name").(string)
	uid, _ := nested(pvc, "metadata", "uid").(string)
	namespace, _ := nested(pvc, "metadata", "namespace").(string)
	storageClass, _ := nested(pvc, "spec", "storageClassName").(string)
	annotations := stringMap(nested(pvc, "metadata", "annotations"))
	if name != spec.PVCName || uid != spec.PVCUID || namespace != spec.Namespace {
		return managedResourceError{resourcev1.FailureRetainedStorageIdentityMismatch, "retained PVC identity changed"}
	}
	if !exactManagedResourceOwnership(pvc, identity) || storageClass != spec.StorageClass || annotations["opsi.dev/storage-hash"] != spec.StorageHash {
		return managedResourceError{resourcev1.FailureRetainedStorageOwnership, "retained PVC ownership does not match destruction authority"}
	}
	return nil
}

func retainedPVMatches(pv map[string]any, spec resourcev1.RetainedStorageDestroySpec) error {
	if pv == nil {
		return managedResourceError{resourcev1.FailureRetainedStorageIdentityMismatch, "expected retained PV is absent while PVC still exists"}
	}
	name, _ := nested(pv, "metadata", "name").(string)
	uid, _ := nested(pv, "metadata", "uid").(string)
	claimName, _ := nested(pv, "spec", "claimRef", "name").(string)
	claimNamespace, _ := nested(pv, "spec", "claimRef", "namespace").(string)
	claimUID, _ := nested(pv, "spec", "claimRef", "uid").(string)
	storageClass, _ := nested(pv, "spec", "storageClassName").(string)
	reclaimPolicy, _ := nested(pv, "spec", "persistentVolumeReclaimPolicy").(string)
	if name != spec.PVName || spec.PVUID != "" && uid != spec.PVUID || claimName != spec.PVCName || claimNamespace != spec.Namespace || claimUID != spec.PVCUID {
		return managedResourceError{resourcev1.FailureRetainedStorageIdentityMismatch, "retained PV claim identity changed"}
	}
	if storageClass != spec.StorageClass || reclaimPolicy != spec.ReclaimPolicy {
		return managedResourceError{resourcev1.FailureRetainedStorageOwnership, "retained PV storage authority changed"}
	}
	return nil
}
