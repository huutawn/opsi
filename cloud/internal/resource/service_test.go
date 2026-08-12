package resource

import (
	"context"
	"strings"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
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
			if err != nil || reused || resource.ProjectID != "project-1" || resource.EnvironmentID != "env-1" || resource.Lifecycle != resourcev1.LifecycleUnplaced || resource.Managed.Placement.RuntimeID != "" {
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
		{"create placement", func(request *resourcev1.CreateRequest) { request.Managed.Placement.RuntimeID = "runtime-1" }},
		{"plaintext config", func(request *resourcev1.CreateRequest) {
			request.Managed.ServiceConfig = map[string]string{"password": "plaintext"}
		}},
		{"missing credential ref", func(request *resourcev1.CreateRequest) { request.Managed.CredentialRefs = nil }},
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

func TestManagedPlacementUsesCanonicalRuntimeReference(t *testing.T) {
	service := testService()
	resource, _, err := service.Create(context.Background(), "project-1", "user-1", "create-placement", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	spec := *resource.Managed
	spec.Placement.RuntimeID = "runtime-1"
	updated, err := service.Update(context.Background(), "project-1", resource.ID, resourcev1.UpdateRequest{Managed: &spec})
	if err != nil || updated.Lifecycle != resourcev1.LifecyclePlanned || updated.Managed.Placement.RuntimeID != "runtime-1" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func testService() Service {
	return Service{Store: NewMemoryStore(), Scopes: testScopes{}, Now: func() time.Time { return time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC) }}
}

func managedRequest(resourceType resourcev1.Type) resourcev1.CreateRequest {
	persistent := resourceType != resourcev1.TypeNATS
	return resourcev1.CreateRequest{
		EnvironmentID: "env-1", Name: string(resourceType), Kind: resourcev1.KindManagedService, Type: resourceType,
		Managed: &resourcev1.ManagedSpec{Type: resourceType, Version: "default", Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20,
			Storage:        resourcev1.StorageRequest{Persistent: persistent, SizeBytes: map[bool]int64{true: 1 << 30}[persistent]},
			CredentialRefs: []resourcev1.SecretReference{{SecretID: "secret-" + string(resourceType)}}, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
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
