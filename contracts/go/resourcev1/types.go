// Package resourcev1 defines canonical managed and external infrastructure
// resource contracts. It describes authority only; it does not provision.
package resourcev1

import "time"

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
	Placement        Placement         `json:"placement"`
	ServiceConfig    map[string]string `json:"service_config,omitempty"`
	CredentialRefs   []SecretReference `json:"credential_refs,omitempty"`
	ConnectionPolicy ExposurePolicy    `json:"connection_policy"`
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
	SchemaVersion string         `json:"schema_version"`
	ID            string         `json:"id"`
	ProjectID     string         `json:"project_id"`
	EnvironmentID string         `json:"environment_id"`
	Name          string         `json:"name"`
	Kind          Kind           `json:"kind"`
	Provider      string         `json:"provider"`
	Type          Type           `json:"type"`
	Lifecycle     LifecycleState `json:"lifecycle"`
	Managed       *ManagedSpec   `json:"managed,omitempty"`
	External      *ExternalSpec  `json:"external,omitempty"`
	InternalName  string         `json:"internal_name,omitempty"`
	CreatedBy     string         `json:"created_by"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
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
	RuntimeRefs   []RuntimeConnectionReference `json:"runtime_refs"`
	CreatedAt     time.Time                    `json:"created_at"`
	UpdatedAt     time.Time                    `json:"updated_at"`
}

type CreateBindingRequest struct {
	EnvironmentID string            `json:"environment_id"`
	Source        EndpointReference `json:"source"`
	Target        EndpointReference `json:"target"`
	Protocol      Protocol          `json:"protocol"`
	LogicalName   string            `json:"logical_name"`
}
