package svcatalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type postgresRunner struct {
	objects    map[string]map[string]any
	commands   [][]string
	inputs     [][]byte
	pvcApplies int
	execError  error
	nextUID    int
}

func (r *postgresRunner) Run(_ context.Context, input []byte, _ string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	r.inputs = append(r.inputs, append([]byte(nil), input...))
	if args[0] == "get" && args[1] == "pods" {
		statefulSet := r.objects["statefulset/opsi-mr-res-postgres-runtime-1"]
		if statefulSet == nil {
			return json.Marshal(map[string]any{"items": []any{}})
		}
		template := nested(statefulSet, "spec", "template").(map[string]any)
		podSpec := nested(template, "spec").(map[string]any)
		container := podSpec["containers"].([]any)[0].(map[string]any)
		digest := strings.Split(container["image"].(string), "@")[1]
		pod := map[string]any{"spec": podSpec, "status": map[string]any{"containerStatuses": []any{map[string]any{"name": "postgres", "ready": true, "imageID": "docker-pullable://postgres@" + digest}}}}
		return json.Marshal(map[string]any{"items": []any{pod}})
	}
	if args[0] == "get" {
		object := r.objects[args[1]+"/"+args[2]]
		if object == nil {
			return nil, nil
		}
		return json.Marshal(object)
	}
	if args[0] == "create" || args[0] == "replace" {
		var object map[string]any
		_ = json.Unmarshal(input, &object)
		metadata := object["metadata"].(map[string]any)
		r.nextUID++
		metadata["uid"], metadata["resourceVersion"], metadata["generation"] = "uid-"+strconv.Itoa(r.nextUID), "1", float64(1)
		kind := manifestKind(object)
		switch kind {
		case "Namespace":
			r.objects["namespace/"+metadata["name"].(string)] = object
			return nil, nil
		case "PersistentVolumeClaim":
			r.pvcApplies++
			object["spec"].(map[string]any)["volumeName"] = "pv-postgres"
			object["spec"].(map[string]any)["storageClassName"] = "local-path"
			object["status"] = map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": "1Gi"}}
			r.objects["persistentvolume/pv-postgres"] = map[string]any{
				"metadata": map[string]any{"name": "pv-postgres", "uid": "pv-uid-postgres"},
				"spec": map[string]any{
					"storageClassName": "local-path", "persistentVolumeReclaimPolicy": "Delete",
					"claimRef": map[string]any{"name": metadata["name"], "namespace": metadata["namespace"], "uid": metadata["uid"]},
				},
			}
		case "StatefulSet":
			object["status"] = map[string]any{"observedGeneration": float64(1), "readyReplicas": float64(1), "currentRevision": "revision-1", "updateRevision": "revision-1"}
		case "Service":
			object["spec"].(map[string]any)["clusterIP"] = "10.43.0.10"
		}
		r.objects[strings.ToLower(kind)+"/"+metadata["name"].(string)] = object
		return nil, nil
	}
	if args[0] == "delete" {
		delete(r.objects, args[1]+"/"+args[2])
		if args[1] == "persistentvolumeclaim" {
			delete(r.objects, "persistentvolume/pv-postgres")
		}
		return nil, nil
	}
	if args[0] == "exec" {
		return []byte("1\n"), r.execError
	}
	return nil, nil
}

func TestPostgresRendererUsesStatefulSetPVCAndSecretFiles(t *testing.T) {
	spec, credential := postgresSpec(t)
	objects := postgresManagedResourceObjects(spec, credential)
	if len(objects) != 4 || manifestKind(objects[0]) != "Secret" || manifestKind(objects[1]) != "PersistentVolumeClaim" || manifestKind(objects[2]) != "StatefulSet" || manifestKind(objects[3]) != "Service" {
		t.Fatalf("objects=%v", objects)
	}
	statefulSet := objects[2]
	container := nested(statefulSet, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	serialized, _ := json.Marshal(map[string]any{"statefulset": statefulSet, "pvc": objects[1]})
	if strings.Contains(string(serialized), credential.Password) || strings.Contains(string(serialized), "hostPath") || strings.Contains(string(serialized), "emptyDir") || nested(container, "env").([]any)[1].(map[string]any)["name"] != "POSTGRES_PASSWORD_FILE" || nested(container, "volumeMounts").([]any)[0].(map[string]any)["mountPath"] != postgresDataMount {
		t.Fatalf("unsafe PostgreSQL manifest: %s", serialized)
	}
	readinessProbe := nested(container, "readinessProbe").(map[string]any)
	if _, exists := readinessProbe["exec"]; exists {
		t.Fatalf("PostgreSQL readiness must not execute child processes: %v", readinessProbe)
	}
	tcpSocket := nested(readinessProbe, "tcpSocket").(map[string]any)
	if tcpSocket["port"] != "postgres" {
		t.Fatalf("readiness tcp socket=%v", tcpSocket)
	}
}

func TestPostgresReconcileIsIdempotentUpdatesComputeAndRetainsPVC(t *testing.T) {
	spec, credential := postgresSpec(t)
	runner := &postgresRunner{objects: map[string]map[string]any{}}
	reconciler := ManagedResourceReconciler{Runner: runner, Timeout: time.Second, PollInterval: time.Millisecond}
	for index := range 3 {
		result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-" + strconv.Itoa(index), Spec: spec, Credential: credential})
		if result.Status != "ready" || result.Evidence == nil || !result.Evidence.StorageReady || !result.Evidence.VolumeMounted || !result.Evidence.AuthReady || result.Evidence.PVCName != managedResourcePVCName(spec) || result.Evidence.PVName != "pv-postgres" {
			t.Fatalf("result=%+v", result)
		}
	}
	if runner.pvcApplies != 1 {
		t.Fatalf("PVC applies=%d", runner.pvcApplies)
	}
	pvc := runner.objects["persistentvolumeclaim/"+managedResourcePVCName(spec)]
	pvcUID := metadataString(pvc, "uid")
	spec.CPUMillicores = 500
	spec.SpecHash, _ = spec.Hash()
	if result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-update", Spec: spec, Credential: credential}); result.Status != "ready" || runner.pvcApplies != 1 || metadataString(runner.objects["persistentvolumeclaim/"+managedResourcePVCName(spec)], "uid") != pvcUID {
		t.Fatalf("update=%+v pvcApplies=%d", result, runner.pvcApplies)
	}
	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "lease-delete", Spec: spec})
	if deleted.Status != "deleted" || deleted.Evidence == nil || !deleted.Evidence.StorageRetained || runner.objects["persistentvolumeclaim/"+managedResourcePVCName(spec)] == nil || runner.objects["statefulset/"+spec.Connection.ServiceName] != nil || runner.objects["secret/"+managedResourceSecretName(spec)] != nil {
		t.Fatalf("delete=%+v objects=%v", deleted, runner.objects)
	}
}

func TestPostgresDeleteAndAuthFailClosedWithoutCredentialLeak(t *testing.T) {
	spec, credential := postgresSpec(t)
	runner := &postgresRunner{objects: map[string]map[string]any{}, execError: errors.New("password authentication failed")}
	reconciler := ManagedResourceReconciler{Runner: runner, Timeout: time.Second, PollInterval: time.Millisecond}
	result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease", Spec: spec, Credential: credential})
	if result.Status != "failed" || result.FailureCode != resourcev1.FailureAuthFailed || strings.Contains(result.FailureMessageRedacted, credential.Password) {
		t.Fatalf("result=%+v", result)
	}
	runner.execError = nil
	if ready := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "ready", Spec: spec, Credential: credential}); ready.Status != "ready" {
		t.Fatalf("ready=%+v", ready)
	}
	pvc := runner.objects["persistentvolumeclaim/"+managedResourcePVCName(spec)]
	pvc["metadata"].(map[string]any)["labels"].(map[string]any)["opsi.dev/managed-resource-id"] = "foreign"
	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "delete", Spec: spec})
	if deleted.Status != "failed" || deleted.FailureCode != resourcev1.FailureDeleteFailed {
		t.Fatalf("delete=%+v", deleted)
	}
}

func TestPostgresInvalidSpecReturnsTypedStorageAndVersionFailures(t *testing.T) {
	spec, credential := postgresSpec(t)
	for _, tc := range []struct {
		name string
		edit func(*resourcev1.ManagedResourceSpec)
		code string
	}{
		{"required", func(value *resourcev1.ManagedResourceSpec) { value.Storage.Persistent = false }, resourcev1.FailureStorageRequired},
		{"invalid", func(value *resourcev1.ManagedResourceSpec) { value.Storage.SizeBytes = 0 }, resourcev1.FailureStorageInvalid},
		{"version", func(value *resourcev1.ManagedResourceSpec) { value.Version = "19" }, resourcev1.FailureVersionUpgradeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := spec
			tc.edit(&invalid)
			invalid.SpecHash, _ = invalid.Hash()
			result := (ManagedResourceReconciler{}).Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease", Spec: invalid, Credential: credential})
			if result.Status != "failed" || result.FailureCode != tc.code {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestPostgresDeleteRequiresFactualRetainedPVC(t *testing.T) {
	spec, _ := postgresSpec(t)
	result := (ManagedResourceReconciler{Runner: &postgresRunner{objects: map[string]map[string]any{}}}).Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "delete", Spec: spec})
	if result.Status != "failed" || result.FailureCode != resourcev1.FailurePersistentDeleteUnsupported {
		t.Fatalf("result=%+v", result)
	}
}

func TestPostgresBindingRoleReconcileAndRevokeUseScopedCredential(t *testing.T) {
	spec, management := postgresSpec(t)
	bindingPassword := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	binding := resourcev1.PostgresBindingOperation{
		BindingID: "rbind-test", CredentialID: "rbcred-rbind-test", RoleName: "opsi_b_0123456789abcdef0123456789abcdef", Database: "opsi", Action: resourcev1.PostgresBindingEnsure, Create: true,
		Credential: &resourcev1.ManagedResourceCredential{CredentialID: "rbcred-rbind-test", Purpose: resourcev1.CredentialPurposeResourceBinding, OwnerID: "rbind-test", ResourceID: spec.ResourceID, Username: "opsi_b_0123456789abcdef0123456789abcdef", Password: bindingPassword, Database: "opsi"},
	}
	runner := &postgresRunner{objects: map[string]map[string]any{}}
	result := (ManagedResourceReconciler{Runner: runner}).reconcilePostgresBindings(context.Background(), spec, []resourcev1.PostgresBindingOperation{binding})
	if len(result) != 1 || result[0].Status != "ready" {
		t.Fatalf("result=%+v", result)
	}
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command, " "), binding.Credential.Password) || strings.Contains(strings.Join(command, " "), management.Password) {
			t.Fatalf("credential leaked into command arguments: %q", command)
		}
	}
	binding.Action, binding.Create, binding.Credential = resourcev1.PostgresBindingRevoke, false, nil
	result = (ManagedResourceReconciler{Runner: runner}).reconcilePostgresBindings(context.Background(), spec, []resourcev1.PostgresBindingOperation{binding})
	if len(result) != 1 || result[0].Status != "ready" {
		t.Fatalf("revoke=%+v", result)
	}
	last := runner.commands[len(runner.commands)-1]
	if len(last) != 7 || last[0] != "delete" || last[1] != "secret" || last[2] != "-n" || last[3] != managedResourceNamespace(spec) || last[4] != "-l" || last[5] != "opsi.dev/workload-secret=rbcred-rbind-test" || last[6] != "--ignore-not-found" {
		t.Fatalf("secret delete command=%q", last)
	}
	failing := &postgresRunner{objects: map[string]map[string]any{}, execError: errors.New("role mutation failed")}
	binding.Action, binding.Create, binding.Credential = resourcev1.PostgresBindingEnsure, true, &resourcev1.ManagedResourceCredential{CredentialID: "rbcred-rbind-test", Purpose: resourcev1.CredentialPurposeResourceBinding, OwnerID: "rbind-test", ResourceID: spec.ResourceID, Username: binding.RoleName, Password: bindingPassword, Database: "opsi"}
	failed := (ManagedResourceReconciler{Runner: failing}).reconcilePostgresBindings(context.Background(), spec, []resourcev1.PostgresBindingOperation{binding})
	if len(failed) != 1 || failed[0].FailureCode != resourcev1.FailureBindingRoleCreate {
		t.Fatalf("failed=%+v", failed)
	}
	for _, role := range []string{"0123456789abcdef0123456789abcdef", "opsi_b_0123456789abcdef0123456789abcde'"} {
		invalidBinding := binding
		invalidCredential := *binding.Credential
		invalidBinding.RoleName, invalidCredential.Username, invalidBinding.Credential = role, role, &invalidCredential
		invalidRunner := &postgresRunner{objects: map[string]map[string]any{}}
		invalid := (ManagedResourceReconciler{Runner: invalidRunner}).reconcilePostgresBindings(context.Background(), spec, []resourcev1.PostgresBindingOperation{invalidBinding})
		if len(invalid) != 1 || invalid[0].FailureCode != resourcev1.FailureBindingRoleReconcile || len(invalidRunner.commands) != 0 {
			t.Fatalf("unsafe role %q accepted result=%+v commands=%q", role, invalid, invalidRunner.commands)
		}
	}
}

func postgresSpec(t *testing.T) (resourcev1.ManagedResourceSpec, *resourcev1.ManagedResourceCredential) {
	t.Helper()
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-postgres", ProjectID: "project-1", EnvironmentID: "env-1",
		ResourceType: resourcev1.TypePostgres, Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage,
		CredentialID: "mrcred-res-postgres", Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}},
		Storage:           resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		Connection:        resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-res-postgres-runtime-1", Host: "opsi-mr-res-postgres-runtime-1.opsi-project-1-env-1.svc.cluster.local", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"},
		ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	return spec, &resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Username: "opsi", Password: "postgres-password-must-not-leak", Database: "opsi"}
}

func anyStrings(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.(string))
	}
	return result
}
