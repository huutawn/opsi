package resource

import (
	"context"
	"strings"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type testScopes struct{}

func (testScopes) EnvironmentExists(_ context.Context, projectID, environmentID string) (bool, error) {
	return projectID == "project-1" && environmentID == "env-1", nil
}
func (testScopes) RuntimeBelongs(_ context.Context, projectID, environmentID, runtimeID string) (bool, error) {
	return projectID == "project-1" && environmentID == "env-1" && runtimeID == "runtime-1", nil
}
func (testScopes) ApplicationBelongs(_ context.Context, projectID, environmentID, applicationID string) (bool, error) {
	return projectID == "project-1" && environmentID == "env-1" && applicationID == "app-1", nil
}

func TestManagedResourcesAndBindings(t *testing.T) {
	service := testService()
	protocols := map[resourcev1.Type]resourcev1.Protocol{
		resourcev1.TypePostgres: resourcev1.ProtocolPostgres,
		resourcev1.TypeRedis:    resourcev1.ProtocolRedis,
		resourcev1.TypeNATS:     resourcev1.ProtocolNATS,
		resourcev1.TypeRabbitMQ: resourcev1.ProtocolAMQP,
	}
	for resourceType, protocol := range protocols {
		t.Run(string(resourceType), func(t *testing.T) {
			request := managedRequest(resourceType)
			resource, reused, err := service.Create(context.Background(), "project-1", "user-1", "create-"+string(resourceType), request)
			if err != nil || reused || resource.ProjectID != "project-1" || resource.EnvironmentID != "env-1" || resource.Lifecycle != resourcev1.LifecycleUnplaced || resource.Runtime != nil {
				t.Fatalf("resource=%+v reused=%t err=%v", resource, reused, err)
			}
			replay, reused, err := service.Create(context.Background(), "project-1", "user-1", "create-"+string(resourceType), request)
			if err != nil || !reused || replay.ID != resource.ID {
				t.Fatalf("replay=%+v reused=%t err=%v", replay, reused, err)
			}
			conflict := request
			conflict.Name += "-other"
			if _, _, err := service.Create(context.Background(), "project-1", "user-1", "create-"+string(resourceType), conflict); err == nil {
				t.Fatal("idempotency key accepted another payload")
			}
			binding, reused, err := service.CreateBinding(context.Background(), "project-1", "bind-"+string(resourceType), resourcev1.CreateBindingRequest{
				EnvironmentID: "env-1", Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"},
				Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: resource.ID}, Protocol: protocol, LogicalName: strings.ToUpper(string(resourceType)),
			})
			if err != nil || reused || binding.Protocol != protocol {
				t.Fatalf("binding=%+v reused=%t err=%v", binding, reused, err)
			}
			assertReferences(t, binding.RuntimeRefs)
		})
	}
}

func TestRedisBindingEmitsTypedSecretReferences(t *testing.T) {
	service := testService()
	request := managedRequest(resourcev1.TypeRedis)
	value, _, err := service.Create(context.Background(), "project-1", "user-1", "redis-bind", request)
	if err != nil {
		t.Fatal(err)
	}
	value.Lifecycle = resourcev1.LifecycleReady
	value.Runtime = &resourcev1.ManagedResourceRuntime{Spec: resourcev1.ManagedResourceSpec{SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: value.ID, ProjectID: "project-1", EnvironmentID: "env-1", ResourceType: resourcev1.TypeRedis, Profile: "single-node-experimental", Version: resourcev1.ValkeyVersion, Image: resourcev1.ValkeyImage, CredentialID: "mrcred-" + value.ID, Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"}, Replicas: 1, CPUMillicores: 100, MemoryBytes: 64 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "redis", Port: 6379, Protocol: resourcev1.ProtocolRedis}}, Connection: resourcev1.ManagedResourceConnection{ServiceName: "redis", Host: "redis.internal", Port: 6379, Protocol: resourcev1.ProtocolRedis}}, Evidence: &resourcev1.ManagedResourceEvidence{WorkloadReady: true, PodReady: true, ServiceReady: true, SecretReady: true, AuthReady: true, Image: resourcev1.ValkeyImage, ImageID: resourcev1.ValkeyImage, AvailableReplicas: 1}}
	value.Runtime.Spec.SpecHash, _ = value.Runtime.Spec.Hash()
	value.Runtime.Spec.TopologyRevision = 1
	value.Runtime.Spec.TopologyHash = strings.Repeat("a", 64)
	value.Runtime.Spec.ConfigurationHash = strings.Repeat("b", 64)
	value.Runtime.Spec.SpecHash, _ = value.Runtime.Spec.Hash()
	value.Runtime.Evidence.ObservedSpecHash = value.Runtime.Spec.SpecHash
	if _, err := service.Store.Update(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	binding, _, err := service.CreateBinding(context.Background(), "project-1", "redis-binding", resourcev1.CreateBindingRequest{EnvironmentID: "env-1", Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"}, Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: value.ID}, Protocol: resourcev1.ProtocolRedis, LogicalName: "CACHE"})
	if err != nil || len(binding.RuntimeRefs) != 5 || binding.RuntimeRefs[2].Sensitivity != resourcev1.ValueSecret || binding.RuntimeRefs[2].Value != "" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
}

func TestManagedResourceValidation(t *testing.T) {
	service := testService()
	cases := []struct {
		name   string
		mutate func(*resourcev1.CreateRequest)
	}{
		{"unknown", func(request *resourcev1.CreateRequest) { request.Type, request.Managed.Type = "kafka", "kafka" }},
		{"cpu", func(request *resourcev1.CreateRequest) { request.Managed.CPUMillicores = 0 }},
		{"memory", func(request *resourcev1.CreateRequest) { request.Managed.MemoryBytes = 0 }},
		{"storage size", func(request *resourcev1.CreateRequest) { request.Managed.Storage.SizeBytes = 0 }},
		{"stateful storage", func(request *resourcev1.CreateRequest) { request.Managed.Storage.Persistent = false }},
		{"plaintext config", func(request *resourcev1.CreateRequest) {
			request.Managed.ServiceConfig = map[string]string{"password": "plaintext"}
		}},
		{"supplied credential ref", func(request *resourcev1.CreateRequest) {
			request.Managed.CredentialRefs = []resourcev1.SecretReference{{SecretID: "user-supplied"}}
		}},
		{"storage policy", func(request *resourcev1.CreateRequest) { request.Managed.Storage.PolicyRef = "unsupported" }},
		{"version upgrade", func(request *resourcev1.CreateRequest) { request.Managed.Version = "19" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := managedRequest(resourcev1.TypePostgres)
			tc.mutate(&request)
			if _, _, err := service.Create(context.Background(), "project-1", "user-1", "invalid-"+tc.name, request); err == nil {
				t.Fatal("invalid resource was accepted")
			}
		})
	}
}

func TestBindingRejectsUnsupportedProtocol(t *testing.T) {
	service := testService()
	resource, _, err := service.Create(context.Background(), "project-1", "user-1", "postgres", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.CreateBinding(context.Background(), "project-1", "bad-protocol", resourcev1.CreateBindingRequest{
		EnvironmentID: "env-1", Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"},
		Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: resource.ID}, Protocol: resourcev1.ProtocolHTTP, LogicalName: "DATABASE",
	})
	if err == nil {
		t.Fatal("unsupported protocol was accepted")
	}
}

func TestExternalResourceIsConfiguredNotHealthy(t *testing.T) {
	service := testService()
	resource, _, err := service.Create(context.Background(), "project-1", "user-1", "external", resourcev1.CreateRequest{
		EnvironmentID: "env-1", Name: "neon", Kind: resourcev1.KindExternalResource, Provider: "neon", Type: resourcev1.TypePostgres,
		External: &resourcev1.ExternalSpec{Protocol: resourcev1.ProtocolPostgres, Endpoint: "db.example.test", Port: 5432, CredentialRef: &resourcev1.SecretReference{SecretID: "secret-neon"}},
	})
	if err != nil || resource.Lifecycle != resourcev1.LifecycleConfigured || resource.Lifecycle == resourcev1.LifecycleReady {
		t.Fatalf("resource=%+v err=%v", resource, err)
	}
}

func TestManagedUpdatePreservesUnplacedAuthority(t *testing.T) {
	service := testService()
	resource, _, err := service.Create(context.Background(), "project-1", "user-1", "create-placement", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	spec := *resource.Managed
	spec.CPUMillicores = 500
	updated, err := service.Update(context.Background(), "project-1", resource.ID, resourcev1.UpdateRequest{Managed: &spec})
	if err != nil || updated.Lifecycle != resourcev1.LifecycleUnplaced || updated.Managed.CPUMillicores != 500 || updated.Runtime != nil {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestPostgresManagedUpdatePreservesStorageCredentialAndVersion(t *testing.T) {
	service := testService()
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "postgres-update", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("c", 64), Assignments: []topologyv1.Assignment{{ServiceKey: created.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	planned, _ := service.Get(context.Background(), "project-1", created.ID)
	credentialID := planned.Runtime.Spec.CredentialID
	next := *planned.Managed
	next.CPUMillicores = 500
	updated, err := service.Update(context.Background(), "project-1", created.ID, resourcev1.UpdateRequest{Managed: &next})
	if err != nil || updated.Runtime.Spec.Storage != planned.Runtime.Spec.Storage || updated.Runtime.Spec.CredentialID != credentialID || updated.Runtime.Spec.Version != resourcev1.PostgresVersion {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*resourcev1.ManagedSpec)
		code string
	}{
		{"grow", func(spec *resourcev1.ManagedSpec) { spec.Storage.SizeBytes++ }, resourcev1.FailureStorageResizeUnsupported},
		{"shrink", func(spec *resourcev1.ManagedSpec) { spec.Storage.SizeBytes-- }, resourcev1.FailureStorageResizeUnsupported},
		{"policy", func(spec *resourcev1.ManagedSpec) { spec.Storage.PolicyRef = "other" }, resourcev1.FailureStorageInvalid},
		{"version", func(spec *resourcev1.ManagedSpec) { spec.Version = "18.7" }, resourcev1.FailureVersionUpgradeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *updated.Managed
			tc.edit(&candidate)
			if _, err := service.Update(context.Background(), "project-1", created.ID, resourcev1.UpdateRequest{Managed: &candidate}); err == nil || !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func testService() Service {
	return Service{Store: NewMemoryStore(), Scopes: testScopes{}, Credentials: NewMemoryCredentialAuthority(), Now: func() time.Time { return time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC) }}
}

func managedRequest(resourceType resourcev1.Type) resourcev1.CreateRequest {
	persistent := resourceType != resourcev1.TypeNATS && resourceType != resourcev1.TypeRedis
	var refs []resourcev1.SecretReference
	if resourceType != resourcev1.TypeRedis && resourceType != resourcev1.TypePostgres {
		refs = []resourcev1.SecretReference{{SecretID: "secret-" + string(resourceType)}}
	}
	storage := resourcev1.StorageRequest{Persistent: persistent, SizeBytes: map[bool]int64{true: 1 << 30}[persistent]}
	if resourceType == resourcev1.TypePostgres {
		storage.PolicyRef = resourcev1.StoragePolicyDefault
	}
	return resourcev1.CreateRequest{
		EnvironmentID: "env-1", Name: string(resourceType), Kind: resourcev1.KindManagedService, Type: resourceType,
		Managed: &resourcev1.ManagedSpec{Type: resourceType, Version: "default", Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20,
			Storage:        storage,
			CredentialRefs: refs, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
	}
}

func assertReferences(t *testing.T, references []resourcev1.RuntimeConnectionReference) {
	t.Helper()
	for _, reference := range references {
		if reference.Sensitivity == resourcev1.ValueSecret && (reference.Value != "" || reference.SecretRef == nil) {
			t.Fatalf("secret reference leaked or missing: %+v", reference)
		}
		if reference.Sensitivity == resourcev1.ValueNonSecret && reference.SecretRef != nil {
			t.Fatalf("non-secret uses secret reference: %+v", reference)
		}
	}
}
