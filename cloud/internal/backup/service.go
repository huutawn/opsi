package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

var ErrNotFound = errors.New("backup not found")

const leaseTTL = 10 * time.Minute

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Message }

type Store interface {
	Create(context.Context, backupv1.Backup, string, string) (backupv1.Backup, bool, error)
	Get(context.Context, string, string) (backupv1.Backup, error)
	List(context.Context, string, string) ([]backupv1.Backup, error)
	Claim(context.Context, string, string, string, time.Time, time.Time) (backupv1.Backup, bool, error)
	UpdateClaimed(context.Context, backupv1.Backup, string) (backupv1.Backup, error)
	HasActive(context.Context, string, string) (bool, error)
}

type ResourceAuthority interface {
	Get(context.Context, string, string) (resourcev1.Resource, error)
}

type StoreAuthority interface {
	LeaseConfig() (backupv1.StoreSpec, backupv1.StoreCredential, error)
	ObjectKey(projectID, environmentID, resourceID, backupID string) string
}

type StaticStoreAuthority struct {
	Spec       backupv1.StoreSpec
	Credential backupv1.StoreCredential
	Prefix     string
}

func (s StaticStoreAuthority) LeaseConfig() (backupv1.StoreSpec, backupv1.StoreCredential, error) {
	if err := s.Spec.Validate(); err != nil {
		return backupv1.StoreSpec{}, backupv1.StoreCredential{}, err
	}
	if err := s.Credential.Validate(); err != nil {
		return backupv1.StoreSpec{}, backupv1.StoreCredential{}, err
	}
	return s.Spec, s.Credential, nil
}

func (s StaticStoreAuthority) ObjectKey(projectID, environmentID, resourceID, backupID string) string {
	parts := []string{strings.Trim(s.Prefix, "/"), "projects", url.PathEscape(projectID), "environments", url.PathEscape(environmentID), "resources", url.PathEscape(resourceID), "backups", url.PathEscape(backupID) + ".dump"}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, "/")
}

type Service struct {
	Store     Store
	Resources ResourceAuthority
	Artifacts StoreAuthority
	Now       func() time.Time
}

func (s Service) Create(ctx context.Context, projectID, resourceID, actor, key string) (backupv1.Backup, bool, error) {
	if s.Store == nil || s.Resources == nil || s.Artifacts == nil {
		return backupv1.Backup{}, false, unavailable(backupv1.FailureStoreUnavailable, "backup authority is unavailable")
	}
	if !validKey(key) {
		return backupv1.Backup{}, false, invalid("BACKUP_IDEMPOTENCY_KEY_INVALID", "idempotency key is invalid")
	}
	storeSpec, _, err := s.Artifacts.LeaseConfig()
	if err != nil {
		return backupv1.Backup{}, false, unavailable(backupv1.FailureStoreUnavailable, "backup store is unavailable")
	}
	resource, err := s.Resources.Get(ctx, projectID, resourceID)
	if err != nil {
		return backupv1.Backup{}, false, err
	}
	if err := readyPostgres(resource); err != nil {
		return backupv1.Backup{}, false, err
	}
	now := s.clock()
	id := newID("bkp")
	value := backupv1.Backup{
		SchemaVersion: backupv1.SchemaVersion, ID: id, ProjectID: projectID, EnvironmentID: resource.EnvironmentID,
		SourceResourceID: resource.ID, SourceNodeID: resource.Runtime.Spec.Assignment.NodeID, ResourceType: resource.Type,
		BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: backupv1.CanonicalDatabase, SourcePostgresVersion: resource.Runtime.Spec.Version,
		SourceSpecRevision: resource.Runtime.Spec.TopologyRevision, SourceSpecHash: resource.Runtime.Spec.SpecHash,
		SourcePVCName: resource.Runtime.Evidence.PVCName, SourcePVCUID: resource.Runtime.Evidence.PVCUID,
		SourcePVName: resource.Runtime.Evidence.PVName, SourcePVUID: resource.Runtime.Evidence.PVUID, SourceStorageHash: resource.Runtime.Evidence.StorageHash,
		Format: backupv1.FormatCustom, DumpOptions: backupv1.CanonicalDumpOptions(), Lifecycle: backupv1.LifecycleQueued,
		StoreID: storeSpec.ID, RequestedBy: strings.TrimSpace(actor), RequestedAt: now, CreatedAt: now,
	}
	value.ObjectKey = s.Artifacts.ObjectKey(value.ProjectID, value.EnvironmentID, value.SourceResourceID, value.ID)
	payload := sha256.Sum256([]byte(projectID + "\x00" + resourceID + "\x00" + backupv1.BackupTypePostgresLogical))
	return s.Store.Create(ctx, value, key, hex.EncodeToString(payload[:]))
}

func (s Service) Get(ctx context.Context, projectID, backupID string) (backupv1.Backup, error) {
	if s.Store == nil {
		return backupv1.Backup{}, unavailable(backupv1.FailureStoreUnavailable, "backup authority is unavailable")
	}
	return s.Store.Get(ctx, projectID, backupID)
}

func (s Service) List(ctx context.Context, projectID, resourceID string) ([]backupv1.Backup, error) {
	if s.Store == nil {
		return nil, unavailable(backupv1.FailureStoreUnavailable, "backup authority is unavailable")
	}
	return s.Store.List(ctx, projectID, resourceID)
}

func (s Service) HasActive(ctx context.Context, projectID, resourceID string) (bool, error) {
	if s.Store == nil {
		return false, unavailable(backupv1.FailureStoreUnavailable, "backup authority is unavailable")
	}
	return s.Store.HasActive(ctx, projectID, resourceID)
}

func (s Service) Lease(ctx context.Context, projectID, nodeID string) (backupv1.Lease, bool, error) {
	if s.Artifacts == nil {
		return backupv1.Lease{}, false, nil
	}
	if s.Store == nil || s.Resources == nil {
		return backupv1.Lease{}, false, unavailable(backupv1.FailureStoreUnavailable, "backup authority is unavailable")
	}
	now := s.clock()
	token := newID("bkplease")
	value, ok, err := s.Store.Claim(ctx, projectID, nodeID, token, now, now.Add(leaseTTL))
	if err != nil || !ok {
		return backupv1.Lease{}, false, err
	}
	resource, err := s.Resources.Get(ctx, projectID, value.SourceResourceID)
	if err == nil {
		err = readyPostgres(resource)
	}
	if err != nil || resource.Runtime.Spec.SpecHash != value.SourceSpecHash {
		failed := value
		failed.Lifecycle, failed.FailureCode, failed.FailureMessageRedacted = backupv1.LifecycleFailed, backupv1.FailureResourceNotReady, "source resource authority changed before backup lease"
		done := now
		failed.CompletedAt = &done
		updated, updateErr := s.Store.UpdateClaimed(ctx, failed, token)
		return backupv1.Lease{Backup: updated}, false, updateErr
	}
	store, credential, err := s.Artifacts.LeaseConfig()
	if err != nil {
		failed := value
		failed.Lifecycle, failed.FailureCode, failed.FailureMessageRedacted = backupv1.LifecycleFailed, backupv1.FailureStoreUnavailable, "backup store is unavailable"
		done := now
		failed.CompletedAt = &done
		updated, updateErr := s.Store.UpdateClaimed(ctx, failed, token)
		return backupv1.Lease{Backup: updated}, false, updateErr
	}
	return backupv1.Lease{LeaseToken: token, Backup: value, SourceSpec: resource.Runtime.Spec, Store: store, Credential: credential}, true, nil
}

func (s Service) Complete(ctx context.Context, projectID, backupID string, result backupv1.Result) (backupv1.Backup, error) {
	value, err := s.Get(ctx, projectID, backupID)
	if err != nil {
		return backupv1.Backup{}, err
	}
	if value.LeaseToken == "" || value.LeaseToken != result.LeaseToken {
		return backupv1.Backup{}, invalid(backupv1.FailureLeaseLost, "backup lease is invalid")
	}
	now := s.clock()
	if result.Status == backupv1.LifecycleRunning {
		if value.Lifecycle != backupv1.LifecycleLeased && value.Lifecycle != backupv1.LifecycleRunning {
			return backupv1.Backup{}, invalid(backupv1.FailureLeaseLost, "backup lease is not startable")
		}
		value.Lifecycle = backupv1.LifecycleRunning
		if value.StartedAt == nil {
			value.StartedAt = &now
		}
		value.LeaseExpiresAt = now.Add(leaseTTL)
		return s.Store.UpdateClaimed(ctx, value, result.LeaseToken)
	}
	if value.Lifecycle != backupv1.LifecycleRunning {
		return backupv1.Backup{}, invalid(backupv1.FailureLeaseLost, "backup lease is not running")
	}
	value.FailureCode = strings.TrimSpace(result.FailureCode)
	value.FailureMessageRedacted = bounded(result.FailureMessageRedacted, 512)
	switch result.Status {
	case backupv1.LifecycleSucceeded:
		value.SourcePostgresVersion = strings.TrimSpace(result.SourcePostgresVersion)
		value.PGDumpVersion = strings.TrimSpace(result.PGDumpVersion)
		value.ArtifactSize, value.SHA256 = result.ArtifactSize, strings.ToLower(strings.TrimSpace(result.SHA256))
		value.ObjectETag, value.ObjectVersionID, value.ArchiveVerified = strings.TrimSpace(result.ObjectETag), strings.TrimSpace(result.ObjectVersionID), result.ArchiveVerified
		value.Lifecycle, value.FailureCode, value.FailureMessageRedacted = backupv1.LifecycleSucceeded, "", ""
		value.CompletedAt = &now
		if err := value.ValidateSucceeded(); err != nil {
			return backupv1.Backup{}, invalid(backupv1.FailureIntegrityFailed, "backup success evidence is invalid")
		}
	case backupv1.LifecycleFailed:
		value.Lifecycle = backupv1.LifecycleFailed
		if !validFailure(value.FailureCode) {
			value.FailureCode = backupv1.FailureDumpFailed
		}
		if value.FailureMessageRedacted == "" {
			value.FailureMessageRedacted = "backup failed"
		}
		value.CompletedAt = &now
	default:
		return backupv1.Backup{}, invalid(backupv1.FailureLeaseLost, "backup result status is invalid")
	}
	return s.Store.UpdateClaimed(ctx, value, result.LeaseToken)
}

func readyPostgres(value resourcev1.Resource) error {
	if value.Kind != resourcev1.KindManagedService || value.Type != resourcev1.TypePostgres || value.Lifecycle != resourcev1.LifecycleReady || value.Runtime == nil || value.Runtime.Evidence == nil || !value.Runtime.Evidence.WorkloadReady || !value.Runtime.Evidence.AuthReady || !value.Runtime.Evidence.StorageReady || value.Runtime.Evidence.PVCUID == "" || value.Runtime.Evidence.StorageHash == "" || value.Runtime.Spec.Connection.Database != backupv1.CanonicalDatabase {
		return Error{Code: backupv1.FailureResourceNotReady, Status: 409, Message: "PostgreSQL resource is not factually ready for logical backup"}
	}
	return nil
}

func validFailure(code string) bool {
	switch code {
	case backupv1.FailureDatabaseUnavailable, backupv1.FailureDumpFailed, backupv1.FailureStoreUnavailable, backupv1.FailureUploadFailed, backupv1.FailureIntegrityFailed, backupv1.FailureArtifactInvalid, backupv1.FailureLeaseLost:
		return true
	default:
		return false
	}
}

func validKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n\x00")
}

func bounded(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func newID(prefix string) string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("generate %s id: %v", prefix, err))
	}
	return prefix + "_" + hex.EncodeToString(value)
}

func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func invalid(code, message string) Error     { return Error{Code: code, Status: 409, Message: message} }
func unavailable(code, message string) Error { return Error{Code: code, Status: 503, Message: message} }
