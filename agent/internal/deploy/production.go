package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

const ProductionFieldManager = "opsi-r5-010"

const (
	MaxCommandOutputBytes = 256 * 1024
	MaxCommandErrorBytes  = 512
)

type CommandRunner interface {
	Run(context.Context, []byte, string, ...string) ([]byte, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	output := &boundedCommandBuffer{limit: MaxCommandOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if output.overflow {
		return nil, errors.New("command output exceeded the allowed bound")
	}
	if err != nil {
		message := strings.TrimSpace(string(output.Bytes()))
		if len(message) > MaxCommandErrorBytes {
			message = message[:MaxCommandErrorBytes]
		}
		if message == "" {
			return nil, fmt.Errorf("%s failed", name)
		}
		return nil, fmt.Errorf("%s failed: %s", name, RedactSensitive(message))
	}
	return append([]byte(nil), output.Bytes()...), nil
}

type boundedCommandBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedCommandBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return written, nil
	}
	if len(data) > remaining {
		b.overflow = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return written, nil
}

// ProductionAdapter is the RolloutRuntime implementation for Opsi-owned K3s resources.
type ProductionAdapter struct {
	Runner              CommandRunner
	TLSResolver         TLSSecretResolver
	RoutingProbe        RoutingProbe
	RequireLocalRouting bool
	KubectlPath         string
	PollInterval        time.Duration
	Timeout             time.Duration
	ReadTimeout         time.Duration
	MaxOutputBytes      int
}

type renderedResources struct {
	Namespace       string
	DeploymentName  string
	ServiceName     string
	Selector        map[string]string
	Deployment      map[string]any
	Service         map[string]any
	NamespaceObject map[string]any
}

func (a ProductionAdapter) getJSON(ctx context.Context, kind, name, namespace string, selector ...map[string]string) (map[string]any, error) {
	args := []string{"get", kind, name}
	if kind == "pods" {
		args = []string{"get", "pods", "-n", namespace, "-l", selectorString(selector[0]), "-o", "json"}
	} else {
		args = append(args, "-n", namespace, "-o", "json")
	}
	out, err := a.Runner.Run(ctx, nil, a.KubectlPath, args...)
	if err != nil {
		return nil, err
	}
	if len(out) > a.kubernetesOutputLimit() {
		return nil, errors.New("Kubernetes response exceeded the allowed bound")
	}
	var value map[string]any
	if err := decodeSingleJSON(out, &value); err != nil {
		return nil, errors.New("Kubernetes returned invalid JSON")
	}
	return value, nil
}

func applicationPodReadiness(pods map[string]any, digest string) (string, int) {
	items, _ := pods["items"].([]any)
	ready := 0
	imageID := ""
	for _, raw := range items {
		pod, _ := raw.(map[string]any)
		status, _ := pod["status"].(map[string]any)
		containers, _ := status["containerStatuses"].([]any)
		for _, rawStatus := range containers {
			container, _ := rawStatus.(map[string]any)
			name, _ := container["name"].(string)
			if name != deploymentv1.ApplicationContainer {
				continue
			}
			id, _ := container["imageID"].(string)
			// Keep the reported identity anchored to an application container
			// running the requested digest, even while old rollout pods remain.
			if id != "" && containsExactDigest(id, digest) {
				imageID = id
			}
			if readyValue, _ := container["ready"].(bool); readyValue && containsExactDigest(id, digest) {
				ready++
			}
		}
	}
	return imageID, ready
}

func containsExactDigest(imageID, digest string) bool {
	if imageID == digest || strings.HasSuffix(imageID, "@"+digest) || strings.HasSuffix(imageID, "://"+digest) {
		return true
	}
	return false
}

func selectorString(selector map[string]string) string {
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+selector[key])
	}
	return strings.Join(parts, ",")
}

func number(value any) int {
	switch value := value.(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
	}
	return 0
}

func deploymentJSONNested(value map[string]any, first, second string) any {
	child, _ := value[first].(map[string]any)
	return child[second]
}

func safeLabel(value string) string {
	value = strings.ToLower(value)
	value = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}

func renderProductionResources(command deploymentv1.AgentCommand) ([]byte, renderedResources, string, error) {
	if err := command.Image.Validate(); err != nil {
		return nil, renderedResources{}, "", err
	}
	spec := command.Workload.Normalize()
	if err := spec.Validate(); err != nil {
		return nil, renderedResources{}, "", err
	}
	namespace := stableDNSName("opsi", command.ProjectID, command.EnvironmentID)
	resourceName := stableDNSName("opsi", spec.ServiceKey, command.RuntimeID)
	selector := map[string]string{
		"app.kubernetes.io/managed-by": "opsi",
		"opsi.dev/project":             safeLabel(command.ProjectID),
		"opsi.dev/environment":         safeLabel(command.EnvironmentID),
		"opsi.dev/runtime":             safeLabel(command.RuntimeID),
		"opsi.dev/service":             safeLabel(spec.ServiceKey),
		"opsi.dev/workload":            resourceName,
	}
	labels := cloneStringMap(selector)
	labels["app.kubernetes.io/name"] = resourceName
	container := map[string]any{
		"name":            deploymentv1.ApplicationContainer,
		"image":           command.Image.Reference,
		"imagePullPolicy": "IfNotPresent",
		"ports":           []any{map[string]any{"name": "http", "containerPort": spec.ContainerPort, "protocol": "TCP"}},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": spec.Resources.Requests.CPU, "memory": spec.Resources.Requests.Memory},
			"limits":   map[string]any{"cpu": spec.Resources.Limits.CPU, "memory": spec.Resources.Limits.Memory},
		},
	}
	if len(spec.Environment) > 0 {
		env := make([]any, 0, len(spec.Environment))
		for _, item := range spec.Environment {
			env = append(env, map[string]any{"name": item.Name, "value": item.Value})
		}
		container["env"] = env
	}
	if spec.ReadinessProbe != nil {
		container["readinessProbe"] = probeObject(*spec.ReadinessProbe)
	}
	if spec.LivenessProbe != nil {
		container["livenessProbe"] = probeObject(*spec.LivenessProbe)
	}
	annotations := map[string]string{"opsi.dev/spec-hash": command.SpecHash, "opsi.dev/image-digest": command.Image.Digest}
	deployment := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": resourceName, "namespace": namespace, "labels": labels, "annotations": annotations},
		"spec":     map[string]any{"replicas": spec.Replicas, "selector": map[string]any{"matchLabels": selector}, "template": map[string]any{"metadata": map[string]any{"labels": selector, "annotations": annotations}, "spec": map[string]any{"terminationGracePeriodSeconds": spec.TerminationGracePeriodSecond, "containers": []any{container}}}},
	}
	service := map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": resourceName, "namespace": namespace, "labels": labels, "annotations": annotations},
		"spec":     map[string]any{"type": "ClusterIP", "selector": selector, "ports": []any{map[string]any{"name": "http", "port": spec.ContainerPort, "targetPort": "http", "protocol": "TCP"}}},
	}
	namespaceObject := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "opsi", "opsi.dev/project": safeLabel(command.ProjectID), "opsi.dev/environment": safeLabel(command.EnvironmentID)}}}
	docs := []any{namespaceObject, deployment, service}
	data, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": docs})
	if err != nil {
		return nil, renderedResources{}, "", err
	}
	return data, renderedResources{Namespace: namespace, DeploymentName: resourceName, ServiceName: resourceName, Selector: selector, Deployment: deployment, Service: service, NamespaceObject: namespaceObject}, namespace, nil
}

func stableDNSName(prefix string, identities ...string) string {
	parts := []string{safeLabel(prefix)}
	for _, identity := range identities {
		part := safeLabel(identity)
		if len(part) > 18 {
			part = part[:18]
		}
		parts = append(parts, strings.Trim(part, "-"))
	}
	base := strings.Trim(strings.Join(parts, "-"), "-")
	sum := sha256.Sum256([]byte(strings.Join(identities, "\x00")))
	suffix := hex.EncodeToString(sum[:])[:10]
	if len(base) > 52 {
		base = strings.TrimRight(base[:52], "-")
	}
	if base == "" {
		base = "opsi"
	}
	return base + "-" + suffix
}

func probeObject(probe deploymentv1.Probe) map[string]any {
	return map[string]any{"httpGet": map[string]any{"path": probe.Path, "port": probe.Port, "scheme": "HTTP"}, "initialDelaySeconds": probe.InitialDelaySeconds, "periodSeconds": probe.PeriodSeconds, "timeoutSeconds": probe.TimeoutSeconds, "failureThreshold": probe.FailureThreshold}
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
