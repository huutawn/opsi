package resource

import (
	"context"
	"strings"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

type mockConfigScope struct {
	configs map[string]serviceconfigurationv1.Configuration
}

func (m mockConfigScope) EnvironmentExists(_ context.Context, projectID, environmentID string) (bool, error) {
	return true, nil
}
func (m mockConfigScope) RuntimeBelongs(_ context.Context, projectID, environmentID, runtimeID string) (bool, error) {
	return true, nil
}
func (m mockConfigScope) ApplicationBelongs(_ context.Context, projectID, environmentID, applicationID string) (bool, error) {
	return true, nil
}
func (m mockConfigScope) GetServiceConfiguration(projectID, serviceID string) (serviceconfigurationv1.Configuration, error) {
	if cfg, ok := m.configs[serviceID]; ok {
		return cfg, nil
	}
	return serviceconfigurationv1.Configuration{}, nil
}

func TestApplicationRuntimeConfiguration_DependencyRealization(t *testing.T) {
	store := NewMemoryStore()
	creds := NewMemoryCredentialAuthority()
	service := Service{Store: store, Credentials: creds, Now: func() time.Time { return time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC) }}

	now := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	// 1. Create PostgreSQL Managed Resource
	pgSpec := resourcev1.ManagedResourceSpec{
		SchemaVersion:    resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:       "res-pg",
		ProjectID:        "proj-1",
		EnvironmentID:    "env-1",
		ResourceType:     resourcev1.TypePostgres,
		Profile:          "single-node-experimental",
		Version:          resourcev1.PostgresVersion,
		Image:            resourcev1.PostgresImage,
		Assignment:       resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas:         1,
		CPUMillicores:    250,
		MemoryBytes:      256 * 1024 * 1024,
		Storage:          resourcev1.StorageRequest{Persistent: true, SizeBytes: 10 * 1024 * 1024 * 1024, PolicyRef: resourcev1.StoragePolicyDefault},
		Ports:            []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}},
		Connection:       resourcev1.ManagedResourceConnection{ServiceName: "postgres-svc", Host: "postgres.local", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"},
		CredentialID:     "mrcred-res-pg",
		TopologyRevision: 1,
	}
	pgSpec.ConfigurationHash = strings.Repeat("a", 64)
	pgSpec.TopologyHash = strings.Repeat("b", 64)
	pgSpec.SpecHash, _ = pgSpec.Hash()

	pgResource := resourcev1.Resource{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "res-pg",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Name:          "main-pg",
		Kind:          resourcev1.KindManagedService,
		Type:          resourcev1.TypePostgres,
		Lifecycle:     resourcev1.LifecycleReady,
		Runtime: &resourcev1.ManagedResourceRuntime{
			Spec: pgSpec,
			Evidence: &resourcev1.ManagedResourceEvidence{
				ObservedSpecHash:  pgSpec.SpecHash,
				WorkloadReady:     true,
				PodReady:          true,
				ServiceReady:      true,
				SecretReady:       true,
				AuthReady:         true,
				StorageReady:      true,
				VolumeMounted:     true,
				PVCName:           "pvc-1",
				PVName:            "pv-1",
				Image:             pgSpec.Image,
				ImageID:           pgSpec.Image,
				AvailableReplicas: 1,
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, _, err := store.Create(context.Background(), pgResource, "pg-key", "payload"); err != nil {
		t.Fatal(err)
	}

	// 2. Create Valkey Managed Resource
	valkeySpec := resourcev1.ManagedResourceSpec{
		SchemaVersion:    resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:       "res-valkey",
		ProjectID:        "proj-1",
		EnvironmentID:    "env-1",
		ResourceType:     resourcev1.TypeRedis,
		Profile:          "single-node-experimental",
		Version:          resourcev1.ValkeyVersion,
		Image:            resourcev1.ValkeyImage,
		Assignment:       resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas:         1,
		CPUMillicores:    250,
		MemoryBytes:      256 * 1024 * 1024,
		Ports:            []resourcev1.ManagedResourcePort{{Name: "redis", Port: 6379, Protocol: resourcev1.ProtocolRedis}},
		Connection:       resourcev1.ManagedResourceConnection{ServiceName: "valkey-svc", Host: "valkey.local", Port: 6379, Protocol: resourcev1.ProtocolRedis},
		CredentialID:     "mrcred-res-valkey",
		TopologyRevision: 1,
	}
	valkeySpec.ConfigurationHash = strings.Repeat("c", 64)
	valkeySpec.TopologyHash = strings.Repeat("d", 64)
	valkeySpec.SpecHash, _ = valkeySpec.Hash()

	valkeyResource := resourcev1.Resource{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "res-valkey",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Name:          "main-cache",
		Kind:          resourcev1.KindManagedService,
		Type:          resourcev1.TypeRedis,
		Lifecycle:     resourcev1.LifecycleReady,
		Runtime: &resourcev1.ManagedResourceRuntime{
			Spec: valkeySpec,
			Evidence: &resourcev1.ManagedResourceEvidence{
				ObservedSpecHash:  valkeySpec.SpecHash,
				WorkloadReady:     true,
				PodReady:          true,
				ServiceReady:      true,
				SecretReady:       true,
				AuthReady:         true,
				Image:             valkeySpec.Image,
				ImageID:           valkeySpec.Image,
				AvailableReplicas: 1,
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, _, err := store.Create(context.Background(), valkeyResource, "valkey-key", "payload"); err != nil {
		t.Fatal(err)
	}

	configs := map[string]serviceconfigurationv1.Configuration{}
	scope := mockConfigScope{configs: configs}
	service.Scopes = scope

	// 3. Create Bindings
	pgBinding, _, err := service.CreateBinding(context.Background(), "proj-1", "bind-pg-key", resourcev1.CreateBindingRequest{
		EnvironmentID: "env-1",
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: "res-pg"},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   "database",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Complete binding lifecycle
	pgBinding.Lifecycle = resourcev1.LifecycleReady
	pgBinding, _ = store.UpdateBinding(context.Background(), pgBinding)

	valkeyBinding, _, err := service.CreateBinding(context.Background(), "proj-1", "bind-valkey-key", resourcev1.CreateBindingRequest{
		EnvironmentID: "env-1",
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: "res-valkey"},
		Protocol:      resourcev1.ProtocolRedis,
		LogicalName:   "cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	valkeyBinding.Lifecycle = resourcev1.LifecycleReady
	valkeyBinding, _ = store.UpdateBinding(context.Background(), valkeyBinding)

	configs["app-1"] = serviceconfigurationv1.Configuration{
		ServiceConfigurationDraft: serviceconfigurationv1.ServiceConfigurationDraft{
			Dependencies: []serviceconfigurationv1.ApplicationDependency{
				{
					LogicalName:    "database",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-pg",
					Protocol:       "postgres",
					Required:       true,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_DATABASE_URL", SymbolicSource: "connection.url"},
						{EnvName: "DB_HOST", SymbolicSource: "resource.host"},
						{EnvName: "DB_PORT", SymbolicSource: "resource.port"},
						{EnvName: "DB_NAME", SymbolicSource: "credential.database"},
						{EnvName: "DB_USER", SymbolicSource: "credential.username"},
						{EnvName: "DB_PASSWORD", SymbolicSource: "credential.password"},
					},
				},
				{
					LogicalName:    "cache",
					TargetKind:     "managed_resource",
					TargetIdentity: "res-valkey",
					Protocol:       "redis",
					Required:       false,
					InjectionPhase: "runtime",
					InjectionMappings: []serviceconfigurationv1.DependencyInjectionMapping{
						{EnvName: "APP_REDIS_URL", SymbolicSource: "connection.url"},
						{EnvName: "CACHE_HOST", SymbolicSource: "resource.host"},
						{EnvName: "CACHE_PORT", SymbolicSource: "resource.port"},
						{EnvName: "CACHE_PASSWORD", SymbolicSource: "credential.password"},
					},
				},
			},
			ResourceBindings: []serviceconfigurationv1.ResourceBinding{
				{LogicalName: "database", BindingID: pgBinding.ID},
				{LogicalName: "cache", BindingID: valkeyBinding.ID},
			},
		},
	}

	valkeyCred, err := creds.Ensure(context.Background(), "mrcred-res-valkey")
	if err != nil {
		t.Fatal(err)
	}
	pgCred, err := creds.Get(context.Background(), pgBinding.CredentialID)
	if err != nil {
		t.Fatal(err)
	}

	// 4. Test ApplicationRuntimeConfiguration projection
	envVars, secRefs, err := service.ApplicationRuntimeConfiguration(context.Background(), "proj-1", "env-1", "app-1")
	if err != nil {
		t.Fatalf("ApplicationRuntimeConfiguration failed: %v", err)
	}

	// Check non-secret env vars
	envMap := map[string]string{}
	for _, env := range envVars {
		envMap[env.Name] = env.Value
	}
	if envMap["DB_HOST"] != "postgres.local" || envMap["DB_PORT"] != "5432" || envMap["DB_NAME"] != "opsi" {
		t.Fatalf("unexpected DB env vars: %+v", envMap)
	}
	if envMap["CACHE_HOST"] != "valkey.local" || envMap["CACHE_PORT"] != "6379" {
		t.Fatalf("unexpected Cache env vars: %+v", envMap)
	}

	// Check secret references
	secMap := map[string]string{}
	for _, sec := range secRefs {
		secMap[sec.EnvName] = sec.SecretID
	}
	if secMap["APP_DATABASE_URL"] != pgBinding.CredentialID || secMap["DB_USER"] != pgBinding.CredentialID || secMap["DB_PASSWORD"] != pgBinding.CredentialID {
		t.Fatalf("unexpected DB secret references: %+v", secMap)
	}
	if secMap["APP_REDIS_URL"] != "mrcred-res-valkey" && secMap["APP_REDIS_URL"] != valkeyBinding.CredentialID {
		t.Fatalf("unexpected Redis secret references: %+v", secMap)
	}

	// 5. Test ResolveSecretMaterials
	materials, err := service.ResolveSecretMaterials(context.Background(), "proj-1", "", secRefs)
	if err != nil {
		t.Fatalf("ResolveSecretMaterials failed: %v", err)
	}

	materialValues := map[string]string{}
	for _, mat := range materials {
		for k, v := range mat.Values {
			materialValues[k] = v
		}
	}

	// Assert custom Postgres values
	if materialValues["DB_USER"] != pgBinding.RoleName {
		t.Fatalf("expected DB_USER=%s, got %s", pgBinding.RoleName, materialValues["DB_USER"])
	}
	if materialValues["DB_PASSWORD"] != pgCred.Password {
		t.Fatalf("expected DB_PASSWORD=%s, got %s", pgCred.Password, materialValues["DB_PASSWORD"])
	}
	expectedPGURL := "postgres://" + pgBinding.RoleName + ":" + pgCred.Password + "@postgres.local:5432/opsi?sslmode=disable"
	if materialValues["APP_DATABASE_URL"] != expectedPGURL {
		t.Fatalf("expected APP_DATABASE_URL=%s, got %s", expectedPGURL, materialValues["APP_DATABASE_URL"])
	}

	// Assert custom Valkey values
	if materialValues["CACHE_PASSWORD"] != valkeyCred.Password {
		t.Fatalf("expected CACHE_PASSWORD=%s, got %s", valkeyCred.Password, materialValues["CACHE_PASSWORD"])
	}
	if !strings.HasPrefix(materialValues["APP_REDIS_URL"], "redis://") || !strings.Contains(materialValues["APP_REDIS_URL"], valkeyCred.Password+"@valkey.local:6379") {
		t.Fatalf("expected valid Redis URL, got %s", materialValues["APP_REDIS_URL"])
	}
}
