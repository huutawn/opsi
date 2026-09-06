package resourcev1

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

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
	KafkaVersion                = "4.3.1"
	KafkaImageVariant           = "4.3.1"
	KafkaImage                  = "docker.io/apache/kafka:4.3.1@sha256:77e3df9054047a88b520d0cc46e16696d3b22022e1d580aeccd2632df6532837"
	StoragePolicyDefault        = "default"
	DefaultPostgresStorageBytes = int64(5 << 30)
	DefaultKafkaStorageBytes    = int64(10 << 30)
	MaxManagedStorageBytes      = int64(1 << 50)
)

func Definitions() []ResourceTypeDefinition {
	return []ResourceTypeDefinition{
		func() ResourceTypeDefinition {
			definition := managedDefinition(TypePostgres, "PostgreSQL", 5432, ProtocolPostgres, true, true, []string{"user", "password", "database"}, []string{"HOST", "PORT", "NAME", "USER", "PASSWORD", "URL"})
			definition.GeneratedValues[2].Sensitivity = ValueNonSecret
			definition.Provisioning = ProvisioningCapability{
				Implemented: true,
				Profiles: []ProvisioningProfile{
					{
						Name: "single-node-experimental",
						ResourceDefaults: &ProfileResourceDefaults{
							CPUMillicores: 250,
							MemoryBytes:   256 << 20,
							StorageBytes:  DefaultPostgresStorageBytes,
						},
						Versions: []SupportedVersion{{Version: PostgresVersion, Image: PostgresImage}},
					},
				},
			}
			return definition
		}(),
		func() ResourceTypeDefinition {
			definition := managedDefinition(TypeKafka, "Kafka", 9092, ProtocolKafka, true, true, []string{"user", "password"}, []string{"HOST", "PORT", "BOOTSTRAP_SERVERS", "SECURITY_PROTOCOL", "SASL_MECHANISM", "USER", "PASSWORD"})
			definition.GeneratedValues[2].Sensitivity = ValueNonSecret // BOOTSTRAP_SERVERS
			definition.GeneratedValues[3].Sensitivity = ValueNonSecret // SECURITY_PROTOCOL
			definition.GeneratedValues[4].Sensitivity = ValueNonSecret // SASL_MECHANISM
			minPartitions, maxPartitions := int64(1), int64(100)
			minRetention, maxRetention := int64(1), int64(8760)
			definition.Provisioning = ProvisioningCapability{
				Implemented: true,
				Profiles: []ProvisioningProfile{
					{
						Name: "single-node-experimental",
						ResourceDefaults: &ProfileResourceDefaults{
							CPUMillicores: 500,
							MemoryBytes:   1 << 30,
							StorageBytes:  DefaultKafkaStorageBytes,
						},
						ConfigMetadata: []ConfigPropertyMetadata{
							{
								Name:        "num_partitions",
								Type:        ConfigTypeInt,
								Default:     "3",
								Description: "Default number of partitions for auto-created topics",
								Min:         &minPartitions,
								Max:         &maxPartitions,
							},
							{
								Name:        "retention_hours",
								Type:        ConfigTypeInt,
								Default:     "168",
								Description: "Log retention hours",
								Min:         &minRetention,
								Max:         &maxRetention,
							},
						},
						Versions: []SupportedVersion{{Version: KafkaVersion, Image: KafkaImage}},
					},
				},
			}
			return definition
		}(),
		func() ResourceTypeDefinition {
			definition := managedDefinition(TypeRedis, "Redis / Valkey-compatible", 6379, ProtocolRedis, true, false, []string{"user", "password"}, []string{"HOST", "PORT", "USER", "PASSWORD", "URL"})
			definition.Storage = StorageCapability{}
			definition.Provisioning = ProvisioningCapability{
				Implemented: true,
				Profiles: []ProvisioningProfile{
					{
						Name: "single-node-experimental",
						ResourceDefaults: &ProfileResourceDefaults{
							CPUMillicores: 100,
							MemoryBytes:   256 << 20,
							StorageBytes:  0,
						},
						Versions: []SupportedVersion{{Version: ValkeyVersion, Image: ValkeyImage}},
					},
				},
			}
			return definition
		}(),
		func() ResourceTypeDefinition {
			definition := managedDefinition(TypeNATS, "NATS", 4222, ProtocolNATS, false, false, nil, []string{"HOST", "PORT", "URL"})
			definition.Provisioning = ProvisioningCapability{
				Implemented: true,
				Profiles: []ProvisioningProfile{
					{
						Name: "single-node-experimental",
						ResourceDefaults: &ProfileResourceDefaults{
							CPUMillicores: 100,
							MemoryBytes:   128 << 20,
							StorageBytes:  0,
						},
						Versions: []SupportedVersion{{Version: NATSVersion, Image: NATSImage}},
					},
				},
			}
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

func ValidateKafkaServiceConfig(config map[string]string) error {
	for k, v := range config {
		switch k {
		case "num_partitions":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 || n > 100 {
				return errors.New("num_partitions must be an integer between 1 and 100")
			}
		case "retention_hours":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 || n > 8760 {
				return errors.New("retention_hours must be an integer between 1 and 8760")
			}
		default:
			return fmt.Errorf("unsupported Kafka service config key: %s", k)
		}
	}
	return nil
}

// ValidateKafkaTopics keeps declarative managed Kafka topics bounded and
// canonical. Topic order is part of the immutable resource specification.
func ValidateKafkaTopics(topics []KafkaTopic) error {
	previous := ""
	for _, topic := range topics {
		if topic.Name == "" || topic.Name != strings.TrimSpace(topic.Name) || len(topic.Name) > 249 || topic.Name == "." || topic.Name == ".." {
			return errors.New("topic name is invalid")
		}
		for _, value := range topic.Name {
			if !(value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '.' || value == '_' || value == '-') {
				return errors.New("topic name is invalid")
			}
		}
		if topic.Name <= previous {
			return errors.New("topics must be unique and sorted by name")
		}
		if topic.Partitions < 1 || topic.Partitions > 100 {
			return errors.New("topic partitions must be between 1 and 100")
		}
		if topic.RetentionHours < 0 || topic.RetentionHours > 8760 {
			return errors.New("topic retention hours must be between 1 and 8760")
		}
		previous = topic.Name
	}
	return nil
}
