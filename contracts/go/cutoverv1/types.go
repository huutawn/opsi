// Package cutoverv1 defines application cutover review authority and Agent contracts.
package cutoverv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

const SchemaVersion = "opsi.cutover_review/v1"

const (
	ReviewQueued    = "queued"
	ReviewLeased    = "leased"
	ReviewSucceeded = "succeeded"
	ReviewFailed    = "failed"
)

const (
	WarningNotContinuouslySynchronized = "TARGET_NOT_CONTINUOUSLY_SYNCHRONIZED"
	WarningBackupAgeNonZero            = "BACKUP_AGE_NONZERO"
)

const (
	FailureSourceBindingInvalid       = "CUTOVER_SOURCE_BINDING_INVALID"
	FailureTargetBindingInvalid       = "CUTOVER_TARGET_BINDING_INVALID"
	FailureTargetNotReady             = "CUTOVER_TARGET_NOT_READY"
	FailureSourceNotReady             = "CUTOVER_SOURCE_NOT_READY"
	FailureTargetRestoreNotSucceeded  = "CUTOVER_TARGET_RESTORE_NOT_SUCCEEDED"
	FailureApplicationStateInvalid    = "CUTOVER_APPLICATION_STATE_INVALID"
	FailureActiveOperationConflict    = "CUTOVER_ACTIVE_OPERATION_CONFLICT"
	FailureDatabaseUnavailable        = "CUTOVER_DATABASE_UNAVAILABLE"
	FailurePrivilegeInvalid           = "CUTOVER_PRIVILEGE_INVALID"
	FailureStaleReview                = "CUTOVER_STALE_REVIEW"
	FailureEnvironmentMismatch        = "CUTOVER_ENVIRONMENT_MISMATCH"
	FailureIdentityConflict           = "CUTOVER_IDENTITY_CONFLICT"
	FailureLeaseLost                  = "CUTOVER_LEASE_LOST"
	FailureSchemaMismatch             = "CUTOVER_SCHEMA_MISMATCH"
	FailureTargetInvalid              = "CUTOVER_TARGET_INVALID"
	FailureSourceInvalid              = "CUTOVER_SOURCE_INVALID"
	FailureIdempotencyKeyInvalid      = "CUTOVER_IDEMPOTENCY_KEY_INVALID"
	FailureIdempotencyConflict        = "CUTOVER_IDEMPOTENCY_CONFLICT"
	FailureReviewNotReady             = "CUTOVER_REVIEW_NOT_READY"
	FailureCutoverAlreadyRunning      = "CUTOVER_ALREADY_RUNNING"
	FailureSourceUnavailable          = "CUTOVER_SOURCE_UNAVAILABLE"
	FailureTargetUnavailable          = "CUTOVER_TARGET_UNAVAILABLE"
	FailureTargetPrivilegeInvalid     = "CUTOVER_TARGET_PRIVILEGE_INVALID"
	FailureConfigApplyFailed          = "CUTOVER_CONFIG_APPLY_FAILED"
	FailureDeploymentFailed           = "CUTOVER_DEPLOYMENT_FAILED"
	FailureApplicationHealthFailed    = "CUTOVER_APPLICATION_HEALTH_FAILED"
	FailureTargetVerificationFailed   = "CUTOVER_TARGET_VERIFICATION_FAILED"
)

const CutoverSchemaVersion = "opsi.cutover/v1"

const (
	CutoverQueued     = "queued"
	CutoverValidating = "validating"
	CutoverApplying   = "applying"
	CutoverDeploying  = "deploying"
	CutoverVerifying  = "verifying"
	CutoverSucceeded  = "succeeded"
	CutoverFailed     = "failed"
)

type CutoverVerificationSummary struct {
	SourceSQLPreflight       string `json:"source_sql_preflight"`
	TargetSQLPreflight       string `json:"target_sql_preflight"`
	TargetRoleAttributes     string `json:"target_role_attributes"`
	DeploymentReady          bool   `json:"deployment_ready"`
	WorkloadReady            bool   `json:"workload_ready"`
	TargetDBConnected        bool   `json:"target_db_connected"`
	RestoredDataVerified     bool   `json:"restored_data_verified"`
	TargetOnlyMarkerPresent  bool   `json:"target_only_marker_present"`
	SourceOnlyMarkerAbsent   bool   `json:"source_only_marker_absent"`
	PostCutoverTargetWritten bool   `json:"post_cutover_target_written"`
	SourceRollbackPreserved  bool   `json:"source_rollback_preserved"`
}

type ApplicationCutover struct {
	SchemaVersion                       string                     `json:"schema_version"`
	ID                                  string                     `json:"id"`
	ProjectID                           string                     `json:"project_id"`
	EnvironmentID                       string                     `json:"environment_id"`
	ApplicationID                       string                     `json:"application_id"`
	CutoverReviewID                     string                     `json:"cutover_review_id"`
	SourceBindingID                     string                     `json:"source_binding_id"`
	TargetBindingID                     string                     `json:"target_binding_id"`
	SourceResourceID                    string                     `json:"source_resource_id"`
	TargetResourceID                    string                     `json:"target_resource_id"`
	ReviewedApplicationConfigRevision   uint64                     `json:"reviewed_application_config_revision"`
	ReviewedApplicationConfigHash       string                     `json:"reviewed_application_config_hash"`
	PreCutoverApplicationConfigRevision uint64                     `json:"pre_cutover_application_config_revision"`
	PreCutoverApplicationConfigHash     string                     `json:"pre_cutover_application_config_hash"`
	ResultingApplicationConfigRevision  uint64                     `json:"resulting_application_config_revision"`
	ResultingApplicationConfigHash      string                     `json:"resulting_application_config_hash"`
	PreCutoverDeploymentJobID           string                     `json:"pre_cutover_deployment_job_id,omitempty"`
	PreCutoverBuildRecordID             string                     `json:"pre_cutover_build_record_id,omitempty"`
	PreCutoverImageDigest               string                     `json:"pre_cutover_image_digest,omitempty"`
	DeploymentJobID                     string                     `json:"deployment_job_id,omitempty"`
	Lifecycle                           string                     `json:"lifecycle"`
	RequestedBy                         string                     `json:"requested_by"`
	RequestedAt                         time.Time                  `json:"requested_at"`
	AppliedAt                           *time.Time                 `json:"applied_at,omitempty"`
	CompletedAt                         *time.Time                 `json:"completed_at,omitempty"`
	UpdatedAt                           time.Time                  `json:"updated_at"`
	TargetNodeID                        string                     `json:"target_node_id,omitempty"`
	LeaseToken                          string                     `json:"-"`
	LeaseExpiresAt                      time.Time                  `json:"-"`
	AttemptCount                        int                        `json:"attempt_count"`
	FailureCode                         string                     `json:"failure_code,omitempty"`
	FailureMessageRedacted              string                     `json:"failure_message_redacted,omitempty"`
	VerificationSummary                 CutoverVerificationSummary `json:"verification_summary"`
	EvidenceHash                        string                     `json:"evidence_hash,omitempty"`
}

type ApplyRequest struct {
	CutoverReviewID string `json:"cutover_review_id"`
}

func CutoverEvidenceHash(c ApplicationCutover) string {
	data, _ := json.Marshal([]any{
		c.ID,
		c.ProjectID,
		c.EnvironmentID,
		c.ApplicationID,
		c.CutoverReviewID,
		c.SourceBindingID,
		c.TargetBindingID,
		c.SourceResourceID,
		c.TargetResourceID,
		c.ReviewedApplicationConfigRevision,
		c.ReviewedApplicationConfigHash,
		c.PreCutoverApplicationConfigRevision,
		c.PreCutoverApplicationConfigHash,
		c.ResultingApplicationConfigRevision,
		c.ResultingApplicationConfigHash,
		c.PreCutoverDeploymentJobID,
		c.PreCutoverBuildRecordID,
		c.PreCutoverImageDigest,
		c.DeploymentJobID,
		c.VerificationSummary,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c ApplicationCutover) ValidateSucceeded() error {
	if c.SchemaVersion != CutoverSchemaVersion ||
		c.ID == "" ||
		c.ProjectID == "" ||
		c.EnvironmentID == "" ||
		c.ApplicationID == "" ||
		c.CutoverReviewID == "" ||
		c.SourceBindingID == "" ||
		c.TargetBindingID == "" ||
		c.SourceResourceID == "" ||
		c.TargetResourceID == "" ||
		c.SourceBindingID == c.TargetBindingID ||
		c.SourceResourceID == c.TargetResourceID ||
		c.ResultingApplicationConfigRevision <= c.PreCutoverApplicationConfigRevision ||
		c.DeploymentJobID == "" ||
		c.Lifecycle != CutoverSucceeded ||
		c.CompletedAt == nil ||
		!c.VerificationSummary.WorkloadReady ||
		!c.VerificationSummary.TargetDBConnected ||
		!c.VerificationSummary.SourceRollbackPreserved ||
		c.EvidenceHash == "" ||
		c.EvidenceHash != CutoverEvidenceHash(c) {
		return errors.New("cutover evidence is invalid")
	}
	return nil
}

func ValidFailure(code string) bool {
	for _, value := range []string{
		FailureSourceBindingInvalid,
		FailureTargetBindingInvalid,
		FailureTargetNotReady,
		FailureSourceNotReady,
		FailureTargetRestoreNotSucceeded,
		FailureApplicationStateInvalid,
		FailureActiveOperationConflict,
		FailureDatabaseUnavailable,
		FailurePrivilegeInvalid,
		FailureStaleReview,
		FailureEnvironmentMismatch,
		FailureIdentityConflict,
		FailureLeaseLost,
		FailureSchemaMismatch,
		FailureTargetInvalid,
		FailureSourceInvalid,
		FailureIdempotencyKeyInvalid,
		FailureIdempotencyConflict,
		FailureReviewNotReady,
		FailureCutoverAlreadyRunning,
		FailureSourceUnavailable,
		FailureTargetUnavailable,
		FailureTargetPrivilegeInvalid,
		FailureConfigApplyFailed,
		FailureDeploymentFailed,
		FailureApplicationHealthFailed,
		FailureTargetVerificationFailed,
	} {
		if strings.TrimSpace(code) == value {
			return true
		}
	}
	return false
}

type ValidationSummary struct {
	SourceSQLPreflight   string `json:"source_sql_preflight"`
	TargetSQLPreflight   string `json:"target_sql_preflight"`
	TargetRoleAttributes string `json:"target_role_attributes"`
	SourceBindingReady   bool   `json:"source_binding_ready"`
	TargetBindingReady   bool   `json:"target_binding_ready"`
	TargetRestoreReady   bool   `json:"target_restore_ready"`
	TargetPVCUID         string `json:"target_pvc_uid,omitempty"`
	TargetPVUID          string `json:"target_pv_uid,omitempty"`
	TargetStorageHash    string `json:"target_storage_hash,omitempty"`
}

func (s ValidationSummary) PreflightPassed() bool {
	return s.SourceSQLPreflight == "PASS" &&
		s.TargetSQLPreflight == "PASS" &&
		strings.Contains(s.TargetRoleAttributes, "LOGIN") &&
		strings.Contains(s.TargetRoleAttributes, "NOSUPERUSER") &&
		s.SourceBindingReady &&
		s.TargetBindingReady &&
		s.TargetRestoreReady
}

type ApplicationCutoverReview struct {
	SchemaVersion             string            `json:"schema_version"`
	ID                        string            `json:"id"`
	ProjectID                 string            `json:"project_id"`
	EnvironmentID             string            `json:"environment_id"`
	ApplicationID             string            `json:"application_id"`
	SourceBindingID           string            `json:"source_binding_id"`
	SourceResourceID          string            `json:"source_resource_id"`
	TargetResourceID          string            `json:"target_resource_id"`
	TargetBindingID           string            `json:"target_binding_id"`
	ApplicationConfigRevision uint64            `json:"application_config_revision"`
	ApplicationConfigHash     string            `json:"application_config_hash"`
	SourceBindingRevision     string            `json:"source_binding_revision"`
	TargetBindingRevision     string            `json:"target_binding_revision"`
	SourceResourceRevision    uint64            `json:"source_resource_revision"`
	SourceResourceSpecHash    string            `json:"source_resource_spec_hash"`
	TargetResourceRevision    uint64            `json:"target_resource_revision"`
	TargetResourceSpecHash    string            `json:"target_resource_spec_hash"`
	TargetRestoreID           string            `json:"target_restore_id"`
	TargetRestoreRevision     string            `json:"target_restore_revision"`
	BackupID                  string            `json:"backup_id"`
	BackupCompletedAt         *time.Time        `json:"backup_completed_at,omitempty"`
	RestoreCompletedAt        *time.Time        `json:"restore_completed_at,omitempty"`
	BackupAgeSeconds          int64             `json:"backup_age_seconds"`
	ValidationSummary         ValidationSummary `json:"validation_summary"`
	IntegrityHashes           map[string]string `json:"integrity_hashes"`
	EvidenceHash              string            `json:"evidence_hash,omitempty"`
	Warnings                  []string          `json:"warnings"`
	Lifecycle                 string            `json:"lifecycle"`
	RequestedBy               string            `json:"requested_by"`
	RequestedAt               time.Time         `json:"requested_at"`
	ReviewedAt                *time.Time        `json:"reviewed_at,omitempty"`
	FailureCode               string            `json:"failure_code,omitempty"`
	FailureMessageRedacted    string            `json:"failure_message_redacted,omitempty"`
	AttemptCount              int               `json:"attempt_count"`
	TargetNodeID              string            `json:"target_node_id,omitempty"`
	LeaseToken                string            `json:"-"`
	LeaseExpiresAt            time.Time         `json:"-"`
}

type ReviewRequest struct {
	SourceBindingID string `json:"source_binding_id,omitempty"`
	TargetBindingID string `json:"target_binding_id"`
}

type ReviewLease struct {
	LeaseToken                 string                                `json:"lease_token"`
	Review                     ApplicationCutoverReview              `json:"review"`
	SourceSpec                 resourcev1.ManagedResourceSpec        `json:"source_spec"`
	TargetSpec                 resourcev1.ManagedResourceSpec        `json:"target_spec"`
	SourceCredential           *resourcev1.ManagedResourceCredential `json:"source_credential,omitempty"`
	TargetCredential           *resourcev1.ManagedResourceCredential `json:"target_credential,omitempty"`
	TargetManagementCredential *resourcev1.ManagedResourceCredential `json:"target_management_credential,omitempty"`
}

type ReviewResult struct {
	Status                 string            `json:"status"`
	LeaseToken             string            `json:"lease_token"`
	SourceSQLPreflight     string            `json:"source_sql_preflight"`
	TargetSQLPreflight     string            `json:"target_sql_preflight"`
	TargetRoleAttributes   string            `json:"target_role_attributes"`
	ValidationSummary      ValidationSummary `json:"validation_summary"`
	EvidenceHash           string            `json:"evidence_hash,omitempty"`
	FailureCode            string            `json:"failure_code,omitempty"`
	FailureMessageRedacted string            `json:"failure_message_redacted,omitempty"`
}

type CutoverApplyResult struct {
	Status                 string                     `json:"status"`
	VerificationSummary    CutoverVerificationSummary `json:"verification_summary"`
	EvidenceHash           string                     `json:"evidence_hash,omitempty"`
	FailureCode            string                     `json:"failure_code,omitempty"`
	FailureMessageRedacted string                     `json:"failure_message_redacted,omitempty"`
}

type CutoverResult = CutoverApplyResult


func EvidenceHash(review ApplicationCutoverReview) string {
	data, _ := json.Marshal([]any{
		review.ID,
		review.ProjectID,
		review.EnvironmentID,
		review.ApplicationID,
		review.SourceBindingID,
		review.SourceResourceID,
		review.TargetResourceID,
		review.TargetBindingID,
		review.ApplicationConfigRevision,
		review.ApplicationConfigHash,
		review.SourceBindingRevision,
		review.TargetBindingRevision,
		review.SourceResourceSpecHash,
		review.TargetResourceSpecHash,
		review.TargetRestoreID,
		review.TargetRestoreRevision,
		review.ValidationSummary,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func BindingRevision(b resourcev1.Binding) string {
	refs, _ := json.Marshal(b.RuntimeRefs)
	data, _ := json.Marshal([]any{
		b.ID,
		b.ProjectID,
		b.EnvironmentID,
		b.Source,
		b.Target,
		b.Protocol,
		b.LogicalName,
		b.Lifecycle,
		b.CredentialID,
		b.RoleName,
		b.Database,
		string(refs),
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func RestoreRevision(r restorev1.Restore) string {
	meta, _ := json.Marshal(r.VerificationMetadata)
	data, _ := json.Marshal([]any{
		r.ID,
		r.ProjectID,
		r.EnvironmentID,
		r.BackupID,
		r.SourceResourceID,
		r.TargetResourceID,
		r.TargetNodeID,
		r.ArtifactSHA256,
		r.ArtifactSize,
		r.TargetSpecHash,
		r.TargetDatabaseOID,
		r.TargetStorageHash,
		r.PristineEvidenceHash,
		r.Lifecycle,
		r.RestoredObjects,
		string(meta),
		r.CompletedAt,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r ApplicationCutoverReview) ValidateSucceeded() error {
	if r.SchemaVersion != SchemaVersion ||
		r.ID == "" ||
		r.ProjectID == "" ||
		r.EnvironmentID == "" ||
		r.ApplicationID == "" ||
		r.SourceBindingID == "" ||
		r.SourceResourceID == "" ||
		r.TargetResourceID == "" ||
		r.TargetBindingID == "" ||
		r.SourceBindingID == r.TargetBindingID ||
		r.SourceResourceID == r.TargetResourceID ||
		r.Lifecycle != ReviewSucceeded ||
		r.ReviewedAt == nil ||
		!r.ValidationSummary.PreflightPassed() ||
		r.EvidenceHash == "" ||
		r.EvidenceHash != EvidenceHash(r) {
		return errors.New("cutover review evidence is invalid")
	}
	return nil
}
