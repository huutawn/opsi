package resource

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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
	deleting, err := service.DeleteIntent(context.Background(), "project-1", created.ID)
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

func TestCompilerFailsClosedForUnsupportedTypeAndMove(t *testing.T) {
	service := testService()
	postgres, _, err := service.Create(context.Background(), "project-1", "user-1", "postgres-runtime", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	plan := topologyv1.Plan{ProjectID: "project-1", Revision: 1, PlanHash: strings.Repeat("b", 64), Assignments: []topologyv1.Assignment{{ServiceKey: postgres.ID, EnvironmentID: "env-1", RuntimeID: "runtime-1", Replicas: 1, CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}}}
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
