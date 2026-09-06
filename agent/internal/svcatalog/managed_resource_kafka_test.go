package svcatalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type kafkaRunner struct {
	objects    map[string]map[string]any
	commands   [][]string
	inputs     [][]byte
	pvcApplies int
	execError  error
	nextUID    int
}

func (r *kafkaRunner) Run(_ context.Context, input []byte, _ string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	r.inputs = append(r.inputs, append([]byte(nil), input...))
	if args[0] == "get" && args[1] == "pods" {
		var statefulSet map[string]any
		for k, obj := range r.objects {
			if strings.HasPrefix(k, "statefulset/") {
				statefulSet = obj
				break
			}
		}
		if statefulSet == nil {
			return json.Marshal(map[string]any{"items": []any{}})
		}
		template := nested(statefulSet, "spec", "template").(map[string]any)
		podSpec := nested(template, "spec").(map[string]any)
		container := podSpec["containers"].([]any)[0].(map[string]any)
		digest := strings.Split(container["image"].(string), "@")[1]
		pod := map[string]any{
			"spec": podSpec,
			"status": map[string]any{
				"containerStatuses": []any{
					map[string]any{"name": "kafka", "ready": true, "imageID": "docker-pullable://kafka@" + digest},
				},
			},
		}
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
			pvName := "pv-kafka"
			object["spec"].(map[string]any)["volumeName"] = pvName
			object["spec"].(map[string]any)["storageClassName"] = "local-path"
			object["status"] = map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": "10Gi"}}
			r.objects["persistentvolume/"+pvName] = map[string]any{
				"metadata": map[string]any{"name": pvName, "uid": "pv-uid-kafka"},
				"spec": map[string]any{
					"storageClassName": "local-path", "persistentVolumeReclaimPolicy": "Delete",
					"claimRef": map[string]any{"name": metadata["name"], "namespace": metadata["namespace"], "uid": metadata["uid"]},
				},
			}
		case "StatefulSet":
			object["status"] = map[string]any{"observedGeneration": float64(1), "readyReplicas": float64(1), "currentRevision": "revision-1", "updateRevision": "revision-1"}
		case "Service":
			object["spec"].(map[string]any)["clusterIP"] = "10.43.0.12"
		}
		r.objects[strings.ToLower(kind)+"/"+metadata["name"].(string)] = object
		return nil, nil
	}
	if args[0] == "delete" {
		delete(r.objects, args[1]+"/"+args[2])
		if args[1] == "persistentvolumeclaim" {
			delete(r.objects, "persistentvolume/pv-kafka")
		}
		return nil, nil
	}
	if args[0] == "exec" {
		return []byte("success\n"), r.execError
	}
	return nil, nil
}

func kafkaSpec(t *testing.T) (resourcev1.ManagedResourceSpec, *resourcev1.ManagedResourceCredential) {
	t.Helper()
	serviceName := "opsi-mr-res-kafka"
	host := serviceName + ".opsi-project-1-env-1.svc.cluster.local"
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:    "res-kafka",
		ProjectID:     "project-1",
		EnvironmentID: "env-1",
		ResourceType:  resourcev1.TypeKafka,
		Profile:       "single-node-experimental",
		Version:       resourcev1.KafkaVersion,
		Image:         resourcev1.KafkaImage,
		CredentialID:  "mrcred-res-kafka",
		Assignment: resourcev1.ManagedResourceAssignment{
			RuntimeID: "runtime-1",
			NodeID:    "node-1",
			AgentID:   "agent-1",
		},
		Replicas:      1,
		CPUMillicores: 500,
		MemoryBytes:   1 << 30,
		Ports:         []resourcev1.ManagedResourcePort{{Name: "kafka", Port: 9092, Protocol: resourcev1.ProtocolKafka}},
		Storage: resourcev1.StorageRequest{
			Persistent: true,
			SizeBytes:  resourcev1.DefaultKafkaStorageBytes,
			PolicyRef:  resourcev1.StoragePolicyDefault,
		},
		Connection: resourcev1.ManagedResourceConnection{
			ServiceName: serviceName,
			Host:        host,
			Port:        9092,
			Protocol:    resourcev1.ProtocolKafka,
		},
		ServiceConfig: map[string]string{
			"num_partitions":  "3",
			"retention_hours": "168",
		},
		ConfigurationHash: strings.Repeat("k", 64),
		TopologyRevision:  1,
		TopologyHash:      strings.Repeat("t", 64),
	}
	hash, _ := spec.Hash()
	spec.SpecHash = hash
	credential := &resourcev1.ManagedResourceCredential{
		CredentialID: spec.CredentialID,
		Username:     "opsi-kafka-user",
		Password:     "secret-kafka-pass-123",
	}
	return spec, credential
}

func TestKafkaRendererUsesStatefulSetPVCAndSecretFiles(t *testing.T) {
	spec, credential := kafkaSpec(t)
	objects := kafkaManagedResourceObjects(spec, credential)
	if len(objects) != 4 {
		t.Fatalf("expected 4 objects (secret, pvc, statefulset, service), got %d", len(objects))
	}

	secret, pvc, statefulSet, service := objects[0], objects[1], objects[2], objects[3]
	if secret["kind"] != "Secret" || pvc["kind"] != "PersistentVolumeClaim" || statefulSet["kind"] != "StatefulSet" || service["kind"] != "Service" {
		t.Fatalf("unexpected object kinds: secret=%v pvc=%v ss=%v svc=%v", secret["kind"], pvc["kind"], statefulSet["kind"], service["kind"])
	}

	// Secret checks
	secData := secret["data"].(map[string]any)
	for _, key := range []string{"username", "password", "kafka_server_jaas.conf", "client.properties"} {
		if secData[key] == nil {
			t.Fatalf("secret missing key %s", key)
		}
	}
	jaasBytes, _ := base64.StdEncoding.DecodeString(secData["kafka_server_jaas.conf"].(string))
	if !strings.Contains(string(jaasBytes), credential.Username) || !strings.Contains(string(jaasBytes), credential.Password) {
		t.Fatal("jaas config missing credentials")
	}
	propsBytes, _ := base64.StdEncoding.DecodeString(secData["client.properties"].(string))
	if !strings.Contains(string(propsBytes), "SASL_PLAINTEXT") || !strings.Contains(string(propsBytes), "PLAIN") {
		t.Fatal("client properties missing SASL config")
	}

	// StatefulSet checks
	ssSpec := statefulSet["spec"].(map[string]any)
	if ssSpec["replicas"].(int32) != 1 {
		t.Fatalf("replicas=%v, want 1", ssSpec["replicas"])
	}
	template := ssSpec["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	containers := podSpec["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("containers count=%d", len(containers))
	}
	container := containers[0].(map[string]any)
	if container["image"] != spec.Image {
		t.Fatalf("image=%v want %v", container["image"], spec.Image)
	}

	// Verify env vars contain KRaft single-node configs and stable cluster ID
	envList := container["env"].([]any)
	envMap := map[string]string{}
	for _, rawEnv := range envList {
		entry := rawEnv.(map[string]any)
		envMap[entry["name"].(string)] = entry["value"].(string)
	}
	expectedClusterID := kafkaClusterID(spec.ResourceID)
	if envMap["CLUSTER_ID"] != expectedClusterID || len(expectedClusterID) != 22 {
		t.Fatalf("CLUSTER_ID=%v want %v", envMap["CLUSTER_ID"], expectedClusterID)
	}
	if envMap["KAFKA_NODE_ID"] != "1" || envMap["KAFKA_PROCESS_ROLES"] != "broker,controller" || envMap["KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR"] != "1" {
		t.Fatalf("KRaft env misconfigured: %+v", envMap)
	}
	if envMap["KAFKA_NUM_PARTITIONS"] != "3" || envMap["KAFKA_LOG_RETENTION_HOURS"] != "168" {
		t.Fatalf("Kafka service config env misconfigured: %+v", envMap)
	}
	if !strings.Contains(envMap["KAFKA_OPTS"], "kafka_server_jaas.conf") {
		t.Fatalf("KAFKA_OPTS missing jaas config: %s", envMap["KAFKA_OPTS"])
	}

	// Verify resources
	res := container["resources"].(map[string]any)
	requests := res["requests"].(map[string]any)
	limits := res["limits"].(map[string]any)
	if requests["cpu"] != "500m" || requests["memory"] != fmt.Sprint(1<<30) || limits["cpu"] != "500m" || limits["memory"] != fmt.Sprint(1<<30) {
		t.Fatalf("resources mismatch: req=%v lim=%v", requests, limits)
	}

	// Service checks
	svcSpec := service["spec"].(map[string]any)
	if svcSpec["type"] != "ClusterIP" {
		t.Fatalf("service type=%v, want ClusterIP", svcSpec["type"])
	}
	ports := svcSpec["ports"].([]any)
	if len(ports) != 1 || ports[0].(map[string]any)["port"].(int32) != 9092 {
		t.Fatalf("service ports=%+v", ports)
	}
}

func TestKafkaReconcileIsIdempotentUpdatesComputeAndRetainsPVC(t *testing.T) {
	spec, credential := kafkaSpec(t)
	runner := &kafkaRunner{objects: map[string]map[string]any{}}
	reconciler := ManagedResourceReconciler{Runner: runner, Timeout: time.Second, PollInterval: time.Millisecond}

	// Initial Apply
	ready := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "lease-apply-1",
		Spec:       spec,
		Credential: credential,
	})
	if ready.Status != "ready" || ready.Evidence == nil || !ready.Evidence.WorkloadReady || !ready.Evidence.AuthReady || !ready.Evidence.StorageReady || !ready.Evidence.VolumeMounted {
		t.Fatalf("initial ready=%+v", ready)
	}

	// Idempotent re-apply
	ready2 := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "lease-apply-2",
		Spec:       spec,
		Credential: credential,
	})
	if ready2.Status != "ready" {
		t.Fatalf("idempotent ready=%+v", ready2)
	}

	// Update CPU, RAM, and ServiceConfig
	updatedSpec := spec
	updatedSpec.CPUMillicores = 1000
	updatedSpec.MemoryBytes = 2 << 30
	updatedSpec.ServiceConfig = map[string]string{
		"num_partitions":  "6",
		"retention_hours": "336",
	}
	updatedSpec.SpecHash, _ = updatedSpec.Hash()
	updated := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "lease-update",
		Spec:       updatedSpec,
		Credential: credential,
	})
	if updated.Status != "ready" || updated.Evidence == nil || updated.Evidence.ObservedSpecHash != updatedSpec.SpecHash {
		t.Fatalf("updated ready=%+v", updated)
	}

	// Delete retains PVC
	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "delete",
		LeaseToken: "lease-delete",
		Spec:       updatedSpec,
	})
	if deleted.Status != "deleted" || deleted.Evidence == nil || !deleted.Evidence.StorageRetained || deleted.Evidence.PVCName == "" || deleted.Evidence.PVName == "" || deleted.Evidence.PVCUID == "" {
		t.Fatalf("deleted evidence=%+v", deleted)
	}
	if runner.objects["statefulset/"+spec.Connection.ServiceName] != nil || runner.objects["service/"+spec.Connection.ServiceName] != nil || runner.objects["secret/"+managedResourceSecretName(spec)] != nil {
		t.Fatalf("workload/service/secret were not deleted: %+v", runner.objects)
	}
	if runner.objects["persistentvolumeclaim/"+managedResourcePVCName(spec)] == nil {
		t.Fatal("PVC was destroyed during delete")
	}
}

func TestKafkaReadinessAuthFailureReturnsTypedErrorWithoutLeak(t *testing.T) {
	spec, credential := kafkaSpec(t)
	runner := &kafkaRunner{
		objects:   map[string]map[string]any{},
		execError: errors.New("Authentication failed: Invalid username or password"),
	}
	reconciler := ManagedResourceReconciler{Runner: runner, Timeout: 100 * time.Millisecond, PollInterval: time.Millisecond}
	result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "lease-auth-fail",
		Spec:       spec,
		Credential: credential,
	})
	if result.Status != "failed" || result.FailureCode != resourcev1.FailureAuthFailed {
		t.Fatalf("result=%+v", result)
	}
	if strings.Contains(result.FailureMessageRedacted, credential.Password) {
		t.Fatalf("credential leaked in failure message: %s", result.FailureMessageRedacted)
	}
}

func TestKafkaInvalidSpecReturnsTypedStorageAndVersionFailures(t *testing.T) {
	spec, _ := kafkaSpec(t)
	for name, tc := range map[string]struct {
		mutate func(*resourcev1.ManagedResourceSpec)
		want   string
	}{
		"storage_required": {
			mutate: func(s *resourcev1.ManagedResourceSpec) { s.Storage.Persistent = false },
			want:   resourcev1.FailureStorageRequired,
		},
		"storage_invalid": {
			mutate: func(s *resourcev1.ManagedResourceSpec) { s.Storage.PolicyRef = "unsupported" },
			want:   resourcev1.FailureStorageInvalid,
		},
		"version": {
			mutate: func(s *resourcev1.ManagedResourceSpec) { s.Version = "5.0.0" },
			want:   resourcev1.FailureVersionUpgradeUnsupported,
		},
	} {
		t.Run(name, func(t *testing.T) {
			copySpec := spec
			tc.mutate(&copySpec)
			if got := invalidSpecFailureCode(copySpec); got != tc.want {
				t.Fatalf("got=%s want=%s", got, tc.want)
			}
		})
	}
}
