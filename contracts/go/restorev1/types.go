// Package restorev1 defines restore-to-new-PostgreSQL authority and Agent contracts.
package restorev1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const SchemaVersion = "opsi.restore/v1"

const (
	LifecycleQueued    = "queued"
	LifecycleLeased    = "leased"
	LifecycleRunning   = "running"
	LifecycleVerifying = "verifying"
	LifecycleSucceeded = "succeeded"
	LifecycleFailed    = "failed"
	ReviewQueued       = "queued"
	ReviewLeased       = "leased"
	ReviewSucceeded    = "succeeded"
	ReviewFailed       = "failed"
)

const (
	FailureBackupNotReady         = "RESTORE_BACKUP_NOT_READY"
	FailureBackupIntegrity        = "RESTORE_BACKUP_INTEGRITY_FAILED"
	FailureBackupInvalid          = "RESTORE_BACKUP_INVALID"
	FailureTargetInvalid          = "RESTORE_TARGET_INVALID"
	FailureTargetNotReady         = "RESTORE_TARGET_NOT_READY"
	FailureTargetNotEmpty         = "RESTORE_TARGET_NOT_EMPTY"
	FailureTargetHasBindings      = "RESTORE_TARGET_HAS_BINDINGS"
	FailureVersionUnsupported     = "RESTORE_VERSION_UNSUPPORTED"
	FailureAlreadyRunning         = "RESTORE_ALREADY_RUNNING"
	FailureStaleReview            = "RESTORE_STALE_REVIEW"
	FailureDownload               = "RESTORE_DOWNLOAD_FAILED"
	FailureDatabaseUnavailable    = "RESTORE_DATABASE_UNAVAILABLE"
	FailureExecution              = "RESTORE_EXECUTION_FAILED"
	FailureVerification           = "RESTORE_VERIFICATION_FAILED"
	FailureLeaseLost              = "RESTORE_LEASE_LOST"
	FailureTargetStateUnknown     = "RESTORE_TARGET_STATE_UNKNOWN"
	FailureEnvironmentUnsupported = "RESTORE_ENVIRONMENT_UNSUPPORTED"
)

func CanonicalOptions() []string {
	return []string{"--single-transaction", "--no-owner", "--no-privileges"}
}

type ObjectSummary struct {
	Schemas   int64 `json:"schemas"`
	Tables    int64 `json:"tables"`
	Sequences int64 `json:"sequences"`
	Indexes   int64 `json:"indexes"`
	Functions int64 `json:"functions"`
}

func (s ObjectSummary) Pristine() bool {
	return s.Schemas == 1 && s.Tables == 0 && s.Sequences == 0 && s.Indexes == 0 && s.Functions == 0
}

type Review struct {
	SchemaVersion          string                    `json:"schema_version"`
	ID                     string                    `json:"id"`
	ProjectID              string                    `json:"project_id"`
	EnvironmentID          string                    `json:"environment_id"`
	BackupID               string                    `json:"backup_id"`
	BackupCreatedAt        time.Time                 `json:"backup_created_at"`
	BackupArtifactSHA256   string                    `json:"backup_artifact_sha256"`
	BackupRevision         string                    `json:"backup_revision"`
	SourceResourceID       string                    `json:"source_resource_id"`
	SourcePostgresVersion  string                    `json:"source_postgres_version"`
	ArtifactSize           int64                     `json:"artifact_size"`
	TargetResourceID       string                    `json:"target_resource_id"`
	TargetNodeID           string                    `json:"target_node_id"`
	TargetPostgresVersion  string                    `json:"target_postgres_version"`
	TargetDatabase         string                    `json:"target_database"`
	TargetDatabaseOID      string                    `json:"target_database_oid,omitempty"`
	TargetLifecycle        resourcev1.LifecycleState `json:"target_lifecycle"`
	TargetSpecRevision     uint64                    `json:"target_spec_revision"`
	TargetSpecHash         string                    `json:"target_spec_hash"`
	TargetPVCName          string                    `json:"target_pvc_name"`
	TargetPVCUID           string                    `json:"target_pvc_uid"`
	TargetPVName           string                    `json:"target_pv_name,omitempty"`
	TargetPVUID            string                    `json:"target_pv_uid,omitempty"`
	TargetStorageHash      string                    `json:"target_storage_hash"`
	Pristine               bool                      `json:"pristine"`
	Objects                ObjectSummary             `json:"objects"`
	PristineEvidenceHash   string                    `json:"pristine_evidence_hash,omitempty"`
	Warning                string                    `json:"warning"`
	Lifecycle              string                    `json:"lifecycle"`
	RequestedBy            string                    `json:"requested_by"`
	RequestedAt            time.Time                 `json:"requested_at"`
	ReviewedAt             *time.Time                `json:"reviewed_at,omitempty"`
	FailureCode            string                    `json:"failure_code,omitempty"`
	FailureMessageRedacted string                    `json:"failure_message_redacted,omitempty"`
	AttemptCount           int                       `json:"attempt_count"`
	LeaseToken             string                    `json:"-"`
	LeaseExpiresAt         time.Time                 `json:"-"`
}

type Restore struct {
	SchemaVersion              string            `json:"schema_version"`
	ID                         string            `json:"id"`
	ProjectID                  string            `json:"project_id"`
	EnvironmentID              string            `json:"environment_id"`
	ReviewID                   string            `json:"review_id"`
	BackupID                   string            `json:"backup_id"`
	BackupRevision             string            `json:"backup_revision"`
	SourceResourceID           string            `json:"source_resource_id"`
	TargetResourceID           string            `json:"target_resource_id"`
	TargetNodeID               string            `json:"target_node_id"`
	ArtifactSHA256             string            `json:"artifact_sha256"`
	ArtifactSize               int64             `json:"artifact_size"`
	SourcePostgresVersion      string            `json:"source_postgres_version"`
	TargetPostgresVersion      string            `json:"target_postgres_version"`
	SourceProfile              string            `json:"source_profile"`
	SourceImage                string            `json:"source_image"`
	TargetProfile              string            `json:"target_profile"`
	TargetImage                string            `json:"target_image"`
	SourceSpecRevision         uint64            `json:"source_spec_revision"`
	SourceSpecHash             string            `json:"source_spec_hash"`
	SourcePVCUID               string            `json:"source_pvc_uid"`
	SourcePVName               string            `json:"source_pv_name,omitempty"`
	SourcePVUID                string            `json:"source_pv_uid,omitempty"`
	TargetSpecRevision         uint64            `json:"target_spec_revision"`
	TargetSpecHash             string            `json:"target_spec_hash"`
	TargetDatabase             string            `json:"target_database"`
	TargetDatabaseOID          string            `json:"target_database_oid"`
	TargetPVCName              string            `json:"target_pvc_name"`
	TargetPVCUID               string            `json:"target_pvc_uid"`
	TargetPVName               string            `json:"target_pv_name,omitempty"`
	TargetPVUID                string            `json:"target_pv_uid,omitempty"`
	TargetStorageHash          string            `json:"target_storage_hash"`
	PristineEvidenceHash       string            `json:"pristine_evidence_hash"`
	RestoreOptions             []string          `json:"restore_options"`
	Lifecycle                  string            `json:"lifecycle"`
	RequestedBy                string            `json:"requested_by"`
	RequestedAt                time.Time         `json:"requested_at"`
	CreatedAt                  time.Time         `json:"created_at"`
	LeasedAt                   *time.Time        `json:"leased_at,omitempty"`
	StartedAt                  *time.Time        `json:"started_at,omitempty"`
	VerifyingAt                *time.Time        `json:"verifying_at,omitempty"`
	CompletedAt                *time.Time        `json:"completed_at,omitempty"`
	FailureCode                string            `json:"failure_code,omitempty"`
	FailureMessageRedacted     string            `json:"failure_message_redacted,omitempty"`
	AttemptCount               int               `json:"attempt_count"`
	PGRestoreVersion           string            `json:"pg_restore_version,omitempty"`
	ArchiveVerified            bool              `json:"archive_verified"`
	RollbackConfirmed          bool              `json:"rollback_confirmed,omitempty"`
	TargetPristineAfterFailure bool              `json:"target_pristine_after_failure,omitempty"`
	RestoredObjects            ObjectSummary     `json:"restored_objects"`
	VerificationMetadata       map[string]string `json:"verification_metadata,omitempty"`
	LeaseToken                 string            `json:"-"`
	LeaseExpiresAt             time.Time         `json:"-"`
}

type ReviewRequest struct {
	TargetResourceID string `json:"target_resource_id"`
}
type CreateRequest struct {
	TargetResourceID string `json:"target_resource_id"`
	ReviewID         string `json:"review_id"`
}

type ReviewLease struct {
	LeaseToken string                         `json:"lease_token"`
	Review     Review                         `json:"review"`
	TargetSpec resourcev1.ManagedResourceSpec `json:"target_spec"`
}

type Lease struct {
	LeaseToken string                         `json:"lease_token"`
	Restore    Restore                        `json:"restore"`
	Backup     backupv1.Backup                `json:"backup"`
	TargetSpec resourcev1.ManagedResourceSpec `json:"target_spec"`
	Store      backupv1.StoreSpec             `json:"store"`
	Credential backupv1.StoreCredential       `json:"credential"`
}

type ReviewResult struct {
	Status                 string        `json:"status"`
	LeaseToken             string        `json:"lease_token"`
	TargetDatabaseOID      string        `json:"target_database_oid,omitempty"`
	Objects                ObjectSummary `json:"objects"`
	PristineEvidenceHash   string        `json:"pristine_evidence_hash,omitempty"`
	FailureCode            string        `json:"failure_code,omitempty"`
	FailureMessageRedacted string        `json:"failure_message_redacted,omitempty"`
}

type Result struct {
	Status                     string            `json:"status"`
	LeaseToken                 string            `json:"lease_token"`
	PGRestoreVersion           string            `json:"pg_restore_version,omitempty"`
	ArchiveVerified            bool              `json:"archive_verified,omitempty"`
	RollbackConfirmed          bool              `json:"rollback_confirmed,omitempty"`
	TargetPristineAfterFailure bool              `json:"target_pristine_after_failure,omitempty"`
	RestoredObjects            ObjectSummary     `json:"restored_objects"`
	VerificationMetadata       map[string]string `json:"verification_metadata,omitempty"`
	FailureCode                string            `json:"failure_code,omitempty"`
	FailureMessageRedacted     string            `json:"failure_message_redacted,omitempty"`
}

func BackupRevision(value backupv1.Backup) string {
	data, _ := json.Marshal([]any{value.ID, value.SHA256, value.ArtifactSize, value.ObjectKey, value.ObjectVersionID, value.CompletedAt})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func PristineEvidenceHash(review Review) string {
	data, _ := json.Marshal([]any{review.TargetResourceID, review.TargetNodeID, review.TargetSpecHash, review.TargetPVCUID, review.TargetDatabase, review.TargetDatabaseOID, review.Objects})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r Review) ValidateSucceeded() error {
	if r.SchemaVersion != SchemaVersion || r.ID == "" || r.BackupID == "" || r.TargetResourceID == "" || r.TargetNodeID == "" || r.SourceResourceID == r.TargetResourceID || r.Lifecycle != ReviewSucceeded || !r.Pristine || !r.Objects.Pristine() || r.ReviewedAt == nil || r.PristineEvidenceHash != PristineEvidenceHash(r) {
		return errors.New("restore review evidence is invalid")
	}
	return nil
}

func (r Restore) ValidateSucceeded() error {
	if r.SchemaVersion != SchemaVersion || r.ID == "" || r.TargetNodeID == "" || r.SourceResourceID == r.TargetResourceID || r.Lifecycle != LifecycleSucceeded || r.CompletedAt == nil || r.VerifyingAt == nil || !r.ArchiveVerified || r.PGRestoreVersion == "" || len(r.ArtifactSHA256) != 64 || r.VerificationMetadata["connectivity"] != "authenticated" || r.VerificationMetadata["transaction"] != "committed" {
		return errors.New("restore success evidence is invalid")
	}
	return nil
}

func ValidFailure(code string) bool {
	for _, value := range []string{FailureBackupNotReady, FailureBackupIntegrity, FailureBackupInvalid, FailureTargetInvalid, FailureTargetNotReady, FailureTargetNotEmpty, FailureTargetHasBindings, FailureVersionUnsupported, FailureAlreadyRunning, FailureStaleReview, FailureDownload, FailureDatabaseUnavailable, FailureExecution, FailureVerification, FailureLeaseLost, FailureTargetStateUnknown, FailureEnvironmentUnsupported} {
		if strings.TrimSpace(code) == value {
			return true
		}
	}
	return false
}
