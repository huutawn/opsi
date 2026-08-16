// Package restore owns review and restore-to-new-PostgreSQL authority.
package restore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	backupdomain "github.com/opsi-dev/opsi/cloud/internal/backup"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

var ErrNotFound = errors.New("restore not found")

const leaseTTL = 10 * time.Minute

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Message }

type Store interface {
	CreateReview(context.Context, restorev1.Review) (restorev1.Review, error)
	GetReview(context.Context, string, string) (restorev1.Review, error)
	ClaimReview(context.Context, string, string, string, time.Time, time.Time) (restorev1.Review, bool, error)
	UpdateReviewClaimed(context.Context, restorev1.Review, string) (restorev1.Review, error)
	Create(context.Context, restorev1.Restore, string, string) (restorev1.Restore, bool, error)
	Get(context.Context, string, string) (restorev1.Restore, error)
	List(context.Context, string, string, string) ([]restorev1.Restore, error)
	Claim(context.Context, string, string, string, time.Time, time.Time) (restorev1.Restore, bool, error)
	UpdateClaimed(context.Context, restorev1.Restore, string) (restorev1.Restore, error)
	HasActive(context.Context, string, string) (bool, error)
}

type BackupAuthority interface {
	Get(context.Context, string, string) (backupv1.Backup, error)
}
type ResourceAuthority interface {
	Get(context.Context, string, string) (resourcev1.Resource, error)
	ListBindings(context.Context, string, string) ([]resourcev1.Binding, error)
}

type Service struct {
	Store     Store
	Backups   BackupAuthority
	Resources ResourceAuthority
	Artifacts backupdomain.StoreAuthority
	Now       func() time.Time
}

func (s Service) Review(ctx context.Context, projectID, backupID, targetID, actor string) (restorev1.Review, error) {
	backup, target, err := s.authorities(ctx, projectID, backupID, targetID)
	if err != nil {
		return restorev1.Review{}, err
	}
	if err := s.validateTarget(ctx, backup, target); err != nil {
		return restorev1.Review{}, err
	}
	now := s.clock()
	review := restorev1.Review{
		SchemaVersion: restorev1.SchemaVersion, ID: newID("rrv"), ProjectID: projectID, EnvironmentID: target.EnvironmentID,
		BackupID: backup.ID, BackupCreatedAt: backup.CreatedAt, BackupArtifactSHA256: backup.SHA256, BackupRevision: restorev1.BackupRevision(backup),
		SourceResourceID: backup.SourceResourceID, SourcePostgresVersion: backup.SourcePostgresVersion, ArtifactSize: backup.ArtifactSize,
		TargetResourceID: target.ID, TargetNodeID: target.Runtime.Spec.Assignment.NodeID, TargetPostgresVersion: target.Runtime.Spec.Version, TargetDatabase: target.Runtime.Spec.Connection.Database,
		TargetLifecycle: target.Lifecycle, TargetSpecRevision: target.Runtime.Spec.TopologyRevision, TargetSpecHash: target.Runtime.Spec.SpecHash,
		TargetPVCName: target.Runtime.Evidence.PVCName, TargetPVCUID: target.Runtime.Evidence.PVCUID, TargetPVName: target.Runtime.Evidence.PVName, TargetPVUID: target.Runtime.Evidence.PVUID, TargetStorageHash: target.Runtime.Evidence.StorageHash,
		Warning: "The target database will be populated from this backup; in-place restore and binding changes are not performed.", Lifecycle: restorev1.ReviewQueued,
		RequestedBy: strings.TrimSpace(actor), RequestedAt: now,
	}
	return s.Store.CreateReview(ctx, review)
}

func (s Service) GetReview(ctx context.Context, projectID, reviewID string) (restorev1.Review, error) {
	if s.Store == nil {
		return restorev1.Review{}, unavailable("restore authority is unavailable")
	}
	return s.Store.GetReview(ctx, projectID, reviewID)
}

func (s Service) Create(ctx context.Context, projectID, backupID, targetID, reviewID, actor, key string) (restorev1.Restore, bool, error) {
	if !validKey(key) {
		return restorev1.Restore{}, false, invalid("RESTORE_IDEMPOTENCY_KEY_INVALID", "idempotency key is invalid")
	}
	backup, target, err := s.authorities(ctx, projectID, backupID, targetID)
	if err != nil {
		return restorev1.Restore{}, false, err
	}
	if err := s.validateTarget(ctx, backup, target); err != nil {
		return restorev1.Restore{}, false, err
	}
	review, err := s.Store.GetReview(ctx, projectID, reviewID)
	if err != nil || review.ValidateSucceeded() != nil || stale(review, backup, target) {
		return restorev1.Restore{}, false, invalid(restorev1.FailureStaleReview, "restore review is stale or invalid")
	}
	now := s.clock()
	value := restorev1.Restore{
		SchemaVersion: restorev1.SchemaVersion, ID: newID("rst"), ProjectID: projectID, EnvironmentID: target.EnvironmentID, ReviewID: review.ID,
		BackupID: backup.ID, BackupRevision: restorev1.BackupRevision(backup), SourceResourceID: backup.SourceResourceID, TargetResourceID: target.ID, TargetNodeID: target.Runtime.Spec.Assignment.NodeID,
		ArtifactSHA256: backup.SHA256, ArtifactSize: backup.ArtifactSize, SourcePostgresVersion: backup.SourcePostgresVersion, TargetPostgresVersion: target.Runtime.Spec.Version,
		SourceProfile: backup.SourceProfile, SourceImage: backup.SourceImage, TargetProfile: target.Runtime.Spec.Profile, TargetImage: target.Runtime.Spec.Image,
		SourceSpecRevision: backup.SourceSpecRevision, SourceSpecHash: backup.SourceSpecHash, SourcePVCUID: backup.SourcePVCUID, SourcePVName: backup.SourcePVName, SourcePVUID: backup.SourcePVUID, TargetSpecRevision: target.Runtime.Spec.TopologyRevision, TargetSpecHash: target.Runtime.Spec.SpecHash,
		TargetDatabase: target.Runtime.Spec.Connection.Database, TargetDatabaseOID: review.TargetDatabaseOID,
		TargetPVCName: target.Runtime.Evidence.PVCName, TargetPVCUID: target.Runtime.Evidence.PVCUID, TargetPVName: target.Runtime.Evidence.PVName, TargetPVUID: target.Runtime.Evidence.PVUID, TargetStorageHash: target.Runtime.Evidence.StorageHash,
		PristineEvidenceHash: review.PristineEvidenceHash, RestoreOptions: restorev1.CanonicalOptions(), Lifecycle: restorev1.LifecycleQueued,
		RequestedBy: strings.TrimSpace(actor), RequestedAt: now, CreatedAt: now,
	}
	payload := sha256.Sum256([]byte(backupID + "\x00" + targetID + "\x00" + reviewID + "\x00" + review.PristineEvidenceHash))
	return s.Store.Create(ctx, value, key, hex.EncodeToString(payload[:]))
}

func (s Service) Get(ctx context.Context, projectID, restoreID string) (restorev1.Restore, error) {
	if s.Store == nil {
		return restorev1.Restore{}, unavailable("restore authority is unavailable")
	}
	return s.Store.Get(ctx, projectID, restoreID)
}

func (s Service) List(ctx context.Context, projectID, backupID, targetID string) ([]restorev1.Restore, error) {
	if s.Store == nil {
		return nil, unavailable("restore authority is unavailable")
	}
	return s.Store.List(ctx, projectID, backupID, targetID)
}

func (s Service) LeaseReview(ctx context.Context, projectID, nodeID string) (restorev1.ReviewLease, bool, error) {
	if s.Store == nil || s.Resources == nil {
		return restorev1.ReviewLease{}, false, unavailable("restore authority is unavailable")
	}
	now, token := s.clock(), newID("rrvlease")
	review, ok, err := s.Store.ClaimReview(ctx, projectID, nodeID, token, now, now.Add(leaseTTL))
	if err != nil || !ok {
		return restorev1.ReviewLease{}, false, err
	}
	target, err := s.Resources.Get(ctx, projectID, review.TargetResourceID)
	if err != nil || staleTarget(review, target) {
		review.Lifecycle, review.FailureCode, review.FailureMessageRedacted = restorev1.ReviewFailed, restorev1.FailureStaleReview, "target authority changed before restore review"
		reviewed := now
		review.ReviewedAt = &reviewed
		updated, updateErr := s.Store.UpdateReviewClaimed(ctx, review, token)
		return restorev1.ReviewLease{Review: updated}, false, updateErr
	}
	return restorev1.ReviewLease{LeaseToken: token, Review: review, TargetSpec: target.Runtime.Spec}, true, nil
}

func (s Service) CompleteReview(ctx context.Context, projectID, reviewID string, result restorev1.ReviewResult) (restorev1.Review, error) {
	review, err := s.Store.GetReview(ctx, projectID, reviewID)
	if err != nil {
		return restorev1.Review{}, err
	}
	if review.LeaseToken == "" || review.LeaseToken != result.LeaseToken {
		return restorev1.Review{}, invalid(restorev1.FailureLeaseLost, "restore review lease is invalid")
	}
	now := s.clock()
	if result.Status == restorev1.ReviewLeased {
		review.LeaseExpiresAt = now.Add(leaseTTL)
		return s.Store.UpdateReviewClaimed(ctx, review, result.LeaseToken)
	}
	review.ReviewedAt = &now
	if result.Status == restorev1.ReviewSucceeded {
		review.TargetDatabaseOID, review.Objects = strings.TrimSpace(result.TargetDatabaseOID), result.Objects
		review.Pristine = result.Objects.Pristine()
		review.PristineEvidenceHash = restorev1.PristineEvidenceHash(review)
		if !review.Pristine || result.PristineEvidenceHash != review.PristineEvidenceHash {
			return restorev1.Review{}, invalid(restorev1.FailureTargetNotEmpty, "target database is not pristine")
		}
		review.Lifecycle = restorev1.ReviewSucceeded
	} else {
		review.Lifecycle, review.FailureCode, review.FailureMessageRedacted = restorev1.ReviewFailed, failureOr(result.FailureCode, restorev1.FailureTargetStateUnknown), bounded(result.FailureMessageRedacted)
	}
	return s.Store.UpdateReviewClaimed(ctx, review, result.LeaseToken)
}

func (s Service) Lease(ctx context.Context, projectID, nodeID string) (restorev1.Lease, bool, error) {
	if s.Artifacts == nil {
		return restorev1.Lease{}, false, nil
	}
	if s.Store == nil || s.Resources == nil || s.Backups == nil {
		return restorev1.Lease{}, false, unavailable("restore authority is unavailable")
	}
	now, token := s.clock(), newID("rstlease")
	value, ok, err := s.Store.Claim(ctx, projectID, nodeID, token, now, now.Add(leaseTTL))
	if err != nil || !ok {
		return restorev1.Lease{}, false, err
	}
	backup, target, err := s.authorities(ctx, projectID, value.BackupID, value.TargetResourceID)
	if err == nil {
		err = s.validateTarget(ctx, backup, target)
	}
	review, reviewErr := s.Store.GetReview(ctx, projectID, value.ReviewID)
	if err != nil || reviewErr != nil || stale(review, backup, target) || value.BackupRevision != restorev1.BackupRevision(backup) || value.PristineEvidenceHash != review.PristineEvidenceHash {
		value.Lifecycle, value.FailureCode, value.FailureMessageRedacted = restorev1.LifecycleFailed, restorev1.FailureStaleReview, "restore authority changed before Agent lease"
		done := now
		value.CompletedAt = &done
		updated, updateErr := s.Store.UpdateClaimed(ctx, value, token)
		return restorev1.Lease{Restore: updated}, false, updateErr
	}
	store, credential, err := s.Artifacts.LeaseConfig()
	if err != nil {
		value.Lifecycle, value.FailureCode, value.FailureMessageRedacted = restorev1.LifecycleFailed, restorev1.FailureDownload, "backup store is unavailable"
		done := now
		value.CompletedAt = &done
		updated, updateErr := s.Store.UpdateClaimed(ctx, value, token)
		return restorev1.Lease{Restore: updated}, false, updateErr
	}
	return restorev1.Lease{LeaseToken: token, Restore: value, Backup: backup, TargetSpec: target.Runtime.Spec, Store: store, Credential: credential}, true, nil
}

func (s Service) Complete(ctx context.Context, projectID, restoreID string, result restorev1.Result) (restorev1.Restore, error) {
	value, err := s.Store.Get(ctx, projectID, restoreID)
	if err != nil {
		return restorev1.Restore{}, err
	}
	if value.LeaseToken == "" || value.LeaseToken != result.LeaseToken {
		return restorev1.Restore{}, invalid(restorev1.FailureLeaseLost, "restore lease is invalid")
	}
	now := s.clock()
	if result.Status == restorev1.LifecycleRunning {
		if value.Lifecycle != restorev1.LifecycleLeased && value.Lifecycle != restorev1.LifecycleRunning {
			return restorev1.Restore{}, invalid(restorev1.FailureLeaseLost, "restore lease is not running")
		}
		value.Lifecycle = restorev1.LifecycleRunning
		if value.StartedAt == nil {
			value.StartedAt = &now
		}
		value.LeaseExpiresAt = now.Add(leaseTTL)
		return s.Store.UpdateClaimed(ctx, value, result.LeaseToken)
	}
	if value.Lifecycle != restorev1.LifecycleRunning {
		return restorev1.Restore{}, invalid(restorev1.FailureLeaseLost, "restore lease is not running")
	}
	value.PGRestoreVersion, value.ArchiveVerified = strings.TrimSpace(result.PGRestoreVersion), result.ArchiveVerified
	value.RollbackConfirmed, value.TargetPristineAfterFailure, value.RestoredObjects, value.VerificationMetadata = result.RollbackConfirmed, result.TargetPristineAfterFailure, result.RestoredObjects, result.VerificationMetadata
	if result.Status == restorev1.LifecycleSucceeded {
		value.Lifecycle, value.VerifyingAt = restorev1.LifecycleVerifying, &now
		value, err = s.Store.UpdateClaimed(ctx, value, result.LeaseToken)
		if err != nil {
			return restorev1.Restore{}, err
		}
		value.Lifecycle, value.CompletedAt = restorev1.LifecycleSucceeded, &now
		if err := value.ValidateSucceeded(); err != nil {
			return restorev1.Restore{}, invalid(restorev1.FailureVerification, "restore success evidence is invalid")
		}
	} else {
		value.Lifecycle, value.FailureCode, value.FailureMessageRedacted, value.CompletedAt = restorev1.LifecycleFailed, failureOr(result.FailureCode, restorev1.FailureExecution), bounded(result.FailureMessageRedacted), &now
		if !result.RollbackConfirmed && result.FailureCode == restorev1.FailureExecution {
			value.FailureCode = restorev1.FailureTargetStateUnknown
		}
	}
	return s.Store.UpdateClaimed(ctx, value, result.LeaseToken)
}

func (s Service) HasActive(ctx context.Context, projectID, resourceID string) (bool, error) {
	if s.Store == nil {
		return false, unavailable("restore authority is unavailable")
	}
	return s.Store.HasActive(ctx, projectID, resourceID)
}

func (s Service) authorities(ctx context.Context, projectID, backupID, targetID string) (backupv1.Backup, resourcev1.Resource, error) {
	if s.Store == nil || s.Backups == nil || s.Resources == nil {
		return backupv1.Backup{}, resourcev1.Resource{}, unavailable("restore authority is unavailable")
	}
	backup, err := s.Backups.Get(ctx, projectID, backupID)
	if err != nil {
		return backupv1.Backup{}, resourcev1.Resource{}, err
	}
	if backup.ProjectID != projectID {
		return backupv1.Backup{}, resourcev1.Resource{}, invalid(restorev1.FailureBackupInvalid, "backup is outside the requested project")
	}
	if backup.ValidateSucceeded() != nil {
		if _, ok := resourcev1.ParsePostgresVersion(backup.SourcePostgresVersion); !ok {
			return backupv1.Backup{}, resourcev1.Resource{}, invalid(restorev1.FailureVersionUnsupported, "backup PostgreSQL version is unsupported")
		}
		return backupv1.Backup{}, resourcev1.Resource{}, invalid(restorev1.FailureBackupNotReady, "backup is not a succeeded canonical logical backup")
	}
	if backup.SourceProfile == "" || backup.SourceImage == "" {
		return backupv1.Backup{}, resourcev1.Resource{}, invalid(restorev1.FailureBackupInvalid, "backup source profile metadata is incomplete")
	}
	target, err := s.Resources.Get(ctx, projectID, targetID)
	if err != nil {
		return backup, target, err
	}
	if target.ProjectID != projectID {
		return backupv1.Backup{}, resourcev1.Resource{}, invalid(restorev1.FailureTargetInvalid, "target is outside the requested project")
	}
	return backup, target, nil
}

func (s Service) validateTarget(ctx context.Context, backup backupv1.Backup, target resourcev1.Resource) error {
	if backup.SourceResourceID == target.ID {
		return invalid(restorev1.FailureTargetInvalid, "restore target must differ from backup source")
	}
	if target.EnvironmentID != backup.EnvironmentID {
		return invalid(restorev1.FailureEnvironmentUnsupported, "cross-environment restore is unsupported")
	}
	if !readyPostgres(target) {
		return invalid(restorev1.FailureTargetNotReady, "target PostgreSQL resource is not factually ready")
	}
	if !resourcev1.CompatiblePostgresVersions(backup.SourcePostgresVersion, target.Runtime.Spec.Version) || backup.SourceProfile != target.Runtime.Spec.Profile || backup.SourceImage != target.Runtime.Spec.Image || target.Runtime.Spec.Image != resourcev1.PostgresImage {
		return invalid(restorev1.FailureVersionUnsupported, "source and target must use the exact supported PostgreSQL profile and image")
	}
	bindings, err := s.Resources.ListBindings(ctx, target.ProjectID, target.EnvironmentID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.Target.ID == target.ID {
			return invalid(restorev1.FailureTargetHasBindings, "restore target has active ResourceBindings")
		}
	}
	return nil
}

func readyPostgres(v resourcev1.Resource) bool {
	return v.Kind == resourcev1.KindManagedService && v.Type == resourcev1.TypePostgres && v.Lifecycle == resourcev1.LifecycleReady && v.Runtime != nil && v.Runtime.Evidence != nil && v.Runtime.Evidence.WorkloadReady && v.Runtime.Evidence.AuthReady && v.Runtime.Evidence.StorageReady && v.Runtime.Evidence.PVCUID != "" && v.Runtime.Evidence.StorageHash != "" && v.Runtime.Spec.Connection.Database == backupv1.CanonicalDatabase
}
func stale(r restorev1.Review, b backupv1.Backup, t resourcev1.Resource) bool {
	return r.BackupID != b.ID || r.BackupRevision != restorev1.BackupRevision(b) || staleTarget(r, t)
}
func staleTarget(r restorev1.Review, t resourcev1.Resource) bool {
	return !readyPostgres(t) || r.TargetResourceID != t.ID || r.TargetNodeID != t.Runtime.Spec.Assignment.NodeID || r.TargetSpecRevision != t.Runtime.Spec.TopologyRevision || r.TargetSpecHash != t.Runtime.Spec.SpecHash || r.TargetPVCUID != t.Runtime.Evidence.PVCUID || r.TargetStorageHash != t.Runtime.Evidence.StorageHash || r.TargetDatabase != t.Runtime.Spec.Connection.Database
}
func validKey(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && len(v) <= 128 && !strings.ContainsAny(v, " \t\r\n\x00")
}
func bounded(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 512 {
		return v[:512]
	}
	if v == "" {
		return "restore failed"
	}
	return v
}
func failureOr(v, fallback string) string {
	if restorev1.ValidFailure(v) {
		return v
	}
	return fallback
}
func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func newID(prefix string) string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("generate %s id: %v", prefix, err))
	}
	return prefix + "_" + hex.EncodeToString(value)
}
func invalid(code, message string) Error { return Error{Code: code, Status: 409, Message: message} }
func unavailable(message string) Error {
	return Error{Code: restorev1.FailureTargetStateUnknown, Status: 503, Message: message}
}
