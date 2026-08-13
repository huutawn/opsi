package svcatalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type managedRunner struct {
	objects map[string]map[string]any
	applies int
}

func (r *managedRunner) Run(_ context.Context, input []byte, _ string, args ...string) ([]byte, error) {
	if args[0] == "get" && args[1] == "pods" {
		for _, object := range r.objects {
			if object["kind"] != "Deployment" {
				continue
			}
			image := nested(object, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["image"]
			digest := strings.Split(image.(string), "@")[1]
			return json.Marshal(map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]any{"name": "nats", "ready": true, "imageID": "docker-pullable://nats@" + digest}}}}}})
		}
	}
	if args[0] == "get" {
		key := args[1] + "/" + args[2]
		object := r.objects[key]
		if object == nil {
			return nil, nil
		}
		return json.Marshal(object)
	}
	if args[0] == "create" || args[0] == "replace" {
		var object map[string]any
		_ = json.Unmarshal(input, &object)
		metadata := object["metadata"].(map[string]any)
		metadata["uid"], metadata["resourceVersion"], metadata["generation"] = "uid", "1", float64(1)
		if object["kind"] == "Namespace" {
			r.objects["namespace/"+metadata["name"].(string)] = object
			return nil, nil
		}
		if object["kind"] == "Deployment" {
			object["status"] = map[string]any{"observedGeneration": float64(1), "availableReplicas": float64(1)}
		} else {
			object["spec"].(map[string]any)["clusterIP"] = "10.43.0.10"
		}
		r.objects[strings.ToLower(object["kind"].(string))+"/"+metadata["name"].(string)] = object
		r.applies++
		return nil, nil
	}
	if args[0] == "delete" {
		delete(r.objects, args[1]+"/"+args[2])
		return nil, nil
	}
	return nil, nil
}

func TestManagedResourceReconcileIsIdempotentReadyAndOwnedDelete(t *testing.T) {
	spec := managedSpec(t)
	runner := &managedRunner{objects: map[string]map[string]any{}}
	reconciler := ManagedResourceReconciler{Runner: runner}
	result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease", Spec: spec})
	if result.Status != "ready" || result.Evidence == nil || result.Evidence.Image != spec.Image || len(runner.objects) != 3 {
		t.Fatalf("result=%+v objects=%v", result, runner.objects)
	}
	result = reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-2", Spec: spec})
	if result.Status != "ready" || len(runner.objects) != 3 || runner.applies != 4 {
		t.Fatalf("replay=%+v objects=%d applies=%d", result, len(runner.objects), runner.applies)
	}
	spec.CPUMillicores = 250
	spec.SpecHash, _ = spec.Hash()
	result = reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-3", Spec: spec})
	if result.Status != "ready" || runner.applies != 6 {
		t.Fatalf("update=%+v applies=%d", result, runner.applies)
	}
	result = reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "lease-4", Spec: spec})
	if result.Status != "deleted" || len(runner.objects) != 1 {
		t.Fatalf("delete=%+v objects=%v", result, runner.objects)
	}
}

func TestManagedResourceDeleteRejectsForeignOwnership(t *testing.T) {
	spec := managedSpec(t)
	objects := managedResourceObjects(spec, nil)
	objects[0]["metadata"].(map[string]any)["labels"].(map[string]string)["opsi.dev/managed-resource-id"] = "other"
	runner := &managedRunner{objects: map[string]map[string]any{"deployment/" + spec.Connection.ServiceName: objects[0]}}
	result := (ManagedResourceReconciler{Runner: runner}).Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "lease", Spec: spec})
	if result.Status != "failed" || result.FailureCode != "MANAGED_RESOURCE_DELETE_FAILED" || len(runner.objects) != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestValkeyManifestUsesSecretFilesAndNoPasswordArgs(t *testing.T) {
	spec := resourcev1.ManagedResourceSpec{SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-redis", ProjectID: "project-1", EnvironmentID: "env-1", ResourceType: resourcev1.TypeRedis, Profile: "single-node-experimental", Version: resourcev1.ValkeyVersion, Image: resourcev1.ValkeyImage, CredentialID: "mrcred-res-redis", Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"}, Replicas: 1, CPUMillicores: 100, MemoryBytes: 64 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "redis", Port: 6379, Protocol: resourcev1.ProtocolRedis}}, Connection: resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-res-redis-runtime-1", Host: "redis.default", Port: 6379, Protocol: resourcev1.ProtocolRedis}, ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64)}
	spec.SpecHash, _ = spec.Hash()
	objects := managedResourceObjects(spec, &resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Username: "opsi", Password: "secret"})
	if len(objects) != 3 {
		t.Fatalf("objects=%d", len(objects))
	}
	deployment := objects[1]
	container := nested(deployment, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	if _, ok := container["args"]; ok || container["command"].([]any)[1] != "/run/opsi-valkey/valkey.conf" {
		t.Fatalf("container=%v", container)
	}
	if _, ok := nested(deployment, "metadata", "annotations").(map[string]string)["opsi.dev/managed-resource-id"]; !ok {
		t.Fatal("deployment ownership annotation missing")
	}
}

func TestManagedResourceRuntimeMismatchReturnsFactualEvidence(t *testing.T) {
	spec := managedSpec(t)
	runner := &managedRunner{objects: map[string]map[string]any{}}
	reconciler := ManagedResourceReconciler{Runner: runner, PollInterval: time.Millisecond}
	if result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease", Spec: spec}); result.Status != "ready" {
		t.Fatalf("initial=%+v", result)
	}
	deployment := runner.objects["deployment/"+spec.Connection.ServiceName]
	deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "docker.io/library/nats@sha256:" + strings.Repeat("f", 64)
	result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-2", Spec: spec})
	if result.Status != "ready" {
		t.Fatalf("controlled reconcile restored image: %+v", result)
	}
	// Observe mismatch independently of apply replacing it first.
	runner.objects["deployment/"+spec.Connection.ServiceName]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "docker.io/library/nats@sha256:" + strings.Repeat("f", 64)
	evidence, err := reconciler.waitReady(context.Background(), spec)
	if err == nil || failureCode(err) != resourcev1.FailureRuntimeMismatch || evidence == nil || evidence.Image == "" || evidence.Image == spec.Image {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func managedSpec(t *testing.T) resourcev1.ManagedResourceSpec {
	t.Helper()
	serviceName := "opsi-mr-res-1"
	host := serviceName + ".opsi-project-1-env-1-411623da25.svc.cluster.local"
	spec := resourcev1.ManagedResourceSpec{SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-1", ProjectID: "project-1", EnvironmentID: "env-1", ResourceType: resourcev1.TypeNATS, Profile: "single-node-experimental", Version: resourcev1.NATSVersion, Image: resourcev1.NATSImage, Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"}, Replicas: 1, CPUMillicores: 100, MemoryBytes: 64 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "nats", Port: 4222, Protocol: resourcev1.ProtocolNATS}}, Connection: resourcev1.ManagedResourceConnection{ServiceName: serviceName, Host: host, Port: 4222, Protocol: resourcev1.ProtocolNATS, URL: "nats://" + host + ":4222"}, ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64)}
	hash, _ := spec.Hash()
	spec.SpecHash = hash
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	return spec
}
