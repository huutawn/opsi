package resourcev1

import (
	"strings"
	"testing"
)

func TestPostgresManagedResourceSpecRequiresPinnedStatefulAuthority(t *testing.T) {
	spec := ManagedResourceSpec{
		SchemaVersion: ManagedResourceSpecSchemaVersion, ResourceID: "res-postgres", ProjectID: "project-1", EnvironmentID: "env-1",
		ResourceType: TypePostgres, Profile: "single-node-experimental", Version: PostgresVersion, Image: PostgresImage,
		Assignment: ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas:   1, CPUMillicores: 250, MemoryBytes: 256 << 20,
		Ports:        []ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: ProtocolPostgres}},
		Storage:      StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: StoragePolicyDefault},
		Connection:   ManagedResourceConnection{ServiceName: "opsi-mr-postgres", Host: "opsi-mr-postgres.opsi-project-1-env-1.svc.cluster.local", Port: 5432, Protocol: ProtocolPostgres, Database: "opsi"},
		CredentialID: "mrcred-postgres", ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ManagedResourceSpec){
		func(value *ManagedResourceSpec) { value.Storage.Persistent = false },
		func(value *ManagedResourceSpec) { value.Storage.SizeBytes = 0 },
		func(value *ManagedResourceSpec) { value.Storage.PolicyRef = "" },
		func(value *ManagedResourceSpec) { value.Version = "19" },
		func(value *ManagedResourceSpec) { value.Image = "postgres:latest" },
		func(value *ManagedResourceSpec) { value.CredentialID = "" },
		func(value *ManagedResourceSpec) { value.Connection.Database = "other" },
	} {
		invalid := spec
		mutate(&invalid)
		invalid.SpecHash, _ = invalid.Hash()
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid spec accepted: %+v", invalid)
		}
	}
}

func TestManagedResourceCredentialRequiresPostgresDatabase(t *testing.T) {
	credential := ManagedResourceCredential{CredentialID: "mrcred-1", Username: "opsi", Password: "secret"}
	if err := credential.ValidateFor(TypeRedis); err != nil {
		t.Fatal(err)
	}
	if err := credential.ValidateFor(TypePostgres); err == nil {
		t.Fatal("PostgreSQL credential without database was accepted")
	}
	credential.Database = "opsi"
	if err := credential.ValidateFor(TypePostgres); err != nil {
		t.Fatal(err)
	}
}
