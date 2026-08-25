package resourcev1

func managedDefinition(resourceType Type, display string, port int, protocol Protocol, stateful, storageRequired bool, credentials, values []string) ResourceTypeDefinition {
	generated := make([]GeneratedValueDefinition, 0, len(values))
	for _, value := range values {
		sensitivity := ValueNonSecret
		if value != "HOST" && value != "PORT" {
			sensitivity = ValueSecret
		}
		generated = append(generated, GeneratedValueDefinition{Name: value, Sensitivity: sensitivity})
	}
	return ResourceTypeDefinition{
		Type: resourceType, DisplayName: display, SupportTier: SupportExperimental, Stateful: stateful,
		DefaultPort: port, Protocols: []Protocol{protocol}, RequiredConfig: []string{}, OptionalConfig: []string{},
		CredentialKeys: credentials, GeneratedValues: generated, Storage: StorageCapability{Supported: stateful, Required: storageRequired},
	}
}

const (
	NATSVersion                 = "2.11.8-alpine"
	NATSImage                   = "docker.io/library/nats@sha256:9e5633ac7584fc4e80d34be3ff7e15aa3fabec79a5573c2d9abefcf1f7761d9a"
	ValkeyVersion               = "8.1.3-alpine"
	ValkeyImage                 = "docker.io/valkey/valkey@sha256:5d586b6d9574ce96954142cdca85f4903a0efdbd4d04d4fe27c9fb245cdf91d4"
	PostgresVersion             = "18.6"
	PostgresImageVariant        = "18.6-bookworm"
	PostgresImage               = "docker.io/library/postgres:18.6-bookworm@sha256:b939b3851e2cccb017dc4497af63b15e34efa57fba036548773c53b2f16a8871"
	StoragePolicyDefault        = "default"
	DefaultPostgresStorageBytes = int64(5 << 30)
	MaxManagedStorageBytes      = int64(1 << 50)
)

func Definitions() []ResourceTypeDefinition {
	return []ResourceTypeDefinition{
		func() ResourceTypeDefinition {
			definition := managedDefinition(TypePostgres, "PostgreSQL", 5432, ProtocolPostgres, true, true, []string{"user", "password", "database"}, []string{"HOST", "PORT", "NAME", "USER", "PASSWORD", "URL"})
			definition.GeneratedValues[2].Sensitivity = ValueNonSecret
			definition.Provisioning = ProvisioningCapability{Implemented: true, Profiles: []ProvisioningProfile{{Name: "single-node-experimental", Versions: []SupportedVersion{{Version: PostgresVersion, Image: PostgresImage}}}}}
			return definition
		}(),
		func() ResourceTypeDefinition {
			definition := managedDefinition(TypeRedis, "Redis / Valkey-compatible", 6379, ProtocolRedis, true, false, []string{"user", "password"}, []string{"HOST", "PORT", "USER", "PASSWORD", "URL"})
			definition.Storage = StorageCapability{}
			definition.Provisioning = ProvisioningCapability{Implemented: true, Profiles: []ProvisioningProfile{{Name: "single-node-experimental", Versions: []SupportedVersion{{Version: ValkeyVersion, Image: ValkeyImage}}}}}
			return definition
		}(),
		func() ResourceTypeDefinition {
			definition := managedDefinition(TypeNATS, "NATS", 4222, ProtocolNATS, false, false, nil, []string{"HOST", "PORT", "URL"})
			definition.Provisioning = ProvisioningCapability{Implemented: true, Profiles: []ProvisioningProfile{{Name: "single-node-experimental", Versions: []SupportedVersion{{Version: NATSVersion, Image: NATSImage}}}}}
			for index := range definition.GeneratedValues {
				definition.GeneratedValues[index].Sensitivity = ValueNonSecret
			}
			return definition
		}(),
		managedDefinition(TypeRabbitMQ, "RabbitMQ", 5672, ProtocolAMQP, true, false, []string{"user", "password"}, []string{"HOST", "PORT", "URL"}),
	}
}

func Definition(resourceType Type) (ResourceTypeDefinition, bool) {
	for _, definition := range Definitions() {
		if definition.Type == resourceType {
			return definition, true
		}
	}
	return ResourceTypeDefinition{Type: resourceType, SupportTier: SupportUnsupported}, false
}

func Supports(resourceType Type, protocol Protocol) bool {
	definition, ok := Definition(resourceType)
	if !ok {
		return false
	}
	for _, supported := range definition.Protocols {
		if supported == protocol {
			return true
		}
	}
	return false
}
