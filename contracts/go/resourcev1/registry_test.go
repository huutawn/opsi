package resourcev1

import (
	"strings"
	"testing"
)

func TestInitialDefinitionsAreExplicitlyExperimental(t *testing.T) {
	want := map[Type]struct {
		port     int
		protocol Protocol
	}{
		TypePostgres: {5432, ProtocolPostgres},
		TypeRedis:    {6379, ProtocolRedis},
		TypeNATS:     {4222, ProtocolNATS},
		TypeRabbitMQ: {5672, ProtocolAMQP},
	}
	definitions := Definitions()
	if len(definitions) != len(want) {
		t.Fatalf("definitions=%d", len(definitions))
	}
	for resourceType, expected := range want {
		definition, ok := Definition(resourceType)
		if !ok || definition.SupportTier != SupportExperimental || definition.DefaultPort != expected.port || !Supports(resourceType, expected.protocol) {
			t.Fatalf("definition %q=%+v ok=%t", resourceType, definition, ok)
		}
	}
	unknown, ok := Definition("kafka")
	if ok || unknown.SupportTier != SupportUnsupported {
		t.Fatalf("unknown=%+v ok=%t", unknown, ok)
	}
}

func TestGeneratedValuesClassifySecrets(t *testing.T) {
	definitions := Definitions()
	definitions[0].Protocols[0] = ProtocolHTTP
	definition, _ := Definition(TypePostgres)
	values := map[string]ValueSensitivity{}
	for _, value := range definition.GeneratedValues {
		values[value.Name] = value.Sensitivity
	}
	if definition.Protocols[0] != ProtocolPostgres || values["HOST"] != ValueNonSecret || values["PORT"] != ValueNonSecret || values["PASSWORD"] != ValueSecret || values["URL"] != ValueSecret {
		t.Fatalf("values=%v", values)
	}
}

func TestManagedProvisioningAuthorityIsPinnedAndUnique(t *testing.T) {
	postgres, ok := Definition(TypePostgres)
	if !ok || !postgres.Provisioning.Implemented || len(postgres.Provisioning.Profiles) != 1 || len(postgres.Provisioning.Profiles[0].Versions) != 1 || !postgres.Storage.Required {
		t.Fatalf("postgres provisioning=%+v storage=%+v", postgres.Provisioning, postgres.Storage)
	}
	postgresVersion := postgres.Provisioning.Profiles[0].Versions[0]
	if postgresVersion.Version != PostgresVersion || postgresVersion.Image != PostgresImage || !strings.Contains(postgresVersion.Image, ":"+PostgresImageVariant+"@sha256:") || strings.Contains(postgresVersion.Image, ":latest") {
		t.Fatalf("postgres version=%+v", postgresVersion)
	}
	definition, ok := Definition(TypeNATS)
	if !ok || !definition.Provisioning.Implemented || len(definition.Provisioning.Profiles) != 1 || len(definition.Provisioning.Profiles[0].Versions) != 1 {
		t.Fatalf("nats provisioning=%+v", definition.Provisioning)
	}
	version := definition.Provisioning.Profiles[0].Versions[0]
	if version.Version != NATSVersion || version.Image != NATSImage || strings.Contains(version.Image, ":latest") {
		t.Fatalf("nats version=%+v", version)
	}
	redis, ok := Definition(TypeRedis)
	if !ok || !redis.Provisioning.Implemented || len(redis.Provisioning.Profiles) != 1 || len(redis.Provisioning.Profiles[0].Versions) != 1 {
		t.Fatalf("redis provisioning=%+v", redis.Provisioning)
	}
	valkey := redis.Provisioning.Profiles[0].Versions[0]
	if valkey.Version != ValkeyVersion || valkey.Image != ValkeyImage || strings.Contains(valkey.Image, ":latest") || redis.Storage.Supported {
		t.Fatalf("valkey version=%+v storage=%+v", valkey, redis.Storage)
	}
	for _, resourceType := range []Type{TypeRabbitMQ} {
		definition, _ := Definition(resourceType)
		if definition.Provisioning.Implemented {
			t.Fatalf("%s unexpectedly provisionable", resourceType)
		}
	}
}
