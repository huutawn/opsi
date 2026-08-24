// Package resourcev1 defines canonical managed and external infrastructure
// resource contracts. It describes authority only; it does not provision.
package resourcev1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const SchemaVersion = "opsi.resource/v1"

type Kind string

const (
	KindApplication      Kind = "application"
	KindManagedService   Kind = "managed_service"
	KindExternalResource Kind = "external_resource"
)

type Type string

const (
	TypePostgres Type = "postgres"
	TypeRedis    Type = "redis"
	TypeNATS     Type = "nats"
	TypeRabbitMQ Type = "rabbitmq"
)

type SupportTier string

const (
	SupportSupported    SupportTier = "supported"
	SupportExperimental SupportTier = "experimental"
	SupportUnsupported  SupportTier = "unsupported"
)

type LifecycleState string

const (
	LifecycleUnplaced     LifecycleState = "unplaced"
	LifecyclePlanned      LifecycleState = "planned"
	LifecycleProvisioning LifecycleState = "provisioning"
	LifecycleReady        LifecycleState = "ready"
	LifecycleDegraded     LifecycleState = "degraded"
	LifecycleFailed       LifecycleState = "failed"
	LifecycleDeleting     LifecycleState = "deleting"
	LifecycleUnknown      LifecycleState = "unknown"
	LifecycleConfigured   LifecycleState = "configured"
)

type Protocol string

const (
	ProtocolPostgres Protocol = "postgres"
	ProtocolRedis    Protocol = "redis"
	ProtocolNATS     Protocol = "nats"
	ProtocolAMQP     Protocol = "amqp"
	ProtocolMySQL    Protocol = "mysql"
	ProtocolHTTP     Protocol = "http"
	ProtocolTCP      Protocol = "tcp"
	ProtocolCustom   Protocol = "custom"
)

type ValueSensitivity string

const (
	ValueNonSecret ValueSensitivity = "non_secret"
	ValueSecret    ValueSensitivity = "secret"
)

type SecretReference struct {
	SecretID string `json:"secret_id"`
	Key      string `json:"key,omitempty"`
}

type GeneratedValueDefinition struct {
	Name        string           `json:"name"`
	Sensitivity ValueSensitivity `json:"sensitivity"`
}

type StorageCapability struct {
	Supported bool `json:"supported"`
	Required  bool `json:"required"`
}

type ResourceTypeDefinition struct {
	Type            Type                       `json:"type"`
	DisplayName     string                     `json:"display_name"`
	SupportTier     SupportTier                `json:"support_tier"`
	Stateful        bool                       `json:"stateful"`
	DefaultPort     int                        `json:"default_port"`
	Protocols       []Protocol                 `json:"protocols"`
	RequiredConfig  []string                   `json:"required_config"`
	OptionalConfig  []string                   `json:"optional_config"`
	CredentialKeys  []string                   `json:"credential_keys"`
	GeneratedValues []GeneratedValueDefinition `json:"generated_values"`
	Storage         StorageCapability          `json:"storage"`
	Provisioning    ProvisioningCapability     `json:"provisioning"`
}

type ProvisioningCapability struct {
	Implemented bool                  `json:"implemented"`
	Profiles    []ProvisioningProfile `json:"profiles"`
}

type ProvisioningProfile struct {
	Name     string             `json:"name"`
	Versions []SupportedVersion `json:"versions"`
}

type SupportedVersion struct {
	Version string `json:"version"`
	Image   string `json:"image"`
}

type StorageRequest struct {
	Persistent bool   `json:"persistent"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	PolicyRef  string `json:"policy_ref,omitempty"`
}

type Placement struct {
	RuntimeID string `json:"runtime_id,omitempty"`
}

type ExposurePolicy struct {
	Mode string `json:"mode"`
}

type ManagedSpec struct {
	Type             Type              `json:"type"`
	Version          string            `json:"version,omitempty"`
	Profile          string            `json:"profile,omitempty"`
	Replicas         int32             `json:"replicas"`
	CPUMillicores    int64             `json:"cpu_millicores"`
	MemoryBytes      int64             `json:"memory_bytes"`
	Storage          StorageRequest    `json:"storage"`
	ServiceConfig    map[string]string `json:"service_config,omitempty"`
	CredentialRefs   []SecretReference `json:"credential_refs,omitempty"`
	ConnectionPolicy ExposurePolicy    `json:"connection_policy"`
}

const ManagedResourceSpecSchemaVersion = "opsi.managed_resource_spec/v1"

const (
	FailureProvisioningUnsupported      = "MANAGED_RESOURCE_PROVISIONING_UNSUPPORTED"
	FailureUnplaced                     = "MANAGED_RESOURCE_UNPLACED"
	FailureAssignmentInvalid            = "MANAGED_RESOURCE_ASSIGNMENT_INVALID"
	FailureSpecInvalid                  = "MANAGED_RESOURCE_SPEC_INVALID"
	FailureImageUnavailable             = "MANAGED_RESOURCE_IMAGE_UNAVAILABLE"
	FailureApplyFailed                  = "MANAGED_RESOURCE_APPLY_FAILED"
	FailureCredentialUnavailable        = "MANAGED_RESOURCE_CREDENTIAL_UNAVAILABLE"
	FailureSecretApplyFailed            = "MANAGED_RESOURCE_SECRET_APPLY_FAILED"
	FailureAuthFailed                   = "MANAGED_RESOURCE_AUTH_FAILED"
	FailureReadinessFailed              = "MANAGED_RESOURCE_READINESS_FAILED"
	FailureRuntimeMismatch              = "MANAGED_RESOURCE_RUNTIME_MISMATCH"
	FailureDeleteFailed                 = "MANAGED_RESOURCE_DELETE_FAILED"
	FailureStorageRequired              = "MANAGED_RESOURCE_STORAGE_REQUIRED"
	FailureStorageInvalid               = "MANAGED_RESOURCE_STORAGE_INVALID"
	FailurePVCApplyFailed               = "MANAGED_RESOURCE_PVC_APPLY_FAILED"
	FailurePVCNotBound                  = "MANAGED_RESOURCE_PVC_NOT_BOUND"
	FailureVolumeMountFailed            = "MANAGED_RESOURCE_VOLUME_MOUNT_FAILED"
	FailureDatabaseInitFailed           = "MANAGED_RESOURCE_DATABASE_INIT_FAILED"
	FailureStorageResizeUnsupported     = "MANAGED_RESOURCE_STORAGE_RESIZE_UNSUPPORTED"
	FailureVersionUpgradeUnsupported    = "MANAGED_RESOURCE_VERSION_UPGRADE_UNSUPPORTED"
	FailurePersistentDeleteUnsupported  = "MANAGED_RESOURCE_PERSISTENT_DELETE_UNSUPPORTED"
	FailureBindingCredentialUnavailable = "RESOURCE_BINDING_CREDENTIAL_UNAVAILABLE"
	FailureBindingRoleCreate            = "RESOURCE_BINDING_ROLE_CREATE_FAILED"
	FailureBindingRoleReconcile         = "RESOURCE_BINDING_ROLE_RECONCILE_FAILED"
	FailureBindingAuth                  = "RESOURCE_BINDING_AUTH_FAILED"
	FailureBindingSecretMaterialization = "RESOURCE_BINDING_SECRET_MATERIALIZATION_FAILED"
	FailureBindingRoleRevoke            = "RESOURCE_BINDING_ROLE_REVOKE_FAILED"
	FailureBindingActive                = "RESOURCE_BINDING_ACTIVE"
)

const (
	CredentialPurposeResourceManagement = "resource_management"
	CredentialPurposeResourceBinding    = "resource_binding"
	CredentialPurposeWorkloadSecret     = "workload_secret"
)

type ManagedResourceAssignment struct {
	RuntimeID string `json:"runtime_id"`
	NodeID    string `json:"node_id"`
	AgentID   string `json:"agent_id"`
}

// ManagedResourceCredential is transient Cloud-to-Agent material. It is never
// part of Resource, TopologyPlan, ManagedResourceSpec, or WorkloadSpec.
type ManagedResourceCredential struct {
	CredentialID string `json:"credential_id"`
	Purpose      string `json:"purpose,omitempty"`
	OwnerID      string `json:"owner_id,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Database     string `json:"database,omitempty"`
}

func (c ManagedResourceCredential) Validate() error {
	if c.CredentialID == "" || c.Username == "" || c.Password == "" || len(c.Username) > 128 || len(c.Password) > 1024 || len(c.Database) > 128 || strings.ContainsAny(c.Username+c.Password+c.Database, "\x00\r\n") {
		return errors.New("managed resource credential is invalid")
	}
	return nil
}

func (c ManagedResourceCredential) ValidateBinding(bindingID, resourceID string) error {
	if err := c.ValidateFor(TypePostgres); err != nil || c.Purpose != CredentialPurposeResourceBinding || c.OwnerID != bindingID || c.ResourceID != resourceID {
		return errors.New("PostgreSQL binding credential is invalid")
	}
	return nil
}

type BindingCredentialSpec struct {
	CredentialID string
	BindingID    string
	ResourceID   string
	Username     string
	Database     string
}

type WorkloadSecretSpec struct {
	CredentialID string
	ProjectID    string
	ServiceID    string
	LogicalName  string
}

type WorkloadSecretMetadata struct {
	ID          string    `json:"id"`
	Reference   string    `json:"reference"`
	ProjectID   string    `json:"project_id"`
	ServiceID   string    `json:"service_id"`
	LogicalName string    `json:"logical_name"`
	Revision    uint64    `json:"revision"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type WorkloadSecretUpsert struct {
	CredentialID   string
	ProjectID      string
	ServiceID      string
	LogicalName    string
	Value          string
	IdempotencyKey string
}

func (c ManagedResourceCredential) ValidateWorkloadSecret(projectID, serviceID string) error {
	if err := c.Validate(); err != nil || c.Purpose != CredentialPurposeWorkloadSecret || c.ResourceID != projectID || c.OwnerID != serviceID {
		return errors.New("workload secret credential is invalid")
	}
	return nil
}

func (c ManagedResourceCredential) ValidateFor(resourceType Type) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if resourceType == TypePostgres && c.Database == "" {
		return errors.New("managed PostgreSQL credential is invalid")
	}
	return nil
}

type ManagedResourcePort struct {
	Name     string   `json:"name"`
	Port     int32    `json:"port"`
	Protocol Protocol `json:"protocol"`
}

type ManagedResourceConnection struct {
	ServiceName string   `json:"service_name"`
	Host        string   `json:"host"`
	Port        int32    `json:"port"`
	Protocol    Protocol `json:"protocol"`
	Database    string   `json:"database,omitempty"`
	URL         string   `json:"url,omitempty"`
}

// ManagedResourceSpec is the immutable Cloud-compiled runtime authority sent to an Agent.
type ManagedResourceSpec struct {
	SchemaVersion     string                    `json:"schema_version"`
	ResourceID        string                    `json:"resource_id"`
	ProjectID         string                    `json:"project_id"`
	EnvironmentID     string                    `json:"environment_id"`
	ResourceType      Type                      `json:"resource_type"`
	Profile           string                    `json:"profile"`
	Version           string                    `json:"version"`
	Image             string                    `json:"image"`
	Assignment        ManagedResourceAssignment `json:"assignment"`
	Replicas          int32                     `json:"replicas"`
	CPUMillicores     int64                     `json:"cpu_millicores"`
	MemoryBytes       int64                     `json:"memory_bytes"`
	Ports             []ManagedResourcePort     `json:"ports"`
	Storage           StorageRequest            `json:"storage"`
	Connection        ManagedResourceConnection `json:"connection"`
	CredentialID      string                    `json:"credential_id,omitempty"`
	ConfigurationHash string                    `json:"configuration_hash"`
	TopologyRevision  uint64                    `json:"topology_revision"`
	TopologyHash      string                    `json:"topology_hash"`
	SpecHash          string                    `json:"spec_hash"`
}

func (s ManagedResourceSpec) Hash() (string, error) {
	s.SpecHash = ""
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s ManagedResourceSpec) Validate() error {
	if s.SchemaVersion != ManagedResourceSpecSchemaVersion || s.ResourceID == "" || s.ProjectID == "" || s.EnvironmentID == "" || (s.ResourceType != TypeNATS && s.ResourceType != TypeRedis && s.ResourceType != TypePostgres) {
		return errors.New("managed resource identity is invalid")
	}
	expectedVersion, expectedImage := NATSVersion, NATSImage
	if s.ResourceType == TypeRedis {
		expectedVersion, expectedImage = ValkeyVersion, ValkeyImage
	} else if s.ResourceType == TypePostgres {
		expectedVersion, expectedImage = PostgresVersion, PostgresImage
	}
	if s.Profile != "single-node-experimental" || s.Version != expectedVersion || s.Image != expectedImage || !strings.Contains(s.Image, "@sha256:") {
		return errors.New("managed resource image authority is invalid")
	}
	if s.Assignment.RuntimeID == "" || s.Assignment.NodeID == "" || s.Assignment.AgentID == "" || s.Replicas != 1 || s.CPUMillicores < 1 || s.MemoryBytes < 1 {
		return errors.New("managed resource runtime intent is invalid")
	}
	if s.ResourceType == TypePostgres {
		if !s.Storage.Persistent || s.Storage.SizeBytes < 1 || s.Storage.PolicyRef != StoragePolicyDefault {
			return errors.New("managed PostgreSQL storage intent is invalid")
		}
	} else if s.Storage.Persistent || s.Storage.SizeBytes != 0 || s.Storage.PolicyRef != "" {
		return errors.New("managed resource runtime intent is invalid")
	}
	portName, port, protocol := "nats", int32(4222), ProtocolNATS
	if s.ResourceType == TypeRedis {
		portName, port, protocol = "redis", 6379, ProtocolRedis
		if s.CredentialID == "" || s.Connection.URL != "" {
			return errors.New("managed resource credential authority is invalid")
		}
	} else if s.ResourceType == TypePostgres {
		portName, port, protocol = "postgres", 5432, ProtocolPostgres
		if s.CredentialID == "" || s.Connection.Database != "opsi" || s.Connection.URL != "" {
			return errors.New("managed resource credential authority is invalid")
		}
	}
	if len(s.Ports) != 1 || s.Ports[0].Name != portName || s.Ports[0].Port != port || s.Ports[0].Protocol != protocol || s.Connection.Protocol != protocol || s.Connection.Port != port || s.Connection.Host == "" || s.Connection.ServiceName == "" || s.ResourceType == TypeNATS && s.Connection.URL != "nats://"+s.Connection.Host+":4222" {
		return errors.New("managed resource connection intent is invalid")
	}
	if len(s.ConfigurationHash) != 64 || s.TopologyRevision < 1 || len(s.TopologyHash) != 64 || len(s.SpecHash) != 64 {
		return errors.New("managed resource revision authority is invalid")
	}
	hash, err := s.Hash()
	if err != nil || hash != s.SpecHash {
		return errors.New("managed resource spec hash is invalid")
	}
	return nil
}

type ManagedResourceEvidence struct {
	ObservedSpecHash  string    `json:"observed_spec_hash"`
	WorkloadReady     bool      `json:"workload_ready"`
	PodReady          bool      `json:"pod_ready"`
	ServiceReady      bool      `json:"service_ready"`
	SecretReady       bool      `json:"secret_ready"`
	AuthReady         bool      `json:"auth_ready"`
	Image             string    `json:"image"`
	ImageID           string    `json:"image_id,omitempty"`
	AvailableReplicas int32     `json:"available_replicas"`
	StorageReady      bool      `json:"storage_ready,omitempty"`
	VolumeMounted     bool      `json:"volume_mounted,omitempty"`
	Namespace         string    `json:"namespace,omitempty"`
	PVCName           string    `json:"pvc_name,omitempty"`
	PVCUID            string    `json:"pvc_uid,omitempty"`
	PVName            string    `json:"pv_name,omitempty"`
	PVUID             string    `json:"pv_uid,omitempty"`
	StorageClass      string    `json:"storage_class,omitempty"`
	ReclaimPolicy     string    `json:"reclaim_policy,omitempty"`
	RequestedBytes    int64     `json:"requested_bytes,omitempty"`
	ActualStorage     string    `json:"actual_storage,omitempty"`
	StorageHash       string    `json:"storage_hash,omitempty"`
	StorageRetained   bool      `json:"storage_retained,omitempty"`
	Deleted           bool      `json:"deleted,omitempty"`
	ObservedAt        time.Time `json:"observed_at"`
}

type TLSConfig struct {
	Mode      string           `json:"mode,omitempty"`
	SecretRef *SecretReference `json:"secret_ref,omitempty"`
}

type ExternalSpec struct {
	Protocol      Protocol         `json:"protocol"`
	Endpoint      string           `json:"endpoint"`
	Port          int              `json:"port,omitempty"`
	Database      string           `json:"database,omitempty"`
	Path          string           `json:"path,omitempty"`
	CredentialRef *SecretReference `json:"credential_ref,omitempty"`
	TLS           TLSConfig        `json:"tls"`
}

type Resource struct {
	SchemaVersion string                  `json:"schema_version"`
	ID            string                  `json:"id"`
	ProjectID     string                  `json:"project_id"`
	EnvironmentID string                  `json:"environment_id"`
	Name          string                  `json:"name"`
	Kind          Kind                    `json:"kind"`
	Provider      string                  `json:"provider"`
	Type          Type                    `json:"type"`
	Lifecycle     LifecycleState          `json:"lifecycle"`
	Managed       *ManagedSpec            `json:"managed,omitempty"`
	External      *ExternalSpec           `json:"external,omitempty"`
	InternalName  string                  `json:"internal_name,omitempty"`
	Runtime       *ManagedResourceRuntime `json:"runtime,omitempty"`
	CreatedBy     string                  `json:"created_by"`
	CreatedAt     time.Time               `json:"created_at"`
	UpdatedAt     time.Time               `json:"updated_at"`
}

type ManagedResourceRuntime struct {
	Spec           ManagedResourceSpec      `json:"spec"`
	Evidence       *ManagedResourceEvidence `json:"evidence,omitempty"`
	FailureCode    string                   `json:"failure_code,omitempty"`
	FailureMessage string                   `json:"failure_message,omitempty"`
	DeleteActor    string                   `json:"delete_actor,omitempty"`
	LeaseToken     string                   `json:"-"`
	LeaseExpiresAt time.Time                `json:"-"`
}

type CreateRequest struct {
	EnvironmentID string        `json:"environment_id"`
	Name          string        `json:"name"`
	Kind          Kind          `json:"kind"`
	Provider      string        `json:"provider,omitempty"`
	Type          Type          `json:"type"`
	Managed       *ManagedSpec  `json:"managed,omitempty"`
	External      *ExternalSpec `json:"external,omitempty"`
}

type UpdateRequest struct {
	Managed  *ManagedSpec  `json:"managed,omitempty"`
	External *ExternalSpec `json:"external,omitempty"`
}

type EndpointReference struct {
	Kind Kind   `json:"kind"`
	ID   string `json:"id"`
}

type RuntimeConnectionReference struct {
	Name        string           `json:"name"`
	Sensitivity ValueSensitivity `json:"sensitivity"`
	Value       string           `json:"value,omitempty"`
	SecretRef   *SecretReference `json:"secret_ref,omitempty"`
}

type Binding struct {
	SchemaVersion string                       `json:"schema_version"`
	ID            string                       `json:"id"`
	ProjectID     string                       `json:"project_id"`
	EnvironmentID string                       `json:"environment_id"`
	Source        EndpointReference            `json:"source"`
	Target        EndpointReference            `json:"target"`
	Protocol      Protocol                     `json:"protocol"`
	LogicalName   string                       `json:"logical_name"`
	Lifecycle     LifecycleState               `json:"lifecycle"`
	CredentialID  string                       `json:"credential_id,omitempty"`
	RoleName      string                       `json:"role_name,omitempty"`
	Database      string                       `json:"database,omitempty"`
	FailureCode   string                       `json:"failure_code,omitempty"`
	RuntimeRefs   []RuntimeConnectionReference `json:"runtime_refs"`
	CreatedAt     time.Time                    `json:"created_at"`
	UpdatedAt     time.Time                    `json:"updated_at"`
}

const (
	PostgresBindingEnsure = "ensure"
	PostgresBindingRevoke = "revoke"
)

type PostgresBindingOperation struct {
	BindingID    string                     `json:"binding_id"`
	CredentialID string                     `json:"credential_id"`
	RoleName     string                     `json:"role_name"`
	Database     string                     `json:"database"`
	Action       string                     `json:"action"`
	Create       bool                       `json:"create,omitempty"`
	Credential   *ManagedResourceCredential `json:"credential,omitempty"`
}

type PostgresBindingResult struct {
	BindingID   string `json:"binding_id"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	FailureCode string `json:"failure_code,omitempty"`
}

type CreateBindingRequest struct {
	EnvironmentID string            `json:"environment_id"`
	Source        EndpointReference `json:"source"`
	Target        EndpointReference `json:"target"`
	Protocol      Protocol          `json:"protocol"`
	LogicalName   string            `json:"logical_name"`
}
