package resourcev1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

func ManagedResourceStorageHash(spec ManagedResourceSpec) string {
	sum := sha256.Sum256([]byte(spec.ResourceID + "\x00" + spec.Storage.PolicyRef + "\x00" + strconv.FormatInt(spec.Storage.SizeBytes, 10)))
	return hex.EncodeToString(sum[:])
}

const RetainedStorageSchemaVersion = "opsi.retained_storage/v1"

type RetainedStorageLifecycle string

const (
	RetainedStorageRetained      RetainedStorageLifecycle = "retained"
	RetainedStorageDestroying    RetainedStorageLifecycle = "destroying"
	RetainedStorageDestroyed     RetainedStorageLifecycle = "destroyed"
	RetainedStorageDestroyFailed RetainedStorageLifecycle = "destroy_failed"
	RetainedStorageUnknown       RetainedStorageLifecycle = "unknown"
)

const (
	FailureRetainedStorageNotFound         = "RETAINED_STORAGE_NOT_FOUND"
	FailureRetainedStorageIdentityMismatch = "RETAINED_STORAGE_IDENTITY_MISMATCH"
	FailureRetainedStorageOwnership        = "RETAINED_STORAGE_OWNERSHIP_MISMATCH"
	FailureRetainedStorageStaleReview      = "RETAINED_STORAGE_STALE_REVIEW"
	FailureRetainedStorageActiveReference  = "RETAINED_STORAGE_ACTIVE_REFERENCE"
	FailureStorageReclaimUnsupported       = "PERSISTENT_STORAGE_RECLAIM_UNSUPPORTED"
	FailureStorageDeleteFailed             = "PERSISTENT_STORAGE_DELETE_FAILED"
	FailureStorageReclaimTimeout           = "PERSISTENT_STORAGE_RECLAIM_TIMEOUT"
	FailureStorageDestroyFailed            = "PERSISTENT_STORAGE_DESTROY_FAILED"
)

type RetainedStorage struct {
	SchemaVersion      string                    `json:"schema_version"`
	ID                 string                    `json:"id"`
	OriginalResourceID string                    `json:"original_resource_id"`
	ProjectID          string                    `json:"project_id"`
	EnvironmentID      string                    `json:"environment_id"`
	ResourceType       Type                      `json:"resource_type"`
	ResourceName       string                    `json:"resource_name"`
	Namespace          string                    `json:"namespace"`
	PVCName            string                    `json:"pvc_name"`
	PVCUID             string                    `json:"pvc_uid"`
	PVName             string                    `json:"pv_name"`
	PVUID              string                    `json:"pv_uid,omitempty"`
	StorageClass       string                    `json:"storage_class"`
	ReclaimPolicy      string                    `json:"reclaim_policy"`
	RequestedBytes     int64                     `json:"requested_bytes"`
	ActualSize         string                    `json:"actual_size"`
	StorageHash        string                    `json:"storage_hash"`
	Assignment         ManagedResourceAssignment `json:"assignment"`
	Lifecycle          RetainedStorageLifecycle  `json:"lifecycle"`
	Revision           uint64                    `json:"revision"`
	OriginalCreatedBy  string                    `json:"original_created_by,omitempty"`
	RetainedBy         string                    `json:"retained_by,omitempty"`
	RetainedAt         time.Time                 `json:"retained_at"`
	DestroyRequestedBy string                    `json:"destroy_requested_by,omitempty"`
	DestroyRequestedAt *time.Time                `json:"destroy_requested_at,omitempty"`
	DestroyedAt        *time.Time                `json:"destroyed_at,omitempty"`
	FailureCode        string                    `json:"failure_code,omitempty"`
	FailureMessage     string                    `json:"failure_message_redacted,omitempty"`
	LeaseToken         string                    `json:"-"`
	LeaseExpiresAt     time.Time                 `json:"-"`
}

type RetainedStorageReview struct {
	RetainedStorageID  string    `json:"retained_storage_id"`
	OriginalResourceID string    `json:"original_resource_id"`
	ResourceName       string    `json:"resource_name"`
	PVCName            string    `json:"pvc_name"`
	PVCUID             string    `json:"pvc_uid"`
	PVName             string    `json:"pv_name"`
	PVUID              string    `json:"pv_uid,omitempty"`
	StorageClass       string    `json:"storage_class"`
	ReclaimPolicy      string    `json:"reclaim_policy"`
	RequestedBytes     int64     `json:"requested_bytes"`
	ActualSize         string    `json:"actual_size"`
	StorageHash        string    `json:"storage_hash"`
	RetainedAt         time.Time `json:"retained_at"`
	Revision           uint64    `json:"revision"`
	ActiveResource     bool      `json:"active_resource"`
	ActiveBinding      bool      `json:"active_binding"`
	Warning            string    `json:"warning"`
	ReviewToken        string    `json:"review_token"`
	ReviewedAt         time.Time `json:"reviewed_at"`
}

func (r RetainedStorageReview) Hash() string {
	data, _ := json.Marshal(struct {
		RetainedStorageID string    `json:"retained_storage_id"`
		PVCUID            string    `json:"pvc_uid"`
		PVUID             string    `json:"pv_uid,omitempty"`
		StorageHash       string    `json:"storage_hash"`
		RetainedAt        time.Time `json:"retained_at"`
		Revision          uint64    `json:"revision"`
	}{r.RetainedStorageID, r.PVCUID, r.PVUID, r.StorageHash, r.RetainedAt, r.Revision})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type DestroyRetainedStorageRequest struct {
	ReviewToken string `json:"review_token"`
}

type RetainedStorageDestroySpec struct {
	SchemaVersion      string                    `json:"schema_version"`
	RetainedStorageID  string                    `json:"retained_storage_id"`
	OriginalResourceID string                    `json:"original_resource_id"`
	ProjectID          string                    `json:"project_id"`
	EnvironmentID      string                    `json:"environment_id"`
	ResourceType       Type                      `json:"resource_type"`
	Namespace          string                    `json:"namespace"`
	PVCName            string                    `json:"pvc_name"`
	PVCUID             string                    `json:"pvc_uid"`
	PVName             string                    `json:"pv_name"`
	PVUID              string                    `json:"pv_uid,omitempty"`
	StorageClass       string                    `json:"storage_class"`
	ReclaimPolicy      string                    `json:"reclaim_policy"`
	StorageHash        string                    `json:"storage_hash"`
	Assignment         ManagedResourceAssignment `json:"assignment"`
	Revision           uint64                    `json:"revision"`
	Operation          string                    `json:"operation"`
}

func (s RetainedStorageDestroySpec) Validate() error {
	if s.SchemaVersion != RetainedStorageSchemaVersion || s.Operation != "destroy" || s.RetainedStorageID == "" || s.OriginalResourceID == "" || s.ProjectID == "" || s.EnvironmentID == "" || s.ResourceType != TypePostgres {
		return errors.New("retained storage identity is invalid")
	}
	if s.Namespace == "" || s.PVCName == "" || s.PVCUID == "" || s.PVName == "" || s.StorageClass == "" || s.ReclaimPolicy == "" || len(s.StorageHash) != 64 || s.Revision < 1 {
		return errors.New("retained storage factual identity is invalid")
	}
	if s.Assignment.RuntimeID == "" || s.Assignment.NodeID == "" || s.Assignment.AgentID == "" || strings.ContainsAny(s.Namespace+s.PVCName+s.PVCUID+s.PVName+s.PVUID+s.StorageClass, "\x00\r\n") {
		return errors.New("retained storage assignment is invalid")
	}
	return nil
}

type RetainedStorageDestroyEvidence struct {
	PVCAbsent  bool      `json:"pvc_absent"`
	PVAbsent   bool      `json:"pv_absent"`
	ObservedAt time.Time `json:"observed_at"`
}
