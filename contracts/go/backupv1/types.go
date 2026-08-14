// Package backupv1 defines logical database backup authority and Agent contracts.
package backupv1

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const (
	SchemaVersion             = "opsi.backup/v1"
	BackupTypePostgresLogical = "postgres_logical"
	FormatCustom              = "custom"
	StoreProviderS3           = "s3"
	CanonicalDatabase         = "opsi"
)

const (
	LifecycleQueued    = "queued"
	LifecycleLeased    = "leased"
	LifecycleRunning   = "running"
	LifecycleSucceeded = "succeeded"
	LifecycleFailed    = "failed"
)

const (
	FailureResourceNotReady    = "BACKUP_RESOURCE_NOT_READY"
	FailureAlreadyRunning      = "BACKUP_ALREADY_RUNNING"
	FailureDatabaseUnavailable = "BACKUP_DATABASE_UNAVAILABLE"
	FailureDumpFailed          = "BACKUP_DUMP_FAILED"
	FailureStoreUnavailable    = "BACKUP_STORE_UNAVAILABLE"
	FailureUploadFailed        = "BACKUP_UPLOAD_FAILED"
	FailureIntegrityFailed     = "BACKUP_INTEGRITY_FAILED"
	FailureArtifactInvalid     = "BACKUP_ARTIFACT_INVALID"
	FailureLeaseLost           = "BACKUP_LEASE_LOST"
)

func CanonicalDumpOptions() []string {
	return []string{"-Fc", "--no-owner", "--no-privileges"}
}

type Backup struct {
	SchemaVersion          string          `json:"schema_version"`
	ID                     string          `json:"id"`
	ProjectID              string          `json:"project_id"`
	EnvironmentID          string          `json:"environment_id"`
	SourceResourceID       string          `json:"source_resource_id"`
	SourceNodeID           string          `json:"source_node_id"`
	ResourceType           resourcev1.Type `json:"resource_type"`
	BackupType             string          `json:"backup_type"`
	SourceDatabase         string          `json:"source_database"`
	SourcePostgresVersion  string          `json:"source_postgres_version,omitempty"`
	SourceSpecRevision     uint64          `json:"source_spec_revision"`
	SourceSpecHash         string          `json:"source_spec_hash"`
	SourcePVCName          string          `json:"source_pvc_name"`
	SourcePVCUID           string          `json:"source_pvc_uid"`
	SourcePVName           string          `json:"source_pv_name,omitempty"`
	SourcePVUID            string          `json:"source_pv_uid,omitempty"`
	SourceStorageHash      string          `json:"source_storage_hash"`
	Format                 string          `json:"format"`
	DumpOptions            []string        `json:"dump_options"`
	Lifecycle              string          `json:"lifecycle"`
	StoreID                string          `json:"store_id"`
	ObjectKey              string          `json:"object_key"`
	ObjectETag             string          `json:"object_etag,omitempty"`
	ObjectVersionID        string          `json:"object_version_id,omitempty"`
	ArtifactSize           int64           `json:"artifact_size,omitempty"`
	SHA256                 string          `json:"sha256,omitempty"`
	PGDumpVersion          string          `json:"pg_dump_version,omitempty"`
	ArchiveVerified        bool            `json:"archive_verified"`
	RequestedBy            string          `json:"requested_by"`
	RequestedAt            time.Time       `json:"requested_at"`
	CreatedAt              time.Time       `json:"created_at"`
	LeasedAt               *time.Time      `json:"leased_at,omitempty"`
	StartedAt              *time.Time      `json:"started_at,omitempty"`
	CompletedAt            *time.Time      `json:"completed_at,omitempty"`
	FailureCode            string          `json:"failure_code,omitempty"`
	FailureMessageRedacted string          `json:"failure_message_redacted,omitempty"`
	AttemptCount           int             `json:"attempt_count"`
	LeaseToken             string          `json:"-"`
	LeaseExpiresAt         time.Time       `json:"-"`
}

func (b Backup) ValidateSucceeded() error {
	if b.SchemaVersion != SchemaVersion || b.ID == "" || b.ProjectID == "" || b.EnvironmentID == "" || b.SourceResourceID == "" || b.ResourceType != resourcev1.TypePostgres || b.BackupType != BackupTypePostgresLogical || b.SourceDatabase != CanonicalDatabase || b.Format != FormatCustom {
		return errors.New("backup identity is invalid")
	}
	decodedSHA, shaErr := hex.DecodeString(b.SHA256)
	if b.Lifecycle != LifecycleSucceeded || b.ArtifactSize <= 0 || shaErr != nil || len(decodedSHA) != 32 || b.SHA256 != strings.ToLower(b.SHA256) || b.PGDumpVersion == "" || b.SourcePostgresVersion == "" || !b.ArchiveVerified || b.CompletedAt == nil {
		return errors.New("succeeded backup evidence is incomplete")
	}
	return nil
}

type CreateRequest struct{}

type StoreSpec struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint,omitempty"`
	Bucket        string `json:"bucket"`
	Region        string `json:"region"`
	CABundlePEM   string `json:"ca_bundle_pem,omitempty"`
	AllowInsecure bool   `json:"allow_insecure,omitempty"`
}

type StoreCredential struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token,omitempty"`
}

func (s StoreSpec) Validate() error {
	if s.ID == "" || s.Provider != StoreProviderS3 || s.Bucket == "" || s.Region == "" || strings.ContainsAny(s.Bucket, "\x00\r\n/") {
		return errors.New("backup store configuration is invalid")
	}
	return nil
}

func (c StoreCredential) Validate() error {
	if c.AccessKey == "" || c.SecretKey == "" || strings.ContainsAny(c.AccessKey+c.SecretKey+c.SessionToken, "\x00\r\n") {
		return errors.New("backup store credential is invalid")
	}
	return nil
}

type Lease struct {
	LeaseToken string                         `json:"lease_token"`
	Backup     Backup                         `json:"backup"`
	SourceSpec resourcev1.ManagedResourceSpec `json:"source_spec"`
	Store      StoreSpec                      `json:"store"`
	Credential StoreCredential                `json:"credential"`
}

type Result struct {
	Status                 string `json:"status"`
	LeaseToken             string `json:"lease_token"`
	SourcePostgresVersion  string `json:"source_postgres_version,omitempty"`
	PGDumpVersion          string `json:"pg_dump_version,omitempty"`
	ArtifactSize           int64  `json:"artifact_size,omitempty"`
	SHA256                 string `json:"sha256,omitempty"`
	ObjectETag             string `json:"object_etag,omitempty"`
	ObjectVersionID        string `json:"object_version_id,omitempty"`
	ArchiveVerified        bool   `json:"archive_verified,omitempty"`
	FailureCode            string `json:"failure_code,omitempty"`
	FailureMessageRedacted string `json:"failure_message_redacted,omitempty"`
}
