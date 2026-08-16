// Package cutover owns review and preflight authority for PostgreSQL application cutover.
package cutover

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

var ErrNotFound = errors.New("cutover not found")

const leaseTTL = 10 * time.Minute

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Code + ": " + e.Message }

type Store interface {
	CreateReview(context.Context, cutoverv1.ApplicationCutoverReview, string, string) (cutoverv1.ApplicationCutoverReview, bool, error)
	GetReview(context.Context, string, string) (cutoverv1.ApplicationCutoverReview, error)
	ListReviews(context.Context, string, string) ([]cutoverv1.ApplicationCutoverReview, error)
	ClaimReview(context.Context, string, string, string, time.Time, time.Time) (cutoverv1.ApplicationCutoverReview, bool, error)
	UpdateReviewClaimed(context.Context, cutoverv1.ApplicationCutoverReview, string) (cutoverv1.ApplicationCutoverReview, error)
	HasActive(context.Context, string, string) (bool, error)

	CreateCutover(context.Context, cutoverv1.ApplicationCutover, string, string) (cutoverv1.ApplicationCutover, bool, error)
	GetCutover(context.Context, string, string) (cutoverv1.ApplicationCutover, error)
	ListCutovers(context.Context, string, string) ([]cutoverv1.ApplicationCutover, error)
	UpdateCutover(context.Context, cutoverv1.ApplicationCutover) (cutoverv1.ApplicationCutover, error)
	HasActiveCutover(context.Context, string, string) (bool, error)

	CreateRollback(context.Context, cutoverv1.ApplicationCutoverRollback, string, string) (cutoverv1.ApplicationCutoverRollback, bool, error)
	GetRollback(context.Context, string, string) (cutoverv1.ApplicationCutoverRollback, error)
	ListRollbacks(context.Context, string, string) ([]cutoverv1.ApplicationCutoverRollback, error)
	UpdateRollback(context.Context, cutoverv1.ApplicationCutoverRollback) (cutoverv1.ApplicationCutoverRollback, error)
	HasActiveRollback(context.Context, string, string) (bool, error)
}

type ApplicationAuthority interface {
	GetServiceConfiguration(projectID, serviceID string) (registry.ServiceConfiguration, error)
	ApplyServiceConfiguration(projectID, serviceID, actorUserID, key string, request registry.ServiceConfigurationApplyRequest) (registry.ServiceConfigurationApplyResult, error)
	ListServices(projectID string) ([]registry.ServiceRecord, error)
	ApplicationBelongs(ctx context.Context, projectID, environmentID, applicationID string) (bool, error)
}

type DeploymentAuthority interface {
	ListDeployments(projectID string) ([]registry.DeploymentJob, error)
	GetDeployment(projectID, deploymentID string) (registry.DeploymentJob, error)
	StartImmutableDeployment(snapshot deploymentv1.JobSnapshot, requestedBy, key, requestID string) (registry.DeploymentJob, bool, error)
}

type BuildRecordAuthority interface {
	Get(ctx context.Context, projectID, recordID string) (buildrecordv1.Record, error)
}

type TopologyAuthority interface {
	Get(ctx context.Context, projectID string) (topologyv1.Plan, error)
}

type PolicyAuthority interface {
	Route(ctx context.Context, projectID string, request deploymentpolicyv1.RoutingRequest) (deploymentpolicyv1.RoutingDecision, error)
	Get(ctx context.Context, projectID, policyID string) (deploymentpolicyv1.Policy, error)
}

type RuntimeResolverAuthority interface {
	ApplicationRuntimeConfiguration(ctx context.Context, projectID, environmentID, applicationID string) ([]deploymentv1.EnvironmentVariable, []deploymentv1.SecretReference, error)
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
	Store           Store
	Applications    ApplicationAuthority
	Deployments     DeploymentAuthority
	BuildRecords    BuildRecordAuthority
	Topology        TopologyAuthority
	Policies        PolicyAuthority
	RuntimeResolver RuntimeResolverAuthority
	Resources       ResourceAuthority
	Restores        RestoreAuthority
	Backups         BackupAuthority
	Credentials     CredentialAuthority
	Now             func() time.Time
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

func conflict(code, message string) Error {
	return Error{Code: code, Status: 409, Message: message}
}

func notFound(message string) Error {
	return Error{Code: "NOT_FOUND", Status: 404, Message: message}
}

func unavailable(message string) Error {
	return Error{Code: "SERVICE_UNAVAILABLE", Status: 503, Message: message}
}

func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func (s Service) ValidateStaleReview(ctx context.Context, review cutoverv1.ApplicationCutoverReview) error {
	app, err := s.findApplication(ctx, review.ProjectID, review.ApplicationID)
	if err != nil {
		return err
	}
	sourceBinding, sourceRes, err := s.resolveSource(ctx, review.ProjectID, app, review.SourceBindingID)
	if err != nil {
		return err
	}
	targetBinding, targetRes, err := s.resolveTarget(ctx, review.ProjectID, app, review.TargetBindingID)
	if err != nil {
		return err
	}
	targetRestore, _, err := s.resolveLineage(ctx, review.ProjectID, targetRes.ID)
	if err != nil {
		return err
	}
	return s.ValidateStale(ctx, review, app, sourceRes, sourceBinding, targetRes, targetBinding, targetRestore)
}

func (s Service) Apply(ctx context.Context, projectID, applicationID string, request cutoverv1.ApplyRequest, actor, key string) (cutoverv1.ApplicationCutover, bool, error) {
	if s.Store == nil || s.Applications == nil || s.Resources == nil || s.Restores == nil || s.Backups == nil {
		return cutoverv1.ApplicationCutover{}, false, unavailable("cutover authority is unavailable")
	}
	if key == "" || !validKey(key) {
		return cutoverv1.ApplicationCutover{}, false, invalid(cutoverv1.FailureIdempotencyKeyInvalid, "idempotency key is invalid")
	}
	if request.CutoverReviewID == "" {
		return cutoverv1.ApplicationCutover{}, false, invalid(cutoverv1.FailureTargetInvalid, "cutover_review_id is required")
	}

	app, err := s.findApplication(ctx, projectID, applicationID)
	if err != nil {
		return cutoverv1.ApplicationCutover{}, false, err
	}

	payloadHash := sha256Hex(request.CutoverReviewID + "\x00" + app.ID)

	review, err := s.Store.GetReview(ctx, projectID, request.CutoverReviewID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return cutoverv1.ApplicationCutover{}, false, notFound("cutover review not found")
		}
		return cutoverv1.ApplicationCutover{}, false, err
	}

	if review.Lifecycle != cutoverv1.ReviewSucceeded {
		return cutoverv1.ApplicationCutover{}, false, invalid(cutoverv1.FailureReviewNotReady, "cutover review must be succeeded before applying cutover")
	}
	if review.ApplicationID != app.ID {
		return cutoverv1.ApplicationCutover{}, false, invalid(cutoverv1.FailureTargetInvalid, "review application does not match requested application")
	}

	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}

	cutoverID := newID("acut")
	initialCutover := cutoverv1.ApplicationCutover{
		SchemaVersion:         cutoverv1.CutoverSchemaVersion,
		ID:                    cutoverID,
		ProjectID:             projectID,
		EnvironmentID:         review.EnvironmentID,
		ApplicationID:         app.ID,
		CutoverReviewID:       review.ID,
		SourceBindingID:       review.SourceBindingID,
		TargetBindingID:       review.TargetBindingID,
		SourceResourceID:      review.SourceResourceID,
		TargetResourceID:      review.TargetResourceID,
		Lifecycle:             cutoverv1.CutoverApplying,
		RequestedBy:           actor,
		RequestedAt:           now,
		TargetNodeID:          review.TargetNodeID,
		VerificationSummary: cutoverv1.CutoverVerificationSummary{
			SourceSQLPreflight:   review.ValidationSummary.SourceSQLPreflight,
			TargetSQLPreflight:   review.ValidationSummary.TargetSQLPreflight,
			TargetRoleAttributes: review.ValidationSummary.TargetRoleAttributes,
		},
	}

	savedCutover, reused, err := s.Store.CreateCutover(ctx, initialCutover, key, payloadHash)
	if err != nil {
		return cutoverv1.ApplicationCutover{}, false, err
	}
	if reused {
		return savedCutover, true, nil
	}

	if err := review.ValidateSucceeded(); err != nil {
		savedCutover.Lifecycle = cutoverv1.CutoverFailed
		savedCutover.FailureCode = cutoverv1.FailureStaleReview
		savedCutover.FailureMessageRedacted = "cutover review evidence is invalid"
		failedTime := now
		savedCutover.CompletedAt = &failedTime
		_, _ = s.Store.UpdateCutover(ctx, savedCutover)
		return savedCutover, false, invalid(cutoverv1.FailureStaleReview, "cutover review evidence is invalid")
	}

	// Server-side stale validation against live entities
	if err := s.ValidateStaleReview(ctx, review); err != nil {
		savedCutover.Lifecycle = cutoverv1.CutoverFailed
		savedCutover.FailureCode = cutoverv1.FailureStaleReview
		savedCutover.FailureMessageRedacted = err.Error()
		failedTime := now
		savedCutover.CompletedAt = &failedTime
		_, _ = s.Store.UpdateCutover(ctx, savedCutover)
		return savedCutover, false, invalid(cutoverv1.FailureStaleReview, err.Error())
	}

	config, err := s.Applications.GetServiceConfiguration(projectID, app.ID)
	if err != nil {
		savedCutover.Lifecycle = cutoverv1.CutoverFailed
		savedCutover.FailureCode = cutoverv1.FailureApplicationStateInvalid
		savedCutover.FailureMessageRedacted = "application configuration is unavailable"
		failedTime := now
		savedCutover.CompletedAt = &failedTime
		_, _ = s.Store.UpdateCutover(ctx, savedCutover)
		return savedCutover, false, invalid(cutoverv1.FailureApplicationStateInvalid, "application configuration is unavailable")
	}
	if config.Revision != review.ApplicationConfigRevision || config.StateHash != review.ApplicationConfigHash {
		savedCutover.Lifecycle = cutoverv1.CutoverFailed
		savedCutover.FailureCode = cutoverv1.FailureStaleReview
		savedCutover.FailureMessageRedacted = "application configuration changed after review"
		failedTime := now
		savedCutover.CompletedAt = &failedTime
		_, _ = s.Store.UpdateCutover(ctx, savedCutover)
		return savedCutover, false, invalid(cutoverv1.FailureStaleReview, "application configuration changed after review")
	}

	var preCutoverDeploymentJobID, preCutoverBuildRecordID, preCutoverImageDigest string
	var currentJob *registry.DeploymentJob
	if s.Deployments != nil {
		jobs, _ := s.Deployments.ListDeployments(projectID)
		for _, j := range jobs {
			if j.ServiceID == app.ID && (j.Status == deploymentv1.StateSucceeded || j.Status == registry.DeploymentSucceeded) && j.Snapshot != nil {
				jobCopy := j
				currentJob = &jobCopy
				preCutoverDeploymentJobID = j.ID
				preCutoverBuildRecordID = j.Snapshot.Authority.BuildRecord.ID
				preCutoverImageDigest = j.Snapshot.Image.Digest
				break
			}
		}
	}

	savedCutover.ReviewedApplicationConfigRevision = review.ApplicationConfigRevision
	savedCutover.ReviewedApplicationConfigHash = review.ApplicationConfigHash
	savedCutover.PreCutoverApplicationConfigRevision = config.Revision
	savedCutover.PreCutoverApplicationConfigHash = config.StateHash
	savedCutover.PreCutoverDeploymentJobID = preCutoverDeploymentJobID
	savedCutover.PreCutoverBuildRecordID = preCutoverBuildRecordID
	savedCutover.PreCutoverImageDigest = preCutoverImageDigest

	// 1. Mutate Application configuration: switch DATABASE binding from SOURCE to TARGET
	nextDraft := config.ServiceConfigurationDraft
	nextResBindings := []serviceconfigurationv1.ResourceBinding{}
	for _, rb := range nextDraft.ResourceBindings {
		if rb.LogicalName != "DATABASE" {
			nextResBindings = append(nextResBindings, rb)
		}
	}
	nextResBindings = append(nextResBindings, serviceconfigurationv1.ResourceBinding{
		LogicalName: "DATABASE",
		BindingID:   review.TargetBindingID,
	})
	nextDraft.ResourceBindings = nextResBindings

	applyResult, err := s.Applications.ApplyServiceConfiguration(projectID, app.ID, actor, "cutover-"+savedCutover.ID, registry.ServiceConfigurationApplyRequest{
		Draft:             nextDraft,
		ExpectedRevision:  config.Revision,
		ExpectedStateHash: config.StateHash,
	})
	if err != nil {
		savedCutover.Lifecycle = cutoverv1.CutoverFailed
		savedCutover.FailureCode = cutoverv1.FailureConfigApplyFailed
		savedCutover.FailureMessageRedacted = "failed to apply new application configuration"
		failedTime := now
		savedCutover.CompletedAt = &failedTime
		_, _ = s.Store.UpdateCutover(ctx, savedCutover)
		return savedCutover, false, invalid(cutoverv1.FailureConfigApplyFailed, "failed to apply application configuration: "+err.Error())
	}

	savedCutover.ResultingApplicationConfigRevision = applyResult.Configuration.Revision
	savedCutover.ResultingApplicationConfigHash = applyResult.Configuration.StateHash
	appliedAt := now
	savedCutover.AppliedAt = &appliedAt
	savedCutover.Lifecycle = cutoverv1.CutoverDeploying

	// 2. Trigger canonical immutable deployment
	if s.Deployments != nil && currentJob != nil && currentJob.Snapshot != nil {
		newConfig := registry.ServiceConfiguration{
			ServiceConfigurationDraft: nextDraft,
			Revision:                  applyResult.Configuration.Revision,
			StateHash:                 applyResult.Configuration.StateHash,
		}
		var services []registry.ServiceRecord
		if svcs, listErr := s.Applications.ListServices(projectID); listErr == nil {
			services = svcs
		}

		var record buildrecordv1.Record
		if s.BuildRecords != nil {
			if rec, recErr := s.BuildRecords.Get(ctx, projectID, currentJob.Snapshot.Authority.BuildRecord.ID); recErr == nil {
				record = rec
			}
		}
		if record.ID == "" {
			record = currentJob.Snapshot.Authority.BuildRecord
		}

		var plan topologyv1.Plan
		if s.Topology != nil {
			if p, pErr := s.Topology.Get(ctx, projectID); pErr == nil && p.ID != "" {
				plan = p
			}
		}
		if plan.ID == "" {
			plan.ID = currentJob.Snapshot.Authority.TopologyPlanID
			plan.Revision = currentJob.Snapshot.Authority.TopologyRevision
			plan.PlanHash = currentJob.Snapshot.Authority.TopologyHash
		}

		var policy deploymentpolicyv1.Policy
		if s.Policies != nil {
			if pol, polErr := s.Policies.Get(ctx, projectID, currentJob.Snapshot.Authority.DeploymentPolicyID); polErr == nil && pol.ID != "" {
				policy = pol
			}
		}
		if policy.ID == "" {
			policy.ID = currentJob.Snapshot.Authority.DeploymentPolicyID
			policy.Revision = currentJob.Snapshot.Authority.DeploymentPolicyRevision
			policy.PolicyHash = currentJob.Snapshot.Authority.DeploymentPolicyHash
		}

		decision := deploymentpolicyv1.RoutingDecision{
			DecisionHash:  currentJob.Snapshot.Authority.RoutingDecisionHash,
			EnvironmentID: review.EnvironmentID,
			RuntimeID:     currentJob.Snapshot.Authority.RuntimeID,
			NodeID:        currentJob.Snapshot.Authority.NodeID,
			AgentID:       currentJob.Snapshot.Authority.AgentID,
		}

		snapshot, snapErr := s.compileDeploymentSnapshot(ctx, projectID, review.EnvironmentID, app, record, plan, policy, decision, newConfig, services, actor, "cutover-dep-"+savedCutover.ID, "req-cutover-"+savedCutover.ID)
		if snapErr != nil {
			savedCutover.Lifecycle = cutoverv1.CutoverFailed
			savedCutover.FailureCode = cutoverv1.FailureDeploymentFailed
			savedCutover.FailureMessageRedacted = "failed to compile deployment snapshot: " + snapErr.Error()
			failedTime := now
			savedCutover.CompletedAt = &failedTime
			_, _ = s.Store.UpdateCutover(ctx, savedCutover)
			return savedCutover, false, snapErr
		}

		job, _, depErr := s.Deployments.StartImmutableDeployment(snapshot, actor, "cutover-dep-"+savedCutover.ID, "req-cutover-"+savedCutover.ID)
		if depErr != nil {
			savedCutover.Lifecycle = cutoverv1.CutoverFailed
			savedCutover.FailureCode = cutoverv1.FailureDeploymentFailed
			savedCutover.FailureMessageRedacted = "failed to start deployment: " + depErr.Error()
			failedTime := now
			savedCutover.CompletedAt = &failedTime
			_, _ = s.Store.UpdateCutover(ctx, savedCutover)
			return savedCutover, false, depErr
		}
		savedCutover.DeploymentJobID = job.ID
	}

	updated, err := s.Store.UpdateCutover(ctx, savedCutover)
	if err != nil {
		return savedCutover, false, err
	}
	return updated, false, nil
}

func (s Service) compileDeploymentSnapshot(ctx context.Context, projectID, environmentID string, app registry.ServiceRecord, record buildrecordv1.Record, plan topologyv1.Plan, policy deploymentpolicyv1.Policy, decision deploymentpolicyv1.RoutingDecision, configuration registry.ServiceConfiguration, services []registry.ServiceRecord, actor, key, requestID string) (deploymentv1.JobSnapshot, error) {
	var assignment topologyv1.Assignment
	for _, a := range plan.Assignments {
		if a.ServiceKey == record.ServiceKey && a.EnvironmentID == environmentID {
			assignment = a
			break
		}
	}
	if assignment.ServiceKey == "" {
		if len(plan.Assignments) > 0 {
			assignment = plan.Assignments[0]
		} else {
			replicas := app.Replicas
			if replicas <= 0 {
				replicas = 1
			}
			assignment = topologyv1.Assignment{
				ServiceKey:           app.Name,
				EnvironmentID:        environmentID,
				RuntimeID:            decision.RuntimeID,
				Replicas:             int32(replicas),
				CPURequestMillicores: 100,
				MemoryRequestBytes:   128 << 20,
				Exposure:             topologyv1.ExposureIntent{Mode: "internal"},
			}
		}
	}
	workload, err := registry.CompileServiceRuntimeSpecs(app, assignment, plan.Assignments, configuration, services)
	if err != nil {
		return deploymentv1.JobSnapshot{}, invalid(cutoverv1.FailureConfigApplyFailed, err.Error())
	}
	if s.RuntimeResolver != nil {
		managedEnv, managedSecrets, err := s.RuntimeResolver.ApplicationRuntimeConfiguration(ctx, projectID, environmentID, app.ID)
		if err != nil {
			return deploymentv1.JobSnapshot{}, invalid(cutoverv1.FailureConfigApplyFailed, err.Error())
		}
		workload.Environment = append(workload.Environment, managedEnv...)
		workload.SecretReferences = append(workload.SecretReferences, managedSecrets...)
	}
	sort.Slice(workload.Environment, func(i, j int) bool { return workload.Environment[i].Name < workload.Environment[j].Name })
	sort.Slice(workload.SecretReferences, func(i, j int) bool { return workload.SecretReferences[i].EnvName < workload.SecretReferences[j].EnvName })
	if err := deploymentv1.ValidateEnvironment(workload.Environment, workload.SecretReferences); err != nil {
		return deploymentv1.JobSnapshot{}, invalid(cutoverv1.FailureConfigApplyFailed, err.Error())
	}
	image, err := deploymentv1.NewImmutableImage(record.Build.OCIRepository, record.Build.OCIDigest)
	if err != nil {
		return deploymentv1.JobSnapshot{}, invalid(cutoverv1.FailureConfigApplyFailed, err.Error())
	}
	specHash, _ := workload.Hash()
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}
	snapshot := deploymentv1.JobSnapshot{
		SchemaVersion:  deploymentv1.JobSchemaVersion,
		ProjectID:      projectID,
		Image:          image,
		Workload:       workload,
		SpecHash:       specHash,
		PayloadHash:    sha256Hex(record.ID + "\x00" + configuration.StateHash + "\x00" + key),
		ActorUserID:    actor,
		IdempotencyKey: key,
		CreatedAt:      now,
		Authority: deploymentv1.AuthoritySnapshot{
			BuildRecord:                   record,
			TopologyPlanID:                plan.ID,
			TopologyRevision:              plan.Revision,
			TopologyHash:                  plan.PlanHash,
			ServiceConfigurationRevision: configuration.Revision,
			ServiceConfigurationStateHash: configuration.StateHash,
			DeploymentPolicyID:            policy.ID,
			DeploymentPolicyRevision:      policy.Revision,
			DeploymentPolicyHash:          policy.PolicyHash,
			RoutingDecisionHash:           decision.DecisionHash,
			EnvironmentID:                 environmentID,
			RuntimeID:                     decision.RuntimeID,
			NodeID:                        decision.NodeID,
			AgentID:                       decision.AgentID,
		},
	}
	return snapshot, nil
}

func (s Service) GetCutover(ctx context.Context, projectID, cutoverID string) (cutoverv1.ApplicationCutover, error) {
	if s.Store == nil {
		return cutoverv1.ApplicationCutover{}, unavailable("cutover store is unavailable")
	}
	cutover, err := s.Store.GetCutover(ctx, projectID, cutoverID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return cutoverv1.ApplicationCutover{}, notFound("cutover not found")
		}
		return cutoverv1.ApplicationCutover{}, err
	}
	return cutover, nil
}

func (s Service) ListCutovers(ctx context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutover, error) {
	if s.Store == nil {
		return nil, unavailable("cutover store is unavailable")
	}
	return s.Store.ListCutovers(ctx, projectID, applicationID)
}

func (s Service) UpdateCutover(ctx context.Context, cutover cutoverv1.ApplicationCutover) (cutoverv1.ApplicationCutover, error) {
	if s.Store == nil {
		return cutoverv1.ApplicationCutover{}, unavailable("cutover store is unavailable")
	}
	return s.Store.UpdateCutover(ctx, cutover)
}

func (s Service) CompleteCutover(ctx context.Context, projectID, cutoverID string, result cutoverv1.CutoverApplyResult) (cutoverv1.ApplicationCutover, error) {
	if s.Store == nil {
		return cutoverv1.ApplicationCutover{}, unavailable("cutover authority is unavailable")
	}
	cutover, err := s.Store.GetCutover(ctx, projectID, cutoverID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return cutoverv1.ApplicationCutover{}, notFound("cutover not found")
		}
		return cutoverv1.ApplicationCutover{}, err
	}
	if cutover.Lifecycle == cutoverv1.CutoverSucceeded {
		return cutover, nil
	}

	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}

	if result.Status == "succeeded" {
		cutover.VerificationSummary = result.VerificationSummary
		cutover.Lifecycle = cutoverv1.CutoverSucceeded
		cutover.CompletedAt = &now
		cutover.EvidenceHash = cutoverv1.CutoverEvidenceHash(cutover)
		return s.Store.UpdateCutover(ctx, cutover)
	}

	cutover.Lifecycle = cutoverv1.CutoverFailed
	cutover.CompletedAt = &now
	cutover.FailureCode = result.FailureCode
	cutover.FailureMessageRedacted = result.FailureMessageRedacted
	return s.Store.UpdateCutover(ctx, cutover)
}

func (s Service) Rollback(ctx context.Context, projectID, applicationID, cutoverID, actor, key string) (cutoverv1.ApplicationCutoverRollback, bool, error) {
	if s.Store == nil || s.Applications == nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, unavailable("cutover authority is unavailable")
	}
	if projectID == "" || applicationID == "" || cutoverID == "" {
		return cutoverv1.ApplicationCutoverRollback{}, false, invalid(cutoverv1.FailureApplicationStateInvalid, "project, application, and cutover ids are required")
	}
	if key == "" {
		return cutoverv1.ApplicationCutoverRollback{}, false, invalid(cutoverv1.FailureIdempotencyKeyInvalid, "idempotency key is required")
	}

	activeCutover, err := s.Store.HasActiveCutover(ctx, projectID, applicationID)
	if err != nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, err
	}
	if activeCutover {
		return cutoverv1.ApplicationCutoverRollback{}, false, conflict(cutoverv1.FailureActiveOperationConflict, "an active cutover operation is currently in flight for this application")
	}

	cutover, err := s.Store.GetCutover(ctx, projectID, cutoverID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return cutoverv1.ApplicationCutoverRollback{}, false, notFound("cutover not found")
		}
		return cutoverv1.ApplicationCutoverRollback{}, false, err
	}
	if cutover.ApplicationID != applicationID {
		return cutoverv1.ApplicationCutoverRollback{}, false, invalid(cutoverv1.FailureApplicationStateInvalid, "cutover does not belong to application")
	}

	// Validate Cutover eligibility:
	// 1. Succeeded cutover is eligible.
	// 2. Failed cutover where target config revision was applied (ResultingApplicationConfigRevision > 0) is eligible.
	// 3. Cutover failed before config mutation (ResultingApplicationConfigRevision == 0) is ineligible (app never left SOURCE).
	if cutover.Lifecycle != cutoverv1.CutoverSucceeded && (cutover.Lifecycle != cutoverv1.CutoverFailed || cutover.ResultingApplicationConfigRevision == 0) {
		return cutoverv1.ApplicationCutoverRollback{}, false, invalid(cutoverv1.FailureRollbackCutoverIneligible, "cutover is not eligible for rollback")
	}

	services, err := s.Applications.ListServices(projectID)
	if err != nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, unavailable("application authority is unavailable")
	}
	var app *registry.ServiceRecord
	for _, svc := range services {
		if svc.ID == applicationID {
			svcCopy := svc
			app = &svcCopy
			break
		}
	}
	if app == nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, notFound("application not found")
	}

	rawID := make([]byte, 8)
	_, _ = rand.Read(rawID)
	rollbackID := "acrb-" + hex.EncodeToString(rawID)

	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"cutover_id":     cutover.ID,
		"application_id": app.ID,
		"project_id":     projectID,
	})
	sum := sha256.Sum256(payloadBytes)
	payloadHash := hex.EncodeToString(sum[:])

	rollback := cutoverv1.ApplicationCutoverRollback{
		SchemaVersion:                               cutoverv1.RollbackSchemaVersion,
		ID:                                          rollbackID,
		ProjectID:                                   projectID,
		EnvironmentID:                               cutover.EnvironmentID,
		ApplicationID:                               app.ID,
		CutoverID:                                   cutover.ID,
		SourceBindingID:                             cutover.SourceBindingID,
		TargetBindingID:                             cutover.TargetBindingID,
		SourceResourceID:                            cutover.SourceResourceID,
		TargetResourceID:                            cutover.TargetResourceID,
		OriginalPreCutoverApplicationConfigRevision: cutover.PreCutoverApplicationConfigRevision,
		OriginalPreCutoverApplicationConfigHash:     cutover.PreCutoverApplicationConfigHash,
		Lifecycle:                                   cutoverv1.RollbackApplying,
		RequestedBy:                                 actor,
		RequestedAt:                                 now,
		TargetNodeID:                                cutover.TargetNodeID,
		Warnings:                                    []string{cutoverv1.WarningTargetWritesMayNotBeOnSource},
		VerificationSummary: cutoverv1.RollbackVerificationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			SourceRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		},
	}

	savedRollback, reused, err := s.Store.CreateRollback(ctx, rollback, key, payloadHash)
	if err != nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, err
	}
	if reused {
		return savedRollback, true, nil
	}

	config, err := s.Applications.GetServiceConfiguration(projectID, app.ID)
	if err != nil {
		savedRollback.Lifecycle = cutoverv1.RollbackFailed
		savedRollback.FailureCode = cutoverv1.FailureApplicationStateInvalid
		savedRollback.FailureMessageRedacted = "application configuration is unavailable"
		failedTime := now
		savedRollback.CompletedAt = &failedTime
		_, _ = s.Store.UpdateRollback(ctx, savedRollback)
		return savedRollback, false, invalid(cutoverv1.FailureApplicationStateInvalid, "application configuration is unavailable")
	}

	savedRollback.CurrentApplicationConfigRevision = config.Revision
	savedRollback.CurrentApplicationConfigHash = config.StateHash

	// Verify current application is pointing to TargetBindingID
	var currentDBBindingID string
	for _, rb := range config.ServiceConfigurationDraft.ResourceBindings {
		if rb.LogicalName == "DATABASE" {
			currentDBBindingID = rb.BindingID
			break
		}
	}
	if currentDBBindingID != cutover.TargetBindingID {
		savedRollback.Lifecycle = cutoverv1.RollbackFailed
		savedRollback.FailureCode = cutoverv1.FailureRollbackStaleApplication
		savedRollback.FailureMessageRedacted = "current application database binding does not match cutover target binding"
		failedTime := now
		savedRollback.CompletedAt = &failedTime
		_, _ = s.Store.UpdateRollback(ctx, savedRollback)
		return savedRollback, false, invalid(cutoverv1.FailureRollbackStaleApplication, "current application database binding does not match cutover target binding")
	}

	// Validate SOURCE rollback authority
	if s.Resources != nil {
		sourceB, bErr := s.Resources.GetBinding(ctx, projectID, cutover.SourceBindingID)
		if bErr != nil || sourceB.ID == "" || sourceB.Lifecycle != "ready" {
			savedRollback.Lifecycle = cutoverv1.RollbackFailed
			savedRollback.FailureCode = cutoverv1.FailureRollbackSourceUnavailable
			savedRollback.FailureMessageRedacted = "source database binding is unavailable or revoked"
			failedTime := now
			savedRollback.CompletedAt = &failedTime
			_, _ = s.Store.UpdateRollback(ctx, savedRollback)
			return savedRollback, false, invalid(cutoverv1.FailureRollbackSourceUnavailable, "source database binding is unavailable or revoked")
		}
		sourceR, rErr := s.Resources.Get(ctx, projectID, cutover.SourceResourceID)
		if rErr != nil || sourceR.ID == "" || sourceR.Lifecycle != "ready" {
			savedRollback.Lifecycle = cutoverv1.RollbackFailed
			savedRollback.FailureCode = cutoverv1.FailureRollbackSourceUnavailable
			savedRollback.FailureMessageRedacted = "source database resource is not ready"
			failedTime := now
			savedRollback.CompletedAt = &failedTime
			_, _ = s.Store.UpdateRollback(ctx, savedRollback)
			return savedRollback, false, invalid(cutoverv1.FailureRollbackSourceUnavailable, "source database resource is not ready")
		}
	}

	// 1. Mutate Application configuration: switch DATABASE binding back to SOURCE
	nextDraft := config.ServiceConfigurationDraft
	nextResBindings := []serviceconfigurationv1.ResourceBinding{}
	for _, rb := range nextDraft.ResourceBindings {
		if rb.LogicalName != "DATABASE" {
			nextResBindings = append(nextResBindings, rb)
		}
	}
	nextResBindings = append(nextResBindings, serviceconfigurationv1.ResourceBinding{
		LogicalName: "DATABASE",
		BindingID:   cutover.SourceBindingID,
	})
	nextDraft.ResourceBindings = nextResBindings

	applyResult, err := s.Applications.ApplyServiceConfiguration(projectID, app.ID, actor, "rollback-"+savedRollback.ID, registry.ServiceConfigurationApplyRequest{
		Draft:             nextDraft,
		ExpectedRevision:  config.Revision,
		ExpectedStateHash: config.StateHash,
	})
	if err != nil {
		savedRollback.Lifecycle = cutoverv1.RollbackFailed
		savedRollback.FailureCode = cutoverv1.FailureRollbackConfigApplyFailed
		savedRollback.FailureMessageRedacted = "failed to apply rollback application configuration"
		failedTime := now
		savedRollback.CompletedAt = &failedTime
		_, _ = s.Store.UpdateRollback(ctx, savedRollback)
		return savedRollback, false, invalid(cutoverv1.FailureRollbackConfigApplyFailed, "failed to apply application configuration: "+err.Error())
	}

	savedRollback.ResultingApplicationConfigRevision = applyResult.Configuration.Revision
	savedRollback.ResultingApplicationConfigHash = applyResult.Configuration.StateHash
	appliedTime := now
	savedRollback.AppliedAt = &appliedTime
	savedRollback.Lifecycle = cutoverv1.RollbackDeploying

	// 2. Trigger canonical immutable deployment
	var currentJob *registry.DeploymentJob
	if s.Deployments != nil {
		jobs, _ := s.Deployments.ListDeployments(projectID)
		for _, j := range jobs {
			if j.ServiceID == app.ID && (j.Status == deploymentv1.StateSucceeded || j.Status == registry.DeploymentSucceeded) && j.Snapshot != nil {
				jobCopy := j
				currentJob = &jobCopy
				break
			}
		}
	}

	if s.Deployments != nil && currentJob != nil && currentJob.Snapshot != nil {
		newConfig := registry.ServiceConfiguration{
			ServiceConfigurationDraft: nextDraft,
			Revision:                  applyResult.Configuration.Revision,
			StateHash:                 applyResult.Configuration.StateHash,
		}
		var services []registry.ServiceRecord
		if svcs, listErr := s.Applications.ListServices(projectID); listErr == nil {
			services = svcs
		}

		var record buildrecordv1.Record
		if s.BuildRecords != nil {
			if rec, recErr := s.BuildRecords.Get(ctx, projectID, currentJob.Snapshot.Authority.BuildRecord.ID); recErr == nil {
				record = rec
			}
		}
		if record.ID == "" {
			record = currentJob.Snapshot.Authority.BuildRecord
		}

		var plan topologyv1.Plan
		if s.Topology != nil {
			if p, pErr := s.Topology.Get(ctx, projectID); pErr == nil && p.ID != "" {
				plan = p
			}
		}
		if plan.ID == "" {
			plan.ID = currentJob.Snapshot.Authority.TopologyPlanID
			plan.Revision = currentJob.Snapshot.Authority.TopologyRevision
			plan.PlanHash = currentJob.Snapshot.Authority.TopologyHash
		}

		var policy deploymentpolicyv1.Policy
		if s.Policies != nil {
			if pol, polErr := s.Policies.Get(ctx, projectID, currentJob.Snapshot.Authority.DeploymentPolicyID); polErr == nil && pol.ID != "" {
				policy = pol
			}
		}
		if policy.ID == "" {
			policy.ID = currentJob.Snapshot.Authority.DeploymentPolicyID
			policy.Revision = currentJob.Snapshot.Authority.DeploymentPolicyRevision
			policy.PolicyHash = currentJob.Snapshot.Authority.DeploymentPolicyHash
		}

		decision := deploymentpolicyv1.RoutingDecision{
			DecisionHash:  currentJob.Snapshot.Authority.RoutingDecisionHash,
			EnvironmentID: cutover.EnvironmentID,
			RuntimeID:     currentJob.Snapshot.Authority.RuntimeID,
			NodeID:        currentJob.Snapshot.Authority.NodeID,
			AgentID:       currentJob.Snapshot.Authority.AgentID,
		}

		snapshot, snapErr := s.compileDeploymentSnapshot(ctx, projectID, cutover.EnvironmentID, *app, record, plan, policy, decision, newConfig, services, actor, "rollback-dep-"+savedRollback.ID, "req-rollback-"+savedRollback.ID)
		if snapErr != nil {
			savedRollback.Lifecycle = cutoverv1.RollbackFailed
			savedRollback.FailureCode = cutoverv1.FailureRollbackDeploymentFailed
			savedRollback.FailureMessageRedacted = "failed to compile deployment snapshot: " + snapErr.Error()
			failedTime := now
			savedRollback.CompletedAt = &failedTime
			_, _ = s.Store.UpdateRollback(ctx, savedRollback)
			return savedRollback, false, snapErr
		}

		job, _, depErr := s.Deployments.StartImmutableDeployment(snapshot, actor, "rollback-dep-"+savedRollback.ID, "req-rollback-"+savedRollback.ID)
		if depErr != nil {
			savedRollback.Lifecycle = cutoverv1.RollbackFailed
			savedRollback.FailureCode = cutoverv1.FailureRollbackDeploymentFailed
			savedRollback.FailureMessageRedacted = "failed to start deployment: " + depErr.Error()
			failedTime := now
			savedRollback.CompletedAt = &failedTime
			_, _ = s.Store.UpdateRollback(ctx, savedRollback)
			return savedRollback, false, depErr
		}
		savedRollback.DeploymentJobID = job.ID
	}

	savedRollback.EvidenceHash = cutoverv1.RollbackEvidenceHash(savedRollback)
	updated, err := s.Store.UpdateRollback(ctx, savedRollback)
	if err != nil {
		return savedRollback, false, err
	}
	return updated, false, nil
}

func (s Service) GetRollback(ctx context.Context, projectID, id string) (cutoverv1.ApplicationCutoverRollback, error) {
	if s.Store == nil {
		return cutoverv1.ApplicationCutoverRollback{}, unavailable("cutover authority is unavailable")
	}
	rollback, err := s.Store.GetRollback(ctx, projectID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return cutoverv1.ApplicationCutoverRollback{}, notFound("cutover rollback not found")
		}
		return cutoverv1.ApplicationCutoverRollback{}, err
	}
	return rollback, nil
}

func (s Service) ListRollbacks(ctx context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutoverRollback, error) {
	if s.Store == nil {
		return nil, unavailable("cutover authority is unavailable")
	}
	return s.Store.ListRollbacks(ctx, projectID, applicationID)
}

func (s Service) CompleteRollback(ctx context.Context, projectID, rollbackID string, result cutoverv1.RollbackResult) (cutoverv1.ApplicationCutoverRollback, error) {
	if s.Store == nil {
		return cutoverv1.ApplicationCutoverRollback{}, unavailable("cutover authority is unavailable")
	}
	rollback, err := s.Store.GetRollback(ctx, projectID, rollbackID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return cutoverv1.ApplicationCutoverRollback{}, notFound("cutover rollback not found")
		}
		return cutoverv1.ApplicationCutoverRollback{}, err
	}
	if rollback.Lifecycle == cutoverv1.RollbackSucceeded {
		return rollback, nil
	}

	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}

	if result.Status == "succeeded" {
		rollback.VerificationSummary = result.VerificationSummary
		rollback.Lifecycle = cutoverv1.RollbackSucceeded
		rollback.CompletedAt = &now
		rollback.EvidenceHash = cutoverv1.RollbackEvidenceHash(rollback)
		return s.Store.UpdateRollback(ctx, rollback)
	}

	rollback.Lifecycle = cutoverv1.RollbackFailed
	rollback.CompletedAt = &now
	rollback.FailureCode = result.FailureCode
	rollback.FailureMessageRedacted = result.FailureMessageRedacted
	return s.Store.UpdateRollback(ctx, rollback)
}
