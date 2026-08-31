package resource

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type staticTarget struct{}

func (staticTarget) ResolveManagedResourceTarget(context.Context, string, string, string) (resourcev1.ManagedResourceAssignment, error) {
	return resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"}, nil
}

func TestNATSCompilerLeaseReadinessDeleteAndBinding(t *testing.T) {
	service := testService()
	request := managedRequest(resourcev1.TypeNATS)
	request.Managed.CredentialRefs = nil
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "nats-runtime", request)
	if err != nil || created.Lifecycle != resourcev1.LifecycleUnplaced {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("a", 64), Assignments: []topologyv1.Assignment{{ServiceKey: created.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	planned, _ := service.Get(context.Background(), "project-1", created.ID)
	if planned.Lifecycle != resourcev1.LifecyclePlanned || planned.Runtime == nil || planned.Runtime.Spec.Image != resourcev1.NATSImage || planned.Runtime.Spec.Connection.Port != 4222 || planned.Runtime.Spec.Storage.Persistent {
		t.Fatalf("planned=%+v", planned)
	}
	if !strings.HasPrefix(planned.Runtime.Spec.Connection.Host, planned.Runtime.Spec.Connection.ServiceName+".") {
		t.Fatalf("service identity mismatch: %+v", planned.Runtime.Spec.Connection)
	}
	if refs := runtimeRefs(planned); len(refs) != 0 {
		t.Fatalf("planned refs=%+v", refs)
	}
	lease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok || lease.Action != "apply" || lease.Spec.SpecHash == "" {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	evidence := &resourcev1.ManagedResourceEvidence{ObservedSpecHash: lease.Spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, Image: lease.Spec.Image, ImageID: lease.Spec.Image, AvailableReplicas: 1, ObservedAt: time.Now().UTC()}
	ready, err := service.CompleteManaged(context.Background(), "project-1", created.ID, ManagedResult{Status: "ready", LeaseToken: lease.LeaseToken, Evidence: evidence})
	if err != nil || ready.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	binding, _, err := service.CreateBinding(context.Background(), "project-1", "bind-nats-runtime", resourcev1.CreateBindingRequest{EnvironmentID: "env-1", Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"}, Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: created.ID}, Protocol: resourcev1.ProtocolNATS, LogicalName: "MESSAGING"})
	if err != nil || len(binding.RuntimeRefs) != 3 {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	env, err := service.ApplicationEnvironment(context.Background(), "project-1", "env-1", "app-1")
	if err != nil || len(env) != 3 || env[0].Name != "MESSAGING_HOST" || env[1].Name != "MESSAGING_PORT" || env[2].Name != "MESSAGING_URL" || env[0].Value != planned.Runtime.Spec.Connection.Host {
		t.Fatalf("env=%+v err=%v", env, err)
	}
	deleting, err := service.DeleteIntent(context.Background(), "project-1", created.ID, "user-1")
	if err != nil || deleting.Lifecycle != resourcev1.LifecycleDeleting {
		t.Fatalf("deleting=%+v err=%v", deleting, err)
	}
	deleteLease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok || deleteLease.Action != "delete" {
		t.Fatalf("delete lease=%+v ok=%t err=%v", deleteLease, ok, err)
	}
	_, err = service.CompleteManaged(context.Background(), "project-1", created.ID, ManagedResult{Status: "deleted", LeaseToken: deleteLease.LeaseToken, Evidence: &resourcev1.ManagedResourceEvidence{ObservedSpecHash: deleteLease.Spec.SpecHash, Deleted: true, ObservedAt: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), "project-1", created.ID); err != ErrNotFound {
		t.Fatalf("resource still exists: %v", err)
	}
}

func TestUnplacedManagedResourceDeletesWithoutRuntimeAuthority(t *testing.T) {
	service := testService()
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "unplaced-delete", managedRequest(resourcev1.TypeRedis))
	if err != nil || created.Runtime != nil || created.Lifecycle != resourcev1.LifecycleUnplaced {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	deleted, err := service.DeleteIntent(context.Background(), "project-1", created.ID, "user-1")
	if err != nil || deleted.Lifecycle != resourcev1.LifecycleDeleting {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	if _, err := service.Get(context.Background(), "project-1", created.ID); err != ErrNotFound {
		t.Fatalf("resource still exists: %v", err)
	}
}

func TestReconcileTopologyPreservesUnchangedInFlightAndReadyResource(t *testing.T) {
	service := testService()
	request := managedRequest(resourcev1.TypeNATS)
	request.Managed.CredentialRefs = nil
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "nats-idempotent-reconcile", request)
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("9", 64), Assignments: []topologyv1.Assignment{{ServiceKey: created.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	inFlight, err := service.Get(context.Background(), "project-1", created.ID)
	if err != nil || inFlight.Lifecycle != resourcev1.LifecycleProvisioning || inFlight.Runtime == nil || inFlight.Runtime.LeaseToken != lease.LeaseToken {
		t.Fatalf("in-flight resource was reset: resource=%+v err=%v", inFlight, err)
	}
	evidence := &resourcev1.ManagedResourceEvidence{ObservedSpecHash: lease.Spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, Image: lease.Spec.Image, ImageID: lease.Spec.Image, AvailableReplicas: 1, ObservedAt: time.Now().UTC()}
	ready, err := service.CompleteManaged(context.Background(), "project-1", created.ID, ManagedResult{Status: "ready", LeaseToken: lease.LeaseToken, Evidence: evidence})
	if err != nil || ready.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := service.Get(context.Background(), "project-1", created.ID)
	if err != nil || unchanged.Lifecycle != resourcev1.LifecycleReady || unchanged.Runtime == nil || unchanged.Runtime.Evidence == nil || unchanged.Runtime.Evidence.ObservedSpecHash != lease.Spec.SpecHash {
		t.Fatalf("ready resource was reset: resource=%+v err=%v", unchanged, err)
	}
	if next, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1"); err != nil || ok {
		t.Fatalf("unchanged ready resource was leased again: lease=%+v ok=%t err=%v", next, ok, err)
	}
}

func TestPostgresCompilerGeneratesStableCredentialAndStorageAuthority(t *testing.T) {
	service := testService()
	postgres, _, err := service.Create(context.Background(), "project-1", "user-1", "postgres-runtime", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	if postgres.Lifecycle != resourcev1.LifecycleUnplaced || postgres.Runtime != nil {
		t.Fatalf("created=%+v", postgres)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("b", 64), Assignments: []topologyv1.Assignment{{ServiceKey: postgres.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	planned, _ := service.Get(context.Background(), "project-1", postgres.ID)
	if planned.Lifecycle != resourcev1.LifecyclePlanned || planned.Runtime == nil || planned.Runtime.Spec.ResourceType != resourcev1.TypePostgres || planned.Runtime.Spec.Version != resourcev1.PostgresVersion || planned.Runtime.Spec.Image != resourcev1.PostgresImage || planned.Runtime.Spec.Storage.PolicyRef != resourcev1.StoragePolicyDefault || !planned.Runtime.Spec.Storage.Persistent || planned.Runtime.Spec.CredentialID == "" || planned.Runtime.Spec.Connection.Protocol != resourcev1.ProtocolPostgres || planned.Runtime.Spec.Connection.Port != 5432 || planned.Runtime.Spec.Connection.URL != "" {
		t.Fatalf("planned=%+v", planned)
	}
	first, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok || first.Credential == nil || first.Credential.Database == "" || first.Credential.CredentialID != planned.Runtime.Spec.CredentialID {
		t.Fatalf("lease=%+v ok=%t err=%v", first, ok, err)
	}
	now := time.Now().UTC()
	ready, err := service.CompleteManaged(context.Background(), "project-1", postgres.ID, ManagedResult{Status: "ready", LeaseToken: first.LeaseToken, Evidence: &resourcev1.ManagedResourceEvidence{ObservedSpecHash: first.Spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, SecretReady: true, AuthReady: true, StorageReady: true, VolumeMounted: true, PVCName: "pvc", PVName: "pv", Image: first.Spec.Image, ImageID: first.Spec.Image, AvailableReplicas: 1, ObservedAt: now}})
	if err != nil || ready.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	if _, err := service.DeleteIntent(context.Background(), "project-1", postgres.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	deleteLease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok || deleteLease.Action != "delete" {
		t.Fatalf("delete lease=%+v ok=%t err=%v", deleteLease, ok, err)
	}
	unsafeEvidence := &resourcev1.ManagedResourceEvidence{ObservedSpecHash: deleteLease.Spec.SpecHash, Deleted: true, ObservedAt: now}
	if _, err := service.CompleteManaged(context.Background(), "project-1", postgres.ID, ManagedResult{Status: "deleted", LeaseToken: deleteLease.LeaseToken, Evidence: unsafeEvidence}); err == nil || !strings.Contains(err.Error(), resourcev1.FailureRetainedStorageIdentityMismatch) {
		t.Fatalf("unsafe delete err=%v", err)
	}
	safeEvidence := &resourcev1.ManagedResourceEvidence{
		ObservedSpecHash: deleteLease.Spec.SpecHash, Deleted: true, StorageRetained: true, Namespace: "opsi-project-1-env-1",
		PVCName: "pvc", PVCUID: "pvc-uid", PVName: "pv", PVUID: "pv-uid", StorageClass: "local-path", ReclaimPolicy: "Delete",
		RequestedBytes: deleteLease.Spec.Storage.SizeBytes, ActualStorage: "1Gi", StorageHash: resourcev1.ManagedResourceStorageHash(deleteLease.Spec), ObservedAt: now,
	}
	if _, err := service.CompleteManaged(context.Background(), "project-1", postgres.ID, ManagedResult{Status: "deleted", LeaseToken: deleteLease.LeaseToken, Evidence: safeEvidence}); err != nil {
		t.Fatal(err)
	}
	retained, err := service.GetRetainedStorageByResource(context.Background(), "project-1", postgres.ID)
	if err != nil || retained.PVCUID != "pvc-uid" || retained.PVUID != "pv-uid" || retained.Lifecycle != resourcev1.RetainedStorageRetained || retained.RetainedBy != "user-1" {
		t.Fatalf("retained=%+v err=%v", retained, err)
	}
}

func TestPostgresBindingCredentialRoleIsolationAndRevocationLifecycle(t *testing.T) {
	service := testService()
	postgres := readyPostgresResource(t, &service)
	create := func(key, logical string) resourcev1.Binding {
		binding, _, err := service.CreateBinding(context.Background(), "project-1", key, resourcev1.CreateBindingRequest{
			EnvironmentID: "env-1", Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"},
			Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: postgres.ID}, Protocol: resourcev1.ProtocolPostgres, LogicalName: logical,
		})
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	first := create("postgres-binding-a", "DATABASE")
	replay, reused, err := service.CreateBinding(context.Background(), "project-1", "postgres-binding-a", resourcev1.CreateBindingRequest{
		EnvironmentID: "env-1", Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: "app-1"},
		Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: postgres.ID}, Protocol: resourcev1.ProtocolPostgres, LogicalName: "DATABASE",
	})
	if err != nil || !reused || replay.ID != first.ID || replay.CredentialID != first.CredentialID || replay.RoleName != first.RoleName {
		t.Fatalf("replay=%+v reused=%t err=%v", replay, reused, err)
	}
	firstCredential, err := service.Credentials.Get(context.Background(), first.CredentialID)
	if err != nil || firstCredential.ValidateBinding(first.ID, postgres.ID) != nil || firstCredential.CredentialID == postgres.Runtime.Spec.CredentialID {
		t.Fatalf("binding credential=%+v err=%v", firstCredential, err)
	}
	if environment, secrets, err := service.ApplicationRuntimeConfiguration(context.Background(), "project-1", "env-1", "app-1"); err != nil || len(environment) != 0 || len(secrets) != 0 {
		t.Fatalf("unready binding compiled environment=%+v secrets=%+v err=%v", environment, secrets, err)
	}
	completePostgresBindingLease(t, &service, postgres.ID, first.ID)
	first, _ = service.GetBinding(context.Background(), "project-1", first.ID)
	if first.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("ready binding=%+v", first)
	}
	environment, secrets, err := service.ApplicationRuntimeConfiguration(context.Background(), "project-1", "env-1", "app-1")
	if err != nil || len(environment) != 3 || len(secrets) != 3 || environment[0].Name != "DATABASE_HOST" || environment[1].Name != "DATABASE_NAME" || environment[2].Name != "DATABASE_PORT" || secrets[0].EnvName != "DATABASE_PASSWORD" || secrets[1].EnvName != "DATABASE_URL" || secrets[2].EnvName != "DATABASE_USER" {
		t.Fatalf("environment=%+v secrets=%+v err=%v", environment, secrets, err)
	}
	materials, err := service.ResolveSecretMaterials(context.Background(), "project-1", "", secrets)
	if err != nil || len(materials) != 1 || materials[0].SecretID != first.CredentialID || !strings.HasPrefix(materials[0].Values["DATABASE_URL"], "postgres://") || !strings.Contains(materials[0].Values["DATABASE_URL"], "/opsi?sslmode=disable") {
		t.Fatalf("materials=%+v err=%v", materials, err)
	}
	if _, err := service.ResolveSecretMaterials(context.Background(), "project-1", "", []deploymentv1.SecretReference{{EnvName: "DATABASE_PASSWORD", SecretID: postgres.Runtime.Spec.CredentialID}}); err == nil || !strings.Contains(err.Error(), resourcev1.FailureBindingSecretMaterialization) {
		t.Fatalf("PostgreSQL management credential was accepted as application material: %v", err)
	}
	second := create("postgres-binding-b", "ANALYTICS")
	secondCredential, _ := service.Credentials.Get(context.Background(), second.CredentialID)
	if second.ID == first.ID || second.CredentialID == first.CredentialID || second.RoleName == first.RoleName || secondCredential.Password == firstCredential.Password {
		t.Fatalf("bindings are not isolated: first=%+v second=%+v", first, second)
	}
	completePostgresBindingLease(t, &service, postgres.ID, first.ID, second.ID)
	if _, err := service.DeleteIntent(context.Background(), "project-1", postgres.ID, "user-1"); err == nil || !strings.Contains(err.Error(), resourcev1.FailureBindingActive) {
		t.Fatalf("active binding delete err=%v", err)
	}
	if _, err := service.DeleteBinding(context.Background(), "project-1", first.ID); err != nil {
		t.Fatal(err)
	}
	retryTarget, err := service.Get(context.Background(), "project-1", postgres.ID)
	if err != nil {
		t.Fatal(err)
	}
	retryTarget.Lifecycle = resourcev1.LifecycleReady
	if _, err := service.Store.Update(context.Background(), retryTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteBinding(context.Background(), "project-1", first.ID); err != nil {
		t.Fatal(err)
	}
	retryTarget, _ = service.Get(context.Background(), "project-1", postgres.ID)
	if retryTarget.Lifecycle != resourcev1.LifecyclePlanned {
		t.Fatalf("binding deletion retry did not re-plan target: %+v", retryTarget)
	}
	completePostgresBindingLease(t, &service, postgres.ID, first.ID, second.ID)
	if _, err := service.GetBinding(context.Background(), "project-1", first.ID); err != ErrNotFound {
		t.Fatalf("deleted binding remained: %v", err)
	}
	if _, err := service.Credentials.Get(context.Background(), first.CredentialID); err == nil {
		t.Fatal("deleted binding credential remained available")
	}
	remaining, err := service.GetBinding(context.Background(), "project-1", second.ID)
	remainingCredential, credentialErr := service.Credentials.Get(context.Background(), second.CredentialID)
	if err != nil || credentialErr != nil || remaining.Lifecycle != resourcev1.LifecycleReady || remainingCredential != secondCredential {
		t.Fatalf("remaining=%+v credential=%+v err=%v credentialErr=%v", remaining, remainingCredential, err, credentialErr)
	}
}

func readyPostgresResource(t *testing.T, service *Service) resourcev1.Resource {
	t.Helper()
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "postgres-binding-resource", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("f", 64), Assignments: []topologyv1.Assignment{{ServiceKey: created.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	ready, err := service.CompleteManaged(context.Background(), "project-1", created.ID, ManagedResult{Status: "ready", LeaseToken: lease.LeaseToken, Evidence: readyPostgresEvidence(lease.Spec)})
	if err != nil || ready.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	return ready
}

func completePostgresBindingLease(t *testing.T, service *Service, resourceID string, bindingIDs ...string) {
	t.Helper()
	lease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok || len(lease.Bindings) != len(bindingIDs) {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	results := make([]resourcev1.PostgresBindingResult, 0, len(lease.Bindings))
	for _, operation := range lease.Bindings {
		results = append(results, resourcev1.PostgresBindingResult{BindingID: operation.BindingID, Action: operation.Action, Status: "ready"})
	}
	if _, err := service.CompleteManaged(context.Background(), "project-1", resourceID, ManagedResult{Status: "ready", LeaseToken: lease.LeaseToken, Evidence: readyPostgresEvidence(lease.Spec), BindingResults: results}); err != nil {
		t.Fatal(err)
	}
}

func readyPostgresEvidence(spec resourcev1.ManagedResourceSpec) *resourcev1.ManagedResourceEvidence {
	return &resourcev1.ManagedResourceEvidence{ObservedSpecHash: spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, SecretReady: true, AuthReady: true, StorageReady: true, VolumeMounted: true, PVCName: "pvc", PVName: "pv", Image: spec.Image, ImageID: spec.Image, AvailableReplicas: 1, ObservedAt: time.Now().UTC()}
}

func TestCompilerFailsClosedForUnsupportedTypeAndMove(t *testing.T) {
	service := testService()
	rabbit, _, err := service.Create(context.Background(), "project-1", "user-1", "rabbit-runtime", managedRequest(resourcev1.TypeRabbitMQ))
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("b", 64), Assignments: []topologyv1.Assignment{{ServiceKey: rabbit.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err == nil || !strings.Contains(err.Error(), "MANAGED_RESOURCE_PROVISIONING_UNSUPPORTED") {
		t.Fatalf("unsupported err=%v", err)
	}
}

func TestRedisCompilerGeneratesStableCredentialAndSecretBinding(t *testing.T) {
	service := testService()
	request := managedRequest(resourcev1.TypeRedis)
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "redis-runtime", request)
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("e", 64), Assignments: []topologyv1.Assignment{{ServiceKey: created.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	planned, _ := service.Get(context.Background(), "project-1", created.ID)
	if planned.Runtime == nil || planned.Runtime.Spec.ResourceType != resourcev1.TypeRedis || planned.Runtime.Spec.Image != resourcev1.ValkeyImage || planned.Runtime.Spec.CredentialID == "" || planned.Runtime.Spec.Connection.URL != "" {
		t.Fatalf("planned=%+v", planned)
	}
	lease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok || lease.Credential == nil || lease.Credential.CredentialID != planned.Runtime.Spec.CredentialID {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	if refs := runtimeRefs(resourcev1.Resource{Kind: resourcev1.KindManagedService, Type: resourcev1.TypeRedis, Lifecycle: resourcev1.LifecycleReady, Runtime: &resourcev1.ManagedResourceRuntime{Spec: lease.Spec, Evidence: &resourcev1.ManagedResourceEvidence{ObservedSpecHash: lease.Spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, SecretReady: true, AuthReady: true, Image: lease.Spec.Image, ImageID: lease.Spec.Image, AvailableReplicas: 1}}}); len(refs) != 5 || refs[2].Sensitivity != resourcev1.ValueSecret || refs[2].SecretRef == nil {
		t.Fatalf("refs=%+v", refs)
	}
}

func TestManagedLeaseIsAtomicAndRecoversAfterExpiry(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	service := testService()
	service.Store = store
	service.Now = func() time.Time { return now }
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "nats-lease", managedRequest(resourcev1.TypeNATS))
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("c", 64), Assignments: []topologyv1.Assignment{{ServiceKey: created.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	claimed := make(chan ManagedLease, 2)
	for range 2 {
		go func() {
			defer wg.Done()
			lease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
			if err != nil {
				t.Errorf("lease: %v", err)
			} else if ok {
				claimed <- lease
			}
		}()
	}
	wg.Wait()
	close(claimed)
	if len(claimed) != 1 {
		t.Fatalf("claimed leases=%d", len(claimed))
	}
	first := <-claimed
	now = now.Add(managedLeaseTTL + time.Second)
	recovered, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok || recovered.LeaseToken == first.LeaseToken || recovered.Spec.SpecHash != first.Spec.SpecHash {
		t.Fatalf("recovered=%+v ok=%t err=%v", recovered, ok, err)
	}
}

func TestRuntimeImageMismatchIsDegraded(t *testing.T) {
	service := testService()
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "nats-mismatch", managedRequest(resourcev1.TypeNATS))
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("d", 64), Assignments: []topologyv1.Assignment{{ServiceKey: created.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}}}
	if err := service.ReconcileTopology(context.Background(), "project-1", plan, staticTarget{}); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseManaged(context.Background(), "project-1", "node-1")
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%t err=%v", lease, ok, err)
	}
	evidence := &resourcev1.ManagedResourceEvidence{ObservedSpecHash: lease.Spec.SpecHash, WorkloadReady: true, PodReady: true, ServiceReady: true, Image: "containerd://sha256:" + strings.Repeat("f", 64), AvailableReplicas: 1, ObservedAt: time.Now().UTC()}
	result, err := service.CompleteManaged(context.Background(), "project-1", created.ID, ManagedResult{Status: "failed", LeaseToken: lease.LeaseToken, Evidence: evidence, FailureCode: resourcev1.FailureRuntimeMismatch})
	if err != nil || result.Lifecycle != resourcev1.LifecycleDegraded || result.Runtime.FailureCode != resourcev1.FailureRuntimeMismatch {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
