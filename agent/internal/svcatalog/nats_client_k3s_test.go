package svcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const (
	natsClientTimeout = 30 * time.Second
	natsClientPoll    = 200 * time.Millisecond
)

func assertNATSConnection(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	namespace := managedResourceNamespace(spec)
	t.Logf("NATS service DNS=%s namespace=%s", spec.Connection.Host, namespace)
	waitForNATSEndpoint(t, spec)
	for run := 1; run <= 3; run++ {
		name := fmt.Sprintf("nats-client-%d-%d", time.Now().UnixNano(), run)
		out, err := runNATSClient(namespace, spec.Connection.Host, name)
		if err != nil {
			t.Fatalf("NATS INFO run %d/3: %v", run, err)
		}
		t.Logf("NATS INFO run %d/3 result=%q cleanup=PASS", run, strings.TrimSpace(out))
	}
}

func waitForNATSEndpoint(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), natsClientTimeout)
	defer cancel()
	namespace := managedResourceNamespace(spec)
	selector := "opsi.dev/managed-resource-id=" + managedLabel(spec.ResourceID)
	lastReason := "endpoint state not observed"
	for {
		service, serviceErr, err := kubectlStreams(ctx, "get", "service", spec.Connection.ServiceName, "-n", namespace, "-o", "json")
		if err != nil {
			lastReason = fmt.Sprintf("Service unavailable: %v stderr=%q", err, strings.TrimSpace(serviceErr))
		} else {
			pods, podsErr, podsCommandErr := kubectlStreams(ctx, "get", "pod", "-n", namespace, "-l", selector, "-o", "json")
			endpoints, endpointsErr, endpointsCommandErr := kubectlStreams(ctx, "get", "endpoints", spec.Connection.ServiceName, "-n", namespace, "-o", "json")
			if podsCommandErr != nil || endpointsCommandErr != nil {
				lastReason = fmt.Sprintf("endpoint authority unavailable: pods=%v stderr=%q endpoints=%v stderr=%q", podsCommandErr, strings.TrimSpace(podsErr), endpointsCommandErr, strings.TrimSpace(endpointsErr))
			} else if ready, reason := natsEndpointReady(service, pods, endpoints); ready {
				t.Logf("NATS endpoint ready: %s", reason)
				return
			} else {
				lastReason = reason
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("NATS endpoint not ready: %s (timeout=%s)", lastReason, natsClientTimeout)
		case <-time.After(natsClientPoll):
		}
	}
}

func natsEndpointReady(serviceJSON, podsJSON, endpointsJSON string) (bool, string) {
	var service, pods, endpoints map[string]any
	if json.Unmarshal([]byte(serviceJSON), &service) != nil || json.Unmarshal([]byte(podsJSON), &pods) != nil || json.Unmarshal([]byte(endpointsJSON), &endpoints) != nil {
		return false, "invalid Service, Pod, or Endpoints JSON"
	}
	serviceSpec, _ := service["spec"].(map[string]any)
	servicePorts, _ := serviceSpec["ports"].([]any)
	if !hasPort(servicePorts, 4222) {
		return false, "Service does not expose port 4222"
	}
	items, _ := pods["items"].([]any)
	if len(items) != 1 {
		return false, fmt.Sprintf("managed NATS Pod count=%d want=1", len(items))
	}
	pod, _ := items[0].(map[string]any)
	if !podReady(pod) {
		return false, "managed NATS Pod is not Ready"
	}
	metadata, _ := pod["metadata"].(map[string]any)
	status, _ := pod["status"].(map[string]any)
	podName, _ := metadata["name"].(string)
	podNamespace, _ := metadata["namespace"].(string)
	podIP, _ := status["podIP"].(string)
	subsets, _ := endpoints["subsets"].([]any)
	for _, rawSubset := range subsets {
		subset, _ := rawSubset.(map[string]any)
		ports, _ := subset["ports"].([]any)
		if !hasPort(ports, 4222) {
			continue
		}
		addresses, _ := subset["addresses"].([]any)
		for _, rawAddress := range addresses {
			address, _ := rawAddress.(map[string]any)
			target, _ := address["targetRef"].(map[string]any)
			if address["ip"] == podIP && target["kind"] == "Pod" && target["name"] == podName && target["namespace"] == podNamespace {
				return true, fmt.Sprintf("Service exists, endpoint=%s maps Pod=%s port=4222", podIP, podName)
			}
		}
	}
	return false, fmt.Sprintf("no ready endpoint maps Pod=%s IP=%s port=4222", podName, podIP)
}

func hasPort(ports []any, want int) bool {
	for _, raw := range ports {
		port, _ := raw.(map[string]any)
		if intValue(port["port"]) == want {
			return true
		}
	}
	return false
}

func runNATSClient(namespace, host, name string) (protocol string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), natsClientTimeout)
	defer cancel()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		stdout, stderr, cleanupErr := kubectlStreams(cleanupCtx, "delete", "pod", name, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=10s")
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("delete client Pod: %w stdout=%q stderr=%q", cleanupErr, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
			if err == nil {
				err = cleanupErr
			} else {
				err = fmt.Errorf("%v; cleanup: %w", err, cleanupErr)
			}
		}
	}()
	createOut, createErrOut, createErr := kubectlStreams(ctx, natsClientArgs(name, namespace, host)...)
	if createErr != nil {
		return "", fmt.Errorf("create client Pod: %w stdout=%q stderr=%q", createErr, strings.TrimSpace(createOut), strings.TrimSpace(createErrOut))
	}
	exitCode, containerErr, waitErr := waitForNATSClient(ctx, namespace, name)
	if waitErr != nil {
		return "", waitErr
	}
	protocol, logsErrOut, logsErr := kubectlStreams(ctx, "logs", name, "-n", namespace)
	if logsErr != nil {
		return "", fmt.Errorf("collect client stdout: %w kubectl_stderr=%q", logsErr, strings.TrimSpace(logsErrOut))
	}
	if exitCode != 0 {
		return protocol, fmt.Errorf("client exit=%d container_stdout=%q container_stderr=%q", exitCode, strings.TrimSpace(protocol), strings.TrimSpace(containerErr))
	}
	if !strings.HasPrefix(strings.TrimSpace(protocol), "INFO ") {
		return protocol, fmt.Errorf("protocol stdout does not start with INFO: container_stdout=%q container_stderr=%q", strings.TrimSpace(protocol), strings.TrimSpace(containerErr))
	}
	return protocol, nil
}

func natsClientArgs(name, namespace, host string) []string {
	const script = `exec 2>/dev/termination-log; nslookup "$1" >/dev/null; line=$(timeout 8 nc -w 5 "$1" 4222 | head -n 1); [ -n "$line" ]; printf '%s\n' "$line"`
	return []string{"run", name, "-n", namespace, "--restart=Never", "--image=" + k3sDNSImage, "--command", "--", "sh", "-ec", script, "probe", host}
}

func waitForNATSClient(ctx context.Context, namespace, name string) (int, string, error) {
	lastState := "Pod has no container status"
	for {
		stdout, stderr, err := kubectlStreams(ctx, "get", "pod", name, "-n", namespace, "-o", "json")
		if err != nil {
			lastState = fmt.Sprintf("kubectl get: %v stderr=%q", err, strings.TrimSpace(stderr))
		} else {
			var pod struct {
				Status struct {
					Phase             string `json:"phase"`
					ContainerStatuses []struct {
						State struct {
							Terminated *struct {
								ExitCode int    `json:"exitCode"`
								Message  string `json:"message"`
							} `json:"terminated"`
						} `json:"state"`
					} `json:"containerStatuses"`
				} `json:"status"`
			}
			if decodeErr := json.Unmarshal([]byte(stdout), &pod); decodeErr != nil {
				lastState = "invalid client Pod JSON: " + decodeErr.Error()
			} else if len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].State.Terminated != nil {
				terminated := pod.Status.ContainerStatuses[0].State.Terminated
				return terminated.ExitCode, terminated.Message, nil
			} else {
				lastState = "phase=" + pod.Status.Phase
			}
		}
		select {
		case <-ctx.Done():
			return 0, "", fmt.Errorf("client Pod did not complete: %s (timeout=%s)", lastState, natsClientTimeout)
		case <-time.After(natsClientPoll):
		}
	}
}

func kubectlStreams(ctx context.Context, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestNATSClientArgsAreNonInteractive(t *testing.T) {
	args := natsClientArgs("client", "namespace", "nats.namespace.svc.cluster.local")
	for _, arg := range args {
		if arg == "--rm" || arg == "-i" || arg == "--stdin" {
			t.Fatalf("interactive lifecycle argument present: %q", args)
		}
	}
	if args[0] != "run" || args[len(args)-1] != "nats.namespace.svc.cluster.local" || !strings.Contains(strings.Join(args, " "), "nc -w 5") {
		t.Fatalf("unexpected NATS client command: %q", args)
	}
}

func TestNATSEndpointReadyRequiresExactPodAndPort(t *testing.T) {
	service := `{"spec":{"ports":[{"port":4222}]}}`
	pods := `{"items":[{"metadata":{"name":"nats-0","namespace":"namespace"},"status":{"podIP":"10.42.0.7","conditions":[{"type":"Ready","status":"True"}]}}]}`
	endpoints := `{"subsets":[{"addresses":[{"ip":"10.42.0.7","targetRef":{"kind":"Pod","name":"nats-0","namespace":"namespace"}}],"ports":[{"port":4222}]}]}`
	if ready, reason := natsEndpointReady(service, pods, endpoints); !ready {
		t.Fatalf("ready endpoint rejected: %s", reason)
	}
	wrongPod := strings.Replace(endpoints, `"name":"nats-0"`, `"name":"other"`, 1)
	if ready, _ := natsEndpointReady(service, pods, wrongPod); ready {
		t.Fatal("endpoint for a different Pod accepted")
	}
	wrongPort := strings.Replace(endpoints, `"port":4222`, `"port":4223`, 1)
	if ready, _ := natsEndpointReady(service, pods, wrongPort); ready {
		t.Fatal("endpoint on a different port accepted")
	}
}
