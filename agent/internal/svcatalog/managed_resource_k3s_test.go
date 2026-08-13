package svcatalog

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestManagedResourceRealK3sNATS(t *testing.T) {
	if os.Getenv("OPSI_E2E_K3S_NATS") != "1" {
		t.Skip("set OPSI_E2E_K3S_NATS=1 with KUBECONFIG pointing to a disposable K3s cluster")
	}
	spec := managedSpec(t)
	reconciler := ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}
	apply := func(token string, spec resourcev1.ManagedResourceSpec) cloudrelay.ManagedResourceResult {
		return reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: token, Spec: spec})
	}
	result := apply("lease-1", spec)
	if result.Status != "ready" || result.Evidence == nil || result.Evidence.Image != resourcev1.NATSImage {
		t.Fatalf("first reconcile=%+v", result)
	}
	assertManagedK3sObjects(t, spec, 1)
	assertNATSConnection(t, spec)
	if replay := apply("lease-2", spec); replay.Status != "ready" {
		t.Fatalf("replay=%+v", replay)
	}
	assertManagedK3sObjects(t, spec, 1)
	if restarted := (ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}).Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-3", Spec: spec}); restarted.Status != "ready" {
		t.Fatalf("restart reconcile=%+v", restarted)
	}
	assertManagedK3sObjects(t, spec, 1)
	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "lease-4", Spec: spec})
	if deleted.Status != "deleted" {
		t.Fatalf("delete=%+v", deleted)
	}
	assertManagedK3sObjects(t, spec, 0)
}

func TestManagedResourceRealK3sValkey(t *testing.T) {
	if os.Getenv("OPSI_E2E_K3S_VALKEY") != "1" {
		t.Skip("set OPSI_E2E_K3S_VALKEY=1 with KUBECONFIG pointing to a disposable K3s cluster")
	}
	spec := resourcev1.ManagedResourceSpec{SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-valkey-e2e", ProjectID: "project-e2e", EnvironmentID: "env-e2e", ResourceType: resourcev1.TypeRedis, Profile: "single-node-experimental", Version: resourcev1.ValkeyVersion, Image: resourcev1.ValkeyImage, CredentialID: "mrcred-res-valkey-e2e", Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-e2e", NodeID: "node-e2e", AgentID: "agent-e2e"}, Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "redis", Port: 6379, Protocol: resourcev1.ProtocolRedis}}, Connection: resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-res-valkey-e2e-runtime-e2e", Host: "opsi-mr-res-valkey-e2e-runtime-e2e.opsi-project-e2e-env-e2e.svc.cluster.local", Port: 6379, Protocol: resourcev1.ProtocolRedis}, ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64)}
	spec.SpecHash, _ = spec.Hash()
	credential := &resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Username: "opsi", Password: "e2e-secret-password"}
	reconciler := ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}
	apply := func(token string) cloudrelay.ManagedResourceResult {
		return reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: token, Spec: spec, Credential: credential})
	}
	result := apply("lease-1")
	if result.Status != "ready" || result.Evidence == nil || result.Evidence.Image != spec.Image || !result.Evidence.AuthReady {
		t.Fatalf("first reconcile=%+v", result)
	}
	assertManagedK3sObjects(t, spec, 1)
	assertValkeyAuth(t, spec, credential)
	if replay := apply("lease-2"); replay.Status != "ready" {
		t.Fatalf("replay=%+v", replay)
	}
	if restarted := (ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}).Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-3", Spec: spec, Credential: credential}); restarted.Status != "ready" {
		t.Fatalf("restart=%+v", restarted)
	}
	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "lease-4", Spec: spec})
	if deleted.Status != "deleted" {
		t.Fatalf("delete=%+v", deleted)
	}
	assertManagedK3sObjects(t, spec, 0)
}

func assertValkeyAuth(t *testing.T, spec resourcev1.ManagedResourceSpec, credential *resourcev1.ManagedResourceCredential) {
	t.Helper()
	name := spec.Connection.ServiceName
	namespace := managedResourceNamespace(spec)
	unauth := kubectl(t, "exec", "deployment/"+name, "-n", namespace, "-c", "redis", "--", "sh", "-ec", "valkey-cli ping 2>&1 || true")
	if !strings.Contains(strings.ToUpper(unauth), "NOAUTH") {
		t.Fatalf("unauthenticated output=%q", unauth)
	}
	wrong := kubectl(t, "exec", "deployment/"+name, "-n", namespace, "-c", "redis", "--", "sh", "-ec", "VALKEYCLI_AUTH=wrong valkey-cli --user opsi ping 2>&1 || true")
	if !strings.Contains(strings.ToUpper(wrong), "WRONGPASS") {
		t.Fatalf("wrong password output=%q", wrong)
	}
	correct := kubectl(t, "exec", "deployment/"+name, "-n", namespace, "-c", "redis", "--", "sh", "-ec", "export VALKEYCLI_AUTH=$(cat /run/opsi-valkey/password); valkey-cli --user $(cat /run/opsi-valkey/username) ping; valkey-cli --user $(cat /run/opsi-valkey/username) set p07b2 value; valkey-cli --user $(cat /run/opsi-valkey/username) get p07b2")
	if !strings.Contains(correct, "PONG") || !strings.Contains(correct, "OK") || !strings.Contains(correct, "value") || strings.Contains(correct, credential.Password) {
		t.Fatalf("authenticated output=%q", correct)
	}
}

func assertManagedK3sObjects(t *testing.T, spec resourcev1.ManagedResourceSpec, want int) {
	t.Helper()
	namespace := managedResourceNamespace(spec)
	selector := "opsi.dev/managed-resource-id=" + managedLabel(spec.ResourceID)
	for _, kind := range []string{"deployment", "service"} {
		out := kubectl(t, "get", kind, "-n", namespace, "-l", selector, "-o", "name")
		count := 0
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line != "" {
				count++
			}
		}
		if count != want {
			t.Fatalf("%s count=%d want=%d output=%q", kind, count, want, out)
		}
	}
	if want == 1 {
		image := kubectl(t, "get", "pod", "-n", namespace, "-l", selector, "-o", "jsonpath={.items[0].status.containerStatuses[0].imageID}")
		if !imageMatches(image, spec.Image) {
			t.Fatalf("running image=%q want=%q", image, spec.Image)
		}
	}
}

func assertNATSConnection(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	namespace := managedResourceNamespace(spec)
	name := "nats-client-" + strings.ToLower(time.Now().UTC().Format("150405"))
	const busyboxImage = "docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
	out := kubectl(t, "run", name, "-n", namespace, "--restart=Never", "--rm", "-i", "--image="+busyboxImage, "--command", "--", "sh", "-ec", "nc -w 5 "+spec.Connection.Host+" 4222 | head -n 1")
	if !strings.Contains(out, "INFO") {
		t.Fatalf("NATS connection output=%q", out)
	}
}

func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
