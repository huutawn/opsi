package svcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const (
	k3sInfrastructureTimeout = 8 * time.Minute
	k3sInfrastructurePoll    = 2 * time.Second
	k3sDNSImage              = "docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
)

func TestManagedResourceRealK3sNATS(t *testing.T) {
	if os.Getenv("OPSI_E2E_K3S_NATS") != "1" {
		t.Skip("set OPSI_E2E_K3S_NATS=1 with KUBECONFIG pointing to a disposable K3s cluster")
	}
	requireK3sInfrastructure(t)
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
	requireK3sInfrastructure(t)
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
		pod := kubectl(t, "get", "pod", "-n", namespace, "-l", selector, "-o", "json")
		var object map[string]any
		if err := json.Unmarshal([]byte(pod), &object); err != nil {
			t.Fatalf("managed pod json: %v", err)
		}
		items, _ := object["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("managed pod count=%d output=%s", len(items), pod)
		}
		item, _ := items[0].(map[string]any)
		if !podReady(item) {
			t.Fatalf("managed pod is not Ready: %s", pod)
		}
		containerStatuses, _ := item["status"].(map[string]any)
		containers, _ := containerStatuses["containerStatuses"].([]any)
		if len(containers) == 0 {
			t.Fatalf("managed pod has no container status: %s", pod)
		}
		status, _ := containers[0].(map[string]any)
		image, _ := status["imageID"].(string)
		t.Logf("NATS/Valkey exact imageID=%s", image)
		if !imageMatches(image, spec.Image) {
			t.Fatalf("running image=%q want=%q", image, spec.Image)
		}
		service := kubectl(t, "get", "service", "-n", namespace, "-l", selector, "-o", "jsonpath={.items[0].spec.clusterIP}")
		if strings.TrimSpace(service) == "" || strings.TrimSpace(service) == "None" {
			t.Fatalf("managed service has no ClusterIP: %q", service)
		}
	}
}

func assertNATSConnection(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	namespace := managedResourceNamespace(spec)
	t.Logf("NATS service DNS=%s namespace=%s", spec.Connection.Host, namespace)
	name := fmt.Sprintf("nats-client-%d", time.Now().UnixNano())
	dns := kubectl(t, "run", name+"-dns", "-n", namespace, "--restart=Never", "--rm", "-i", "--image="+k3sDNSImage, "--command", "--", "nslookup", spec.Connection.Host)
	t.Logf("NATS service DNS resolution=%q", strings.TrimSpace(dns))
	out := kubectl(t, "run", name, "-n", namespace, "--restart=Never", "--rm", "-i", "--image="+k3sDNSImage, "--command", "--", "sh", "-ec", "nc -w 5 "+spec.Connection.Host+" 4222 | head -n 1")
	if !strings.Contains(out, "INFO") {
		t.Fatalf("NATS connection output=%q", out)
	}
	t.Logf("NATS INFO result=%q", strings.TrimSpace(out))
}

func requireK3sInfrastructure(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), k3sInfrastructureTimeout)
	defer cancel()
	var lastReason string
	for {
		nodes, nodesErr := kubectlOutput(ctx, "get", "nodes", "-o", "json")
		deployment, deploymentErr := kubectlOutput(ctx, "get", "deployment", "coredns", "-n", "kube-system", "-o", "json")
		pods, podsErr := kubectlOutput(ctx, "get", "pods", "-n", "kube-system", "-l", "k8s-app=kube-dns", "-o", "json")
		if nodesErr == nil && deploymentErr == nil && podsErr == nil {
			ready, reason := k3sStateReady(nodes, deployment, pods)
			if ready {
				if dnsSmoke(ctx) {
					t.Log("K3S_INFRA_READY: node Ready, CoreDNS available/Pod Ready, cluster DNS smoke PASS")
					return
				}
				lastReason = "cluster DNS smoke pending"
			} else {
				lastReason = reason
			}
		} else {
			lastReason = fmt.Sprintf("kubectl state unavailable: nodes=%v deployment=%v pods=%v", nodesErr, deploymentErr, podsErr)
		}
		select {
		case <-ctx.Done():
			captureK3sDiagnostics(t, lastReason)
			t.Fatalf("K3S_INFRA_NOT_READY: %s (timeout=%s)", lastReason, k3sInfrastructureTimeout)
		case <-time.After(k3sInfrastructurePoll):
		}
	}
}

func k3sStateReady(nodesJSON, deploymentJSON, podsJSON string) (bool, string) {
	var nodes, deployment, pods map[string]any
	if err := json.Unmarshal([]byte(nodesJSON), &nodes); err != nil {
		return false, "invalid node status"
	}
	if err := json.Unmarshal([]byte(deploymentJSON), &deployment); err != nil {
		return false, "invalid CoreDNS Deployment status"
	}
	if err := json.Unmarshal([]byte(podsJSON), &pods); err != nil {
		return false, "invalid CoreDNS Pod status"
	}
	items, _ := nodes["items"].([]any)
	if len(items) == 0 {
		return false, "no nodes"
	}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if !conditionTrue(item, "Ready") {
			return false, "node is not Ready"
		}
	}
	spec, _ := deployment["spec"].(map[string]any)
	status, _ := deployment["status"].(map[string]any)
	desired := intValue(spec["replicas"])
	available := intValue(status["availableReplicas"])
	if desired < 1 || available < desired {
		return false, fmt.Sprintf("CoreDNS Deployment unavailable: desired=%d available=%d", desired, available)
	}
	readyPods := 0
	podItems, _ := pods["items"].([]any)
	for _, raw := range podItems {
		item, _ := raw.(map[string]any)
		if conditionTrue(item, "Ready") {
			readyPods++
		}
	}
	if readyPods < desired {
		return false, fmt.Sprintf("CoreDNS Pods not Ready: ready=%d desired=%d", readyPods, desired)
	}
	return true, ""
}

func conditionTrue(object map[string]any, conditionType string) bool {
	status, _ := object["status"].(map[string]any)
	conditions, _ := status["conditions"].([]any)
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		if condition["type"] == conditionType && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func podReady(object map[string]any) bool { return conditionTrue(object, "Ready") }

func intValue(value any) int {
	if number, ok := value.(float64); ok {
		return int(number)
	}
	return 0
}

func dnsSmoke(ctx context.Context) bool {
	name := fmt.Sprintf("k3s-dns-smoke-%d", time.Now().UnixNano())
	_, err := kubectlOutput(ctx, "run", name, "--restart=Never", "--rm", "-i", "--image="+k3sDNSImage, "--command", "--", "nslookup", "kubernetes.default.svc.cluster.local")
	if err == nil {
		return true
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = kubectlOutput(cleanupCtx, "delete", "pod", name, "--ignore-not-found")
	return false
}

func kubectlOutput(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	return string(out), err
}

func captureK3sDiagnostics(t *testing.T, reason string) {
	t.Helper()
	dir := os.Getenv("OPSI_K3S_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "k3s-readiness-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Logf("K3S_INFRA_NOT_READY diagnostics unavailable: %v", err)
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "reason.txt"), []byte(reason+"\n"), 0o600)
	commands := map[string][]string{
		"nodes.txt":              {"get", "nodes", "-o", "wide"},
		"kube-system-pods.txt":   {"get", "pods", "-n", "kube-system", "-o", "wide"},
		"coredns-deployment.txt": {"get", "deployment", "coredns", "-n", "kube-system", "-o", "yaml"},
		"events.txt":             {"get", "events", "-A", "--sort-by=.lastTimestamp"},
		"coredns-describe.txt":   {"describe", "pods", "-n", "kube-system", "-l", "k8s-app=kube-dns"},
	}
	for file, args := range commands {
		out, _ := kubectlOutput(context.Background(), args...)
		_ = os.WriteFile(filepath.Join(dir, file), []byte(out), 0o600)
	}
	out, _ := exec.Command("journalctl", "-u", "k3s", "-n", "100", "--no-pager").CombinedOutput()
	_ = os.WriteFile(filepath.Join(dir, "k3s-logs.txt"), out, 0o600)
	t.Logf("K3S_INFRA_NOT_READY evidence=%s", dir)
}

func TestK3sStateReadyRequiresNodeCoreDNSAndPod(t *testing.T) {
	readyNode := `{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
	readyDeployment := `{"spec":{"replicas":1},"status":{"availableReplicas":1}}`
	readyPod := `{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
	if ready, reason := k3sStateReady(readyNode, readyDeployment, readyPod); !ready || reason != "" {
		t.Fatalf("ready state=(%v, %q)", ready, reason)
	}
	cases := []struct {
		name       string
		nodes      string
		deployment string
		pods       string
	}{
		{"node not ready", `{"items":[{"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`, readyDeployment, readyPod},
		{"CoreDNS unavailable", readyNode, `{"spec":{"replicas":1},"status":{"availableReplicas":0}}`, readyPod},
		{"CoreDNS pod not ready", readyNode, readyDeployment, `{"items":[{"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ready, reason := k3sStateReady(tc.nodes, tc.deployment, tc.pods); ready || reason == "" {
				t.Fatalf("state=(%v, %q)", ready, reason)
			}
		})
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
