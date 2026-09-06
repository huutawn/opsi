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
		TypeKafka:    {9092, ProtocolKafka},
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
	unknown, ok := Definition("cassandra")
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

func TestKafkaProvisioningAuthorityAndConfig(t *testing.T) {
	kafka, ok := Definition(TypeKafka)
	if !ok || !kafka.Provisioning.Implemented || len(kafka.Provisioning.Profiles) != 1 || len(kafka.Provisioning.Profiles[0].Versions) != 1 || !kafka.Storage.Required {
		t.Fatalf("kafka provisioning=%+v storage=%+v", kafka.Provisioning, kafka.Storage)
	}
	profile := kafka.Provisioning.Profiles[0]
	if profile.Name != "single-node-experimental" || profile.ResourceDefaults == nil || profile.ResourceDefaults.CPUMillicores != 500 || profile.ResourceDefaults.MemoryBytes != 1<<30 || profile.ResourceDefaults.StorageBytes != DefaultKafkaStorageBytes {
		t.Fatalf("kafka profile defaults=%+v", profile.ResourceDefaults)
	}
	if len(profile.ConfigMetadata) != 2 {
		t.Fatalf("kafka config metadata count=%d", len(profile.ConfigMetadata))
	}
	version := profile.Versions[0]
	if version.Version != KafkaVersion || version.Image != KafkaImage || !strings.Contains(version.Image, "@sha256:") || strings.Contains(version.Image, ":latest") {
		t.Fatalf("kafka version=%+v", version)
	}
	if err := ValidateKafkaServiceConfig(map[string]string{"num_partitions": "3", "retention_hours": "168"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := ValidateKafkaServiceConfig(map[string]string{"num_partitions": "0"}); err == nil {
		t.Fatal("invalid num_partitions accepted")
	}
	if err := ValidateKafkaServiceConfig(map[string]string{"retention_hours": "9000"}); err == nil {
		t.Fatal("invalid retention_hours accepted")
	}
	if err := ValidateKafkaServiceConfig(map[string]string{"auto_create_topics": "false"}); err == nil {
		t.Fatal("unsupported key accepted")
	}
}

func TestKafkaManagedResourceSpecValidation(t *testing.T) {
	spec := ManagedResourceSpec{
		SchemaVersion: ManagedResourceSpecSchemaVersion,
		ResourceID:    "res-kafka-1",
		ProjectID:     "project-1",
		EnvironmentID: "env-1",
		ResourceType:  TypeKafka,
		Profile:       "single-node-experimental",
		Version:       KafkaVersion,
		Image:         KafkaImage,
		Assignment: ManagedResourceAssignment{
			RuntimeID: "rt-1",
			NodeID:    "node-1",
			AgentID:   "agent-1",
		},
		Replicas:          1,
		CPUMillicores:     500,
		MemoryBytes:       1 << 30,
		Ports:             []ManagedResourcePort{{Name: "kafka", Port: 9092, Protocol: ProtocolKafka}},
		Storage:           StorageRequest{Persistent: true, SizeBytes: DefaultKafkaStorageBytes, PolicyRef: StoragePolicyDefault},
		Connection:        ManagedResourceConnection{ServiceName: "kafka-1", Host: "kafka-1.svc", Port: 9092, Protocol: ProtocolKafka},
		CredentialID:      "mrcred-kafka-1",
		ConfigurationHash: strings.Repeat("a", 64),
		TopologyRevision:  1,
		TopologyHash:      strings.Repeat("b", 64),
	}
	hash, err := spec.Hash()
	if err != nil {
		t.Fatal(err)
	}
	spec.SpecHash = hash
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid kafka spec rejected: %v", err)
	}

	// Non-persistent storage rejected
	badStorage := spec
	badStorage.Storage.Persistent = false
	badStorage.SpecHash, _ = badStorage.Hash()
	if err := badStorage.Validate(); err == nil {
		t.Fatal("non-persistent storage accepted for kafka")
	}

	// Missing credential rejected
	badCred := spec
	badCred.CredentialID = ""
	badCred.SpecHash, _ = badCred.Hash()
	if err := badCred.Validate(); err == nil {
		t.Fatal("missing credential accepted for kafka")
	}

	// Replicas != 1 rejected
	badReplicas := spec
	badReplicas.Replicas = 3
	badReplicas.SpecHash, _ = badReplicas.Hash()
	if err := badReplicas.Validate(); err == nil {
		t.Fatal("replicas > 1 accepted for kafka")
	}
}

func TestKafkaRetainedStorageDestroySpecValidation(t *testing.T) {
	spec := RetainedStorageDestroySpec{
		SchemaVersion:      RetainedStorageSchemaVersion,
		RetainedStorageID:  "rs-kafka-1",
		OriginalResourceID: "res-kafka-1",
		ProjectID:          "p1",
		EnvironmentID:      "e1",
		ResourceType:       TypeKafka,
		Namespace:          "opsi-kafka",
		PVCName:            "kafka-pvc",
		PVCUID:             "uid-1",
		PVName:             "pv-1",
		StorageClass:       "local-path",
		ReclaimPolicy:      "Retain",
		StorageHash:        strings.Repeat("c", 64),
		Assignment:         ManagedResourceAssignment{RuntimeID: "r1", NodeID: "n1", AgentID: "a1"},
		Revision:           1,
		Operation:          "destroy",
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("kafka retained storage destroy spec rejected: %v", err)
	}
}
