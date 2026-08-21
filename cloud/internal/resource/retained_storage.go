package resource

import (
	"context"
	"errors"
	"strings"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const retainedStorageWarning = "Permanently destroys the retained persistent volume. This cannot be undone, and backup/restore is not available."

type RetainedStorageLease struct {
	Spec       resourcev1.RetainedStorageDestroySpec `json:"spec"`
	LeaseToken string                                `json:"lease_token"`
}

type RetainedStorageResult struct {
	Status         string                                     `json:"status"`
	LeaseToken     string                                     `json:"lease_token"`
	Evidence       *resourcev1.RetainedStorageDestroyEvidence `json:"evidence,omitempty"`
	FailureCode    string                                     `json:"failure_code,omitempty"`
	FailureMessage string                                     `json:"failure_message_redacted,omitempty"`
}

func (s Service) GetRetainedStorage(ctx context.Context, projectID, id string) (resourcev1.RetainedStorage, error) {
	if s.Store == nil {
		return resourcev1.RetainedStorage{}, unavailable()
	}
	value, err := s.Store.GetRetainedStorage(ctx, projectID, id)
	if errors.Is(err, ErrNotFound) {
		return resourcev1.RetainedStorage{}, Error{Code: resourcev1.FailureRetainedStorageNotFound, Status: 404, Message: "retained storage was not found"}
	}
	return value, err
}

func (s Service) GetRetainedStorageByResource(ctx context.Context, projectID, resourceID string) (resourcev1.RetainedStorage, error) {
	if s.Store == nil {
		return resourcev1.RetainedStorage{}, unavailable()
	}
	value, err := s.Store.GetRetainedStorageByResource(ctx, projectID, resourceID)
	if errors.Is(err, ErrNotFound) {
		return resourcev1.RetainedStorage{}, Error{Code: resourcev1.FailureRetainedStorageNotFound, Status: 404, Message: "retained storage was not found"}
	}
	return value, err
}

func (s Service) ListRetainedStorage(ctx context.Context, projectID, environmentID string) ([]resourcev1.RetainedStorage, error) {
	if s.Store == nil {
		return nil, unavailable()
	}
	return s.Store.ListRetainedStorage(ctx, projectID, environmentID)
}

func (s Service) ReviewRetainedStorageDestroy(ctx context.Context, projectID, id, actor string) (resourcev1.RetainedStorageReview, error) {
	current, err := s.GetRetainedStorage(ctx, projectID, id)
	if err != nil {
		return resourcev1.RetainedStorageReview{}, err
	}
	if current.Lifecycle != resourcev1.RetainedStorageRetained && current.Lifecycle != resourcev1.RetainedStorageDestroyFailed && current.Lifecycle != resourcev1.RetainedStorageUnknown {
		return resourcev1.RetainedStorageReview{}, Error{Code: resourcev1.FailureRetainedStorageStaleReview, Status: 409, Message: "retained storage is not reviewable in its current lifecycle"}
	}
	now := s.clock()
	review := retainedStorageReview(current, current.Revision+1, now)
	review.ReviewToken = review.Hash()
	stored, activeResource, activeBinding, err := s.Store.SaveRetainedStorageReview(ctx, projectID, id, current.Revision, review.ReviewToken, actor, now)
	if err != nil {
		return resourcev1.RetainedStorageReview{}, err
	}
	review = retainedStorageReview(stored, stored.Revision, now)
	review.ActiveResource, review.ActiveBinding = activeResource, activeBinding
	review.ReviewToken = review.Hash()
	return review, nil
}

func (s Service) RequestRetainedStorageDestroy(ctx context.Context, projectID, id, actor, key string, request resourcev1.DestroyRetainedStorageRequest) (resourcev1.RetainedStorage, bool, error) {
	if !validKey(key) {
		return resourcev1.RetainedStorage{}, false, invalid("RETAINED_STORAGE_IDEMPOTENCY_KEY_INVALID", "idempotency key is invalid")
	}
	request.ReviewToken = strings.TrimSpace(request.ReviewToken)
	if len(request.ReviewToken) != 64 {
		return resourcev1.RetainedStorage{}, false, invalid(resourcev1.FailureRetainedStorageStaleReview, "destruction review token is invalid")
	}
	current, err := s.GetRetainedStorage(ctx, projectID, id)
	if err != nil {
		return resourcev1.RetainedStorage{}, false, err
	}
	if current.Lifecycle != resourcev1.RetainedStorageDestroyed && current.ReclaimPolicy != "Delete" {
		return resourcev1.RetainedStorage{}, false, Error{Code: resourcev1.FailureStorageReclaimUnsupported, Status: 409, Message: "retained storage reclaim policy is not supported for explicit destruction"}
	}
	return s.Store.RequestRetainedStorageDestroy(ctx, projectID, id, key, payloadHash(request), request.ReviewToken, actor, s.clock())
}

func (s Service) LeaseRetainedStorageDestroy(ctx context.Context, projectID, nodeID string) (RetainedStorageLease, bool, error) {
	now := s.clock()
	token := newID("rslease")
	value, ok, err := s.Store.ClaimRetainedStorageDestroy(ctx, projectID, nodeID, token, now, now.Add(managedLeaseTTL))
	if err != nil || !ok {
		return RetainedStorageLease{}, false, err
	}
	return RetainedStorageLease{LeaseToken: token, Spec: retainedStorageDestroySpec(value)}, true, nil
}

func (s Service) CompleteRetainedStorageDestroy(ctx context.Context, projectID, id string, result RetainedStorageResult) (resourcev1.RetainedStorage, error) {
	value, err := s.GetRetainedStorage(ctx, projectID, id)
	if err != nil {
		return resourcev1.RetainedStorage{}, err
	}
	if value.LeaseToken == "" || value.LeaseToken != result.LeaseToken {
		return resourcev1.RetainedStorage{}, invalid(resourcev1.FailureStorageDestroyFailed, "retained storage destruction lease is invalid")
	}
	value.FailureCode = strings.TrimSpace(result.FailureCode)
	value.FailureMessage = strings.TrimSpace(result.FailureMessage)
	if len(value.FailureMessage) > 512 {
		value.FailureMessage = value.FailureMessage[:512]
	}
	now := s.clock()
	switch {
	case result.Status == "destroyed" && result.Evidence != nil && result.Evidence.PVCAbsent && result.Evidence.PVAbsent:
		value.Lifecycle = resourcev1.RetainedStorageDestroyed
		value.FailureCode, value.FailureMessage = "", ""
		value.DestroyedAt = &now
	case result.Status == "failed":
		value.Lifecycle = resourcev1.RetainedStorageDestroyFailed
		if value.FailureCode == "" {
			value.FailureCode = resourcev1.FailureStorageDestroyFailed
		}
	default:
		value.Lifecycle = resourcev1.RetainedStorageUnknown
		if value.FailureCode == "" {
			value.FailureCode = resourcev1.FailureStorageDestroyFailed
		}
	}
	value.Revision++
	return s.Store.UpdateRetainedStorageClaimed(ctx, value, result.LeaseToken)
}

func retainedStorageReview(value resourcev1.RetainedStorage, revision uint64, now time.Time) resourcev1.RetainedStorageReview {
	return resourcev1.RetainedStorageReview{
		RetainedStorageID: value.ID, OriginalResourceID: value.OriginalResourceID, ResourceName: value.ResourceName,
		PVCName: value.PVCName, PVCUID: value.PVCUID, PVName: value.PVName, PVUID: value.PVUID,
		StorageClass: value.StorageClass, ReclaimPolicy: value.ReclaimPolicy, RequestedBytes: value.RequestedBytes,
		ActualSize: value.ActualSize, StorageHash: value.StorageHash, RetainedAt: value.RetainedAt,
		Revision: revision, Warning: retainedStorageWarning, ReviewedAt: now,
	}
}

func retainedStorageDestroySpec(value resourcev1.RetainedStorage) resourcev1.RetainedStorageDestroySpec {
	return resourcev1.RetainedStorageDestroySpec{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, RetainedStorageID: value.ID, OriginalResourceID: value.OriginalResourceID,
		ProjectID: value.ProjectID, EnvironmentID: value.EnvironmentID, ResourceType: value.ResourceType,
		Namespace: value.Namespace, PVCName: value.PVCName, PVCUID: value.PVCUID, PVName: value.PVName, PVUID: value.PVUID,
		StorageClass: value.StorageClass, ReclaimPolicy: value.ReclaimPolicy, StorageHash: value.StorageHash,
		Assignment: value.Assignment, Revision: value.Revision, Operation: "destroy",
	}
}

func retainedStorageFromDeletion(value resourcev1.Resource, evidence *resourcev1.ManagedResourceEvidence, now time.Time) (resourcev1.RetainedStorage, error) {
	if value.Runtime == nil || evidence == nil || !evidence.Deleted || !evidence.StorageRetained || evidence.ObservedSpecHash != value.Runtime.Spec.SpecHash || evidence.Namespace == "" || evidence.PVCName == "" || evidence.PVCUID == "" || evidence.PVName == "" || evidence.PVUID == "" || evidence.StorageClass == "" || evidence.ReclaimPolicy == "" || evidence.ActualStorage == "" || evidence.RequestedBytes != value.Runtime.Spec.Storage.SizeBytes || evidence.StorageHash != resourcev1.ManagedResourceStorageHash(value.Runtime.Spec) {
		return resourcev1.RetainedStorage{}, invalid(resourcev1.FailureRetainedStorageIdentityMismatch, "managed PostgreSQL delete did not prove exact retained storage identity")
	}
	return resourcev1.RetainedStorage{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, ID: newID("rsto"), OriginalResourceID: value.ID,
		ProjectID: value.ProjectID, EnvironmentID: value.EnvironmentID, ResourceType: value.Type, ResourceName: value.Name,
		Namespace: evidence.Namespace, PVCName: evidence.PVCName, PVCUID: evidence.PVCUID, PVName: evidence.PVName, PVUID: evidence.PVUID,
		StorageClass: evidence.StorageClass, ReclaimPolicy: evidence.ReclaimPolicy, RequestedBytes: evidence.RequestedBytes,
		ActualSize: evidence.ActualStorage, StorageHash: evidence.StorageHash, Assignment: value.Runtime.Spec.Assignment,
		Lifecycle: resourcev1.RetainedStorageRetained, Revision: 1, OriginalCreatedBy: value.CreatedBy,
		RetainedBy: value.Runtime.DeleteActor, RetainedAt: now,
	}, nil
}
