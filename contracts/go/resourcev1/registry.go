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

func Definitions() []ResourceTypeDefinition {
	return []ResourceTypeDefinition{
		managedDefinition(TypePostgres, "PostgreSQL", 5432, ProtocolPostgres, true, true, []string{"user", "password", "database"}, []string{"HOST", "PORT", "USER", "PASSWORD", "DATABASE", "URL"}),
		managedDefinition(TypeRedis, "Redis / Valkey-compatible", 6379, ProtocolRedis, true, false, []string{"password"}, []string{"HOST", "PORT", "URL"}),
		managedDefinition(TypeNATS, "NATS", 4222, ProtocolNATS, false, false, []string{"credentials"}, []string{"HOST", "PORT", "URL"}),
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
