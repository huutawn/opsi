// Package cutover owns review and preflight authority for PostgreSQL application cutover.
package cutover

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

var ErrNotFound = errors.New("cutover review not found")

const leaseTTL = 10 * time.Minute

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Message }

type Store interface {
	CreateReview(context.Context, cutoverv1.ApplicationCutoverReview, string, string) (cutoverv1.ApplicationCutoverReview, bool, error)
	GetReview(context.Context, string, string) (cutoverv1.ApplicationCutoverReview, error)
	ListReviews(context.Context, string, string) ([]cutoverv1.ApplicationCutoverReview, error)
	ClaimReview(context.Context, string, string, string, time.Time, time.Time) (cutoverv1.ApplicationCutoverReview, bool, error)
	UpdateReviewClaimed(context.Context, cutoverv1.ApplicationCutoverReview, string) (cutoverv1.ApplicationCutoverReview, error)
	HasActive(context.Context, string, string) (bool, error)
}

type ApplicationAuthority interface {
	GetServiceConfiguration(projectID, serviceID string) (registry.ServiceConfiguration, error)
	ListServices(projectID string) ([]registry.ServiceRecord, error)
	ApplicationBelongs(ctx context.Context, projectID, environmentID, applicationID string) (bool, error)
}

type ResourceAuthority interface {
	Get(context.Context, string, string) (resourcev1.Resource, error)
	GetBinding(context.Context, string, string) (resourcev1.Binding, error)
	ListBindings(context.Context, string, string) ([]resourcev1.Binding, error)
}

type RestoreAuthority interface {
	Get(context.Context, string, string) (restorev1.Restore, error)
	List(context.Context, string, string, string) ([]restorev1.Restore, error)
	HasActive(context.Context, string, string) (bool, error)
}

type BackupAuthority interface {
	Get(context.Context, string, string) (backupv1.Backup, error)
	HasActive(context.Context, string, string) (bool, error)
}

type CredentialAuthority interface {
	Get(context.Context, string) (resourcev1.ManagedResourceCredential, error)
}

type Service struct {
	Store        Store
	Applications ApplicationAuthority
	Resources    ResourceAuthority
	Restores     RestoreAuthority
	Backups      BackupAuthority
	Credentials  CredentialAuthority
	Now          func() time.Time
}

func (s Service) Review(ctx context.Context, projectID, applicationID string, request cutoverv1.ReviewRequest, actor, key string) (cutoverv1.ApplicationCutoverReview, bool, error) {
	if s.Store == nil || s.Applications == nil || s.Resources == nil || s.Restores == nil || s.Backups == nil {
		return cutoverv1.ApplicationCutoverReview{}, false, unavailable("cutover review authority is unavailable")
	}
	if key != "" && !validKey(key) {
		return cutoverv1.ApplicationCutoverReview{}, false, invalid(cutoverv1.FailureIdempotencyKeyInvalid, "idempotency key is invalid")
	}

	app, err := s.findApplication(ctx, projectID, applicationID)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, false, err
	}

	config, err := s.Applications.GetServiceConfiguration(projectID, app.ID)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, false, invalid(cutoverv1.FailureApplicationStateInvalid, "application configuration is unavailable")
	}

	sourceBinding, sourceResource, err := s.resolveSource(ctx, projectID, app, request.SourceBindingID)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, false, err
	}

	targetBinding, targetResource, err := s.resolveTarget(ctx, projectID, app, request.TargetBindingID)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, false, err
	}

	// Identity safety checks
	if sourceBinding.ID == targetBinding.ID {
		return cutoverv1.ApplicationCutoverReview{}, false, invalid(cutoverv1.FailureIdentityConflict, "source and target bindings must be distinct")
	}
	if sourceResource.ID == targetResource.ID {
		return cutoverv1.ApplicationCutoverReview{}, false, invalid(cutoverv1.FailureIdentityConflict, "source and target resources must be distinct")
	}
	if sourceBinding.CredentialID == targetBinding.CredentialID || (sourceBinding.RoleName != "" && sourceBinding.RoleName == targetBinding.RoleName) {
		return cutoverv1.ApplicationCutoverReview{}, false, invalid(cutoverv1.FailureIdentityConflict, "source and target binding credentials must be distinct")
	}

	// Active operation conflict checks
	if conflict, err := s.hasConflictingOperations(ctx, projectID, app.ID, sourceResource.ID, targetResource.ID); err != nil || conflict {
		if err != nil {
			return cutoverv1.ApplicationCutoverReview{}, false, err
		}
		return cutoverv1.ApplicationCutoverReview{}, false, Error{Code: cutoverv1.FailureActiveOperationConflict, Status: 409, Message: "conflicting active operations on application or target"}
	}

	// Restore lineage
	restore, backup, err := s.resolveLineage(ctx, projectID, targetResource.ID)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, false, err
	}

	now := s.clock()
	var backupCompletedAt, restoreCompletedAt *time.Time
	var backupAgeSeconds int64
	if backup.CompletedAt != nil {
		backupCompletedAt = backup.CompletedAt
		backupAgeSeconds = int64(now.Sub(*backup.CompletedAt).Seconds())
		if backupAgeSeconds < 0 {
			backupAgeSeconds = 0
		}
	}
	if restore.CompletedAt != nil {
		restoreCompletedAt = restore.CompletedAt
	}

	warnings := []string{cutoverv1.WarningNotContinuouslySynchronized}
	if backupAgeSeconds > 0 {
		warnings = append(warnings, cutoverv1.WarningBackupAgeNonZero)
	}

	validationSummary := cutoverv1.ValidationSummary{
		SourceBindingReady: sourceBinding.Lifecycle == resourcev1.LifecycleReady,
		TargetBindingReady: targetBinding.Lifecycle == resourcev1.LifecycleReady,
		TargetRestoreReady: restore.Lifecycle == restorev1.LifecycleSucceeded,
	}
	if targetResource.Runtime != nil && targetResource.Runtime.Evidence != nil {
		validationSummary.TargetPVCUID = targetResource.Runtime.Evidence.PVCUID
		validationSummary.TargetPVUID = targetResource.Runtime.Evidence.PVUID
		validationSummary.TargetStorageHash = targetResource.Runtime.Evidence.StorageHash
	}

	sourceBindingRev := cutoverv1.BindingRevision(sourceBinding)
	targetBindingRev := cutoverv1.BindingRevision(targetBinding)
	targetRestoreRev := cutoverv1.RestoreRevision(restore)

	integrityHashes := map[string]string{
		"application_config_hash": config.StateHash,
		"source_binding_hash":     sourceBindingRev,
		"target_binding_hash":     targetBindingRev,
		"source_spec_hash":        sourceResource.Runtime.Spec.SpecHash,
		"target_spec_hash":        targetResource.Runtime.Spec.SpecHash,
		"target_restore_revision": targetRestoreRev,
	}
	if targetResource.Runtime.Evidence != nil {
		integrityHashes["target_storage_hash"] = targetResource.Runtime.Evidence.StorageHash
	}

	review := cutoverv1.ApplicationCutoverReview{
		SchemaVersion:             cutoverv1.SchemaVersion,
		ID:                        newID("acrv"),
		ProjectID:                 projectID,
		EnvironmentID:             app.EnvironmentID,
		ApplicationID:             app.ID,
		SourceBindingID:           sourceBinding.ID,
		SourceResourceID:          sourceResource.ID,
		TargetResourceID:          targetResource.ID,
		TargetBindingID:           targetBinding.ID,
		ApplicationConfigRevision: config.Revision,
		ApplicationConfigHash:     config.StateHash,
		SourceBindingRevision:     sourceBindingRev,
		TargetBindingRevision:     targetBindingRev,
		SourceResourceRevision:    sourceResource.Runtime.Spec.TopologyRevision,
		SourceResourceSpecHash:    sourceResource.Runtime.Spec.SpecHash,
		TargetResourceRevision:    targetResource.Runtime.Spec.TopologyRevision,
		TargetResourceSpecHash:    targetResource.Runtime.Spec.SpecHash,
		TargetRestoreID:           restore.ID,
		TargetRestoreRevision:     targetRestoreRev,
		BackupID:                  backup.ID,
		BackupCompletedAt:         backupCompletedAt,
		RestoreCompletedAt:        restoreCompletedAt,
		BackupAgeSeconds:          backupAgeSeconds,
		ValidationSummary:         validationSummary,
		IntegrityHashes:           integrityHashes,
		Warnings:                  warnings,
		Lifecycle:                 cutoverv1.ReviewQueued,
		RequestedBy:               strings.TrimSpace(actor),
		RequestedAt:               now,
		TargetNodeID:              targetResource.Runtime.Spec.Assignment.NodeID,
	}

	payload := sha256.Sum256([]byte(applicationID + "\x00" + sourceBinding.ID + "\x00" + targetBinding.ID + "\x00" + restore.ID))
	return s.Store.CreateReview(ctx, review, key, hex.EncodeToString(payload[:]))
}

func (s Service) GetReview(ctx context.Context, projectID, reviewID string) (cutoverv1.ApplicationCutoverReview, error) {
	if s.Store == nil {
		return cutoverv1.ApplicationCutoverReview{}, unavailable("cutover review authority is unavailable")
	}
	return s.Store.GetReview(ctx, projectID, reviewID)
}

func (s Service) ListReviews(ctx context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutoverReview, error) {
	if s.Store == nil {
		return nil, unavailable("cutover review authority is unavailable")
	}
	return s.Store.ListReviews(ctx, projectID, applicationID)
}

func (s Service) LeaseReview(ctx context.Context, projectID, nodeID string) (cutoverv1.ReviewLease, bool, error) {
	if s.Store == nil || s.Resources == nil || s.Credentials == nil {
		return cutoverv1.ReviewLease{}, false, unavailable("cutover review authority is unavailable")
	}
	now, token := s.clock(), newID("crvlease")
	review, ok, err := s.Store.ClaimReview(ctx, projectID, nodeID, token, now, now.Add(leaseTTL))
	if err != nil || !ok {
		return cutoverv1.ReviewLease{}, false, err
	}

	sourceRes, err := s.Resources.Get(ctx, projectID, review.SourceResourceID)
	if err != nil || sourceRes.Runtime == nil {
		return s.failClaimedReview(ctx, review, token, cutoverv1.FailureSourceNotReady, "source resource authority unavailable")
	}
	targetRes, err := s.Resources.Get(ctx, projectID, review.TargetResourceID)
	if err != nil || targetRes.Runtime == nil {
		return s.failClaimedReview(ctx, review, token, cutoverv1.FailureTargetNotReady, "target resource authority unavailable")
	}
	sourceBinding, err := s.Resources.GetBinding(ctx, projectID, review.SourceBindingID)
	if err != nil {
		return s.failClaimedReview(ctx, review, token, cutoverv1.FailureSourceBindingInvalid, "source binding authority unavailable")
	}
	targetBinding, err := s.Resources.GetBinding(ctx, projectID, review.TargetBindingID)
	if err != nil {
		return s.failClaimedReview(ctx, review, token, cutoverv1.FailureTargetBindingInvalid, "target binding authority unavailable")
	}

	sourceCred, err := s.Credentials.Get(ctx, sourceBinding.CredentialID)
	if err != nil {
		return s.failClaimedReview(ctx, review, token, cutoverv1.FailureDatabaseUnavailable, "source credential unavailable")
	}
	targetCred, err := s.Credentials.Get(ctx, targetBinding.CredentialID)
	if err != nil {
		return s.failClaimedReview(ctx, review, token, cutoverv1.FailureDatabaseUnavailable, "target credential unavailable")
	}
	var targetMgmtCred *resourcev1.ManagedResourceCredential
	if targetRes.Runtime.Spec.CredentialID != "" {
		mgmt, err := s.Credentials.Get(ctx, targetRes.Runtime.Spec.CredentialID)
		if err == nil {
			targetMgmtCred = &mgmt
		}
	}

	lease := cutoverv1.ReviewLease{
		LeaseToken:                 token,
		Review:                     review,
		SourceSpec:                 sourceRes.Runtime.Spec,
		TargetSpec:                 targetRes.Runtime.Spec,
		SourceCredential:           &sourceCred,
		TargetCredential:           &targetCred,
		TargetManagementCredential: targetMgmtCred,
	}
	return lease, true, nil
}

func (s Service) failClaimedReview(ctx context.Context, review cutoverv1.ApplicationCutoverReview, token, code, message string) (cutoverv1.ReviewLease, bool, error) {
	now := s.clock()
	review.Lifecycle = cutoverv1.ReviewFailed
	review.FailureCode = code
	review.FailureMessageRedacted = message
	review.ReviewedAt = &now
	updated, updateErr := s.Store.UpdateReviewClaimed(ctx, review, token)
	return cutoverv1.ReviewLease{Review: updated}, false, updateErr
}

func (s Service) CompleteReview(ctx context.Context, projectID, reviewID string, result cutoverv1.ReviewResult) (cutoverv1.ApplicationCutoverReview, error) {
	if s.Store == nil {
		return cutoverv1.ApplicationCutoverReview{}, unavailable("cutover review authority is unavailable")
	}
	current, err := s.Store.GetReview(ctx, projectID, reviewID)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, err
	}
	if current.Lifecycle != cutoverv1.ReviewLeased {
		return current, invalid(cutoverv1.FailureLeaseLost, "cutover review is not leased")
	}
	now := s.clock()
	current.ReviewedAt = &now
	if result.Status == cutoverv1.ReviewSucceeded {
		current.ValidationSummary = result.ValidationSummary
		current.ValidationSummary.SourceSQLPreflight = result.SourceSQLPreflight
		current.ValidationSummary.TargetSQLPreflight = result.TargetSQLPreflight
		current.ValidationSummary.TargetRoleAttributes = result.TargetRoleAttributes
		current.ValidationSummary.SourceBindingReady = true
		current.ValidationSummary.TargetBindingReady = true
		current.ValidationSummary.TargetRestoreReady = true
		current.EvidenceHash = cutoverv1.EvidenceHash(current)
		current.Lifecycle = cutoverv1.ReviewSucceeded
		current.FailureCode = ""
		current.FailureMessageRedacted = ""
	} else {
		current.Lifecycle = cutoverv1.ReviewFailed
		current.FailureCode = result.FailureCode
		current.FailureMessageRedacted = result.FailureMessageRedacted
		if current.FailureCode == "" {
			current.FailureCode = cutoverv1.FailureDatabaseUnavailable
		}
	}
	return s.Store.UpdateReviewClaimed(ctx, current, result.LeaseToken)
}

func (s Service) ValidateStale(ctx context.Context, review cutoverv1.ApplicationCutoverReview, app registry.ServiceRecord, sourceRes resourcev1.Resource, sourceBinding resourcev1.Binding, targetRes resourcev1.Resource, targetBinding resourcev1.Binding, targetRestore restorev1.Restore) error {
	if err := review.ValidateSucceeded(); err != nil {
		return invalid(cutoverv1.FailureStaleReview, "review evidence is invalid")
	}
	config, err := s.Applications.GetServiceConfiguration(app.ProjectID, app.ID)
	if err != nil || config.Revision != review.ApplicationConfigRevision || config.StateHash != review.ApplicationConfigHash {
		return invalid(cutoverv1.FailureStaleReview, "application configuration changed after review")
	}
	if sourceRes.Lifecycle != resourcev1.LifecycleReady || sourceRes.Runtime == nil || sourceRes.Runtime.Spec.SpecHash != review.SourceResourceSpecHash {
		return invalid(cutoverv1.FailureStaleReview, "source resource changed or is not ready")
	}
	if sourceBinding.Lifecycle != resourcev1.LifecycleReady || cutoverv1.BindingRevision(sourceBinding) != review.SourceBindingRevision {
		return invalid(cutoverv1.FailureStaleReview, "source binding changed or is not ready")
	}
	if targetRes.Lifecycle != resourcev1.LifecycleReady || targetRes.Runtime == nil || targetRes.Runtime.Spec.SpecHash != review.TargetResourceSpecHash {
		return invalid(cutoverv1.FailureStaleReview, "target resource changed or is not ready")
	}
	if targetBinding.Lifecycle != resourcev1.LifecycleReady || cutoverv1.BindingRevision(targetBinding) != review.TargetBindingRevision {
		return invalid(cutoverv1.FailureStaleReview, "target binding changed or is not ready")
	}
	if targetRestore.ID != review.TargetRestoreID || targetRestore.Lifecycle != restorev1.LifecycleSucceeded || cutoverv1.RestoreRevision(targetRestore) != review.TargetRestoreRevision {
		return invalid(cutoverv1.FailureStaleReview, "target restore authority changed or is not succeeded")
	}
	return nil
}

func (s Service) findApplication(ctx context.Context, projectID, applicationID string) (registry.ServiceRecord, error) {
	services, err := s.Applications.ListServices(projectID)
	if err != nil {
		return registry.ServiceRecord{}, err
	}
	for _, service := range services {
		if service.ID == applicationID || service.Name == applicationID {
			belongs, err := s.Applications.ApplicationBelongs(ctx, projectID, service.EnvironmentID, service.ID)
			if err != nil {
				return registry.ServiceRecord{}, err
			}
			if !belongs {
				return registry.ServiceRecord{}, invalid(cutoverv1.FailureEnvironmentMismatch, "application environment mismatch")
			}
			return service, nil
		}
	}
	return registry.ServiceRecord{}, Error{Code: "APPLICATION_NOT_FOUND", Status: 404, Message: "application not found"}
}

func (s Service) resolveSource(ctx context.Context, projectID string, app registry.ServiceRecord, bindingID string) (resourcev1.Binding, resourcev1.Resource, error) {
	var sourceBinding resourcev1.Binding
	if bindingID != "" {
		b, err := s.Resources.GetBinding(ctx, projectID, bindingID)
		if err != nil {
			return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureSourceBindingInvalid, "source binding not found")
		}
		if b.Source.ID != app.ID || b.Protocol != resourcev1.ProtocolPostgres || b.LogicalName != "DATABASE" || b.Lifecycle != resourcev1.LifecycleReady {
			return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureSourceBindingInvalid, "source binding is invalid for application")
		}
		sourceBinding = b
	} else {
		bindings, err := s.Resources.ListBindings(ctx, projectID, app.EnvironmentID)
		if err != nil {
			return resourcev1.Binding{}, resourcev1.Resource{}, err
		}
		found := false
		for _, b := range bindings {
			if b.Source.ID == app.ID && b.Protocol == resourcev1.ProtocolPostgres && b.LogicalName == "DATABASE" && b.Lifecycle == resourcev1.LifecycleReady {
				sourceBinding = b
				found = true
				break
			}
		}
		if !found {
			return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureSourceBindingInvalid, "no active source PostgreSQL binding found for application")
		}
	}

	res, err := s.Resources.Get(ctx, projectID, sourceBinding.Target.ID)
	if err != nil {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureSourceInvalid, "source resource not found")
	}
	if res.EnvironmentID != app.EnvironmentID || res.Type != resourcev1.TypePostgres || res.Lifecycle != resourcev1.LifecycleReady || res.Runtime == nil || !factualReady(res.Runtime.Spec, res.Runtime.Evidence) {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureSourceNotReady, "source PostgreSQL resource is not ready")
	}
	return sourceBinding, res, nil
}

func (s Service) resolveTarget(ctx context.Context, projectID string, app registry.ServiceRecord, bindingID string) (resourcev1.Binding, resourcev1.Resource, error) {
	if bindingID == "" {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureTargetBindingInvalid, "target binding ID is required")
	}
	targetBinding, err := s.Resources.GetBinding(ctx, projectID, bindingID)
	if err != nil {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureTargetBindingInvalid, "target binding not found")
	}
	if targetBinding.EnvironmentID != app.EnvironmentID || targetBinding.Source.ID != app.ID || targetBinding.Protocol != resourcev1.ProtocolPostgres || targetBinding.LogicalName != "DATABASE" || targetBinding.Lifecycle != resourcev1.LifecycleReady {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureTargetBindingInvalid, "target binding is invalid or belongs to different application")
	}
	if targetBinding.RoleName == "" || targetBinding.CredentialID == "" || targetBinding.Database == "" {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureTargetBindingInvalid, "target binding credential authority is incomplete")
	}

	targetResource, err := s.Resources.Get(ctx, projectID, targetBinding.Target.ID)
	if err != nil {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureTargetInvalid, "target resource not found")
	}
	if targetResource.EnvironmentID != app.EnvironmentID || targetResource.Type != resourcev1.TypePostgres || targetResource.Lifecycle != resourcev1.LifecycleReady || targetResource.Runtime == nil || !factualReady(targetResource.Runtime.Spec, targetResource.Runtime.Evidence) {
		return resourcev1.Binding{}, resourcev1.Resource{}, invalid(cutoverv1.FailureTargetNotReady, "target PostgreSQL resource is not ready")
	}
	return targetBinding, targetResource, nil
}

func (s Service) resolveLineage(ctx context.Context, projectID, targetResourceID string) (restorev1.Restore, backupv1.Backup, error) {
	restores, err := s.Restores.List(ctx, projectID, "", targetResourceID)
	if err != nil {
		return restorev1.Restore{}, backupv1.Backup{}, err
	}
	var succeededRestore *restorev1.Restore
	for i := range restores {
		if restores[i].Lifecycle == restorev1.LifecycleSucceeded && restores[i].ValidateSucceeded() == nil {
			succeededRestore = &restores[i]
			break
		}
	}
	if succeededRestore == nil {
		return restorev1.Restore{}, backupv1.Backup{}, invalid(cutoverv1.FailureTargetRestoreNotSucceeded, "target resource does not have a succeeded restore authority")
	}

	backup, err := s.Backups.Get(ctx, projectID, succeededRestore.BackupID)
	if err != nil || backup.Lifecycle != backupv1.LifecycleSucceeded {
		return restorev1.Restore{}, backupv1.Backup{}, invalid(cutoverv1.FailureTargetRestoreNotSucceeded, "backup authority for restore is not succeeded")
	}
	return *succeededRestore, backup, nil
}

func (s Service) hasConflictingOperations(ctx context.Context, projectID, appID, sourceID, targetID string) (bool, error) {
	activeTargetRestore, err := s.Restores.HasActive(ctx, projectID, targetID)
	if err != nil {
		return false, err
	}
	if activeTargetRestore {
		return true, nil
	}
	activeSourceRestore, err := s.Restores.HasActive(ctx, projectID, sourceID)
	if err != nil {
		return false, err
	}
	if activeSourceRestore {
		return true, nil
	}
	return false, nil
}

func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func factualReady(spec resourcev1.ManagedResourceSpec, evidence *resourcev1.ManagedResourceEvidence) bool {
	if evidence == nil {
		return false
	}
	storageOK := evidence.StorageReady && evidence.VolumeMounted && evidence.PVCUID != "" && evidence.PVUID != "" && evidence.StorageHash == resourcev1.ManagedResourceStorageHash(spec)
	return evidence.ObservedSpecHash == spec.SpecHash && evidence.WorkloadReady && evidence.PodReady && evidence.ServiceReady && evidence.SecretReady && evidence.AuthReady && storageOK && !evidence.Deleted
}

func validKey(key string) bool {
	if len(key) == 0 || len(key) > 256 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' && r != ':' {
			return false
		}
	}
	return true
}

func newID(prefix string) string {
	bytes := make([]byte, 12)
	_, _ = rand.Read(bytes)
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(bytes))
}

func invalid(code, message string) Error {
	return Error{Code: code, Status: 400, Message: message}
}

func unavailable(message string) Error {
	return Error{Code: "SERVICE_UNAVAILABLE", Status: 503, Message: message}
}
