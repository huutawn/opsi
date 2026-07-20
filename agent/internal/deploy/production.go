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

// ProductionRuntime is the only Agent execution boundary for immutable image jobs.
// The legacy Git/Build/K3s adapters remain available for development compatibility.
type ProductionRuntime interface {
	Deploy(context.Context, deploymentv1.AgentCommand, ProgressFunc) (Record, error)
}

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

// RegistryCredentialProvider is deliberately separate from command execution.
// The R5-010 live path is anonymous public OCI; private credentials remain a
// typed, scoped extension and are never passed through argv or logs.
type RegistryCredentialProvider interface {
	Credentials(context.Context, deploymentv1.ImmutableImage) (RegistryCredential, error)
}

type RegistryCredential struct {
	Username string
	Secret   string
}

type AnonymousRegistryCredentials struct{}

func (AnonymousRegistryCredentials) Credentials(context.Context, deploymentv1.ImmutableImage) (RegistryCredential, error) {
	return RegistryCredential{}, nil
}

// ProductionAdapter pulls by digest, renders Opsi-owned resources, and checks
// readiness using the application container name instead of container position.
type ProductionAdapter struct {
	Runner              CommandRunner
	Credentials         RegistryCredentialProvider
	TLSResolver         TLSSecretResolver
	RoutingProbe        RoutingProbe
	RequireLocalRouting bool
	KubectlPath         string
	K3sPath             string
	PollInterval        time.Duration
	Timeout             time.Duration
	ReadTimeout         time.Duration
	MaxOutputBytes      int
}

func (a ProductionAdapter) Deploy(ctx context.Context, command deploymentv1.AgentCommand, progress ProgressFunc) (Record, error) {
	if a.Runner == nil {
		a.Runner = ExecCommandRunner{}
	}
	if a.KubectlPath == "" {
		a.KubectlPath = "kubectl"
	}
	if a.K3sPath == "" {
		a.K3sPath = "k3s"
	}
	if a.PollInterval <= 0 {
		a.PollInterval = time.Second
	}
	if a.Timeout <= 0 {
		a.Timeout = 10 * time.Minute
	}
	if a.Credentials == nil {
		a.Credentials = AnonymousRegistryCredentials{}
	}
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()
	start := time.Now().UTC()
	record := Record{DeployID: command.JobID, ProjectID: command.ProjectID, ServiceID: command.Workload.ServiceKey, ServiceName: command.Workload.ServiceKey, StartedAt: start, ImageTag: command.Image.Reference, Status: StatusRunning, TriggeredBy: "cloud", SpecHash: command.SpecHash}
	if err := emitProduction(progress, record, PhasePulling, "pulling immutable OCI image", 15, nil); err != nil {
		return record, err
	}
	imageID, err := a.pull(ctx, command.Image)
	if err != nil {
		return productionFailure(record, progress, "IMAGE_PULL_FAILED", err)
	}
	record.ImageID = imageID
	if err := emitProduction(progress, record, PhaseApplying, "applying Opsi-owned Deployment and ClusterIP Service", 50, nil); err != nil {
		return record, err
	}
	rendered, resources, namespace, err := renderProductionResources(command)
	if err != nil {
		return productionFailure(record, progress, "WORKLOAD_RENDER_FAILED", err)
	}
	if err := a.verifyOwnership(ctx, resources, namespace, command); err != nil {
		return productionFailure(record, progress, "K8S_RESOURCE_OWNERSHIP_CONFLICT", err)
	}
	if _, err := a.Runner.Run(ctx, rendered, a.KubectlPath, "apply", "--server-side", "--field-manager="+ProductionFieldManager, "-f", "-"); err != nil {
		return productionFailure(record, progress, "K8S_APPLY_FAILED", err)
	}
	record.Namespace = namespace
	record.DeploymentName = resources.DeploymentName
	record.KubernetesServiceName = resources.ServiceName
	if err := emitProduction(progress, record, PhaseWatching, "waiting for application container readiness", 70, nil); err != nil {
		return record, err
	}
	ready, observedImageID, available, err := a.waitReady(ctx, resources, namespace, command)
	if err != nil {
		return productionFailure(record, progress, "K8S_READINESS_FAILED", err)
	}
	if !ready || !containsExactDigest(observedImageID, command.Image.Digest) {
		return productionFailure(record, progress, "K8S_IMAGE_ID_MISMATCH", errors.New("application container imageID does not match requested digest"))
	}
	record.AvailableReplicas = available
	record.FinishedAt = time.Now().UTC()
	record.Status = StatusSuccess
	_ = emitProduction(progress, record, PhaseSuccess, "deployment succeeded", 100, nil)
	return record, nil
}

func (a ProductionAdapter) pull(ctx context.Context, image deploymentv1.ImmutableImage) (string, error) {
	if err := image.Validate(); err != nil {
		return "", err
	}
	credentials, err := a.Credentials.Credentials(ctx, image)
	if err != nil {
		return "", err
	}
	if credentials.Username != "" || credentials.Secret != "" {
		return "", errors.New("PRIVATE_REGISTRY_CREDENTIAL_UNSUPPORTED")
	}
	if _, err := a.Runner.Run(ctx, nil, a.K3sPath, "crictl", "pull", image.Reference); err != nil {
		if looksLikeRegistryAuthFailure(err) {
			return "", errors.New("REGISTRY_AUTH_REQUIRED")
		}
		return "", err
	}
	out, err := a.Runner.Run(ctx, nil, a.K3sPath, "crictl", "inspecti", image.Reference)
	if err != nil {
		return "", err
	}
	imageID, ok := runtimeImageID(out, image.Digest)
	if !ok {
		return "", errors.New("runtime image identity did not contain requested digest")
	}
	return imageID, nil
}

func looksLikeRegistryAuthFailure(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unauthorized") || strings.Contains(message, "authentication required") || strings.Contains(message, "401")
}

func runtimeImageID(data []byte, digest string) (string, bool) {
	var document map[string]any
	if json.Unmarshal(data, &document) != nil {
		return "", false
	}
	status, _ := document["status"].(map[string]any)
	if id, _ := status["id"].(string); id == digest {
		return id, true
	}
	if digests, _ := status["repoDigests"].([]any); len(digests) > 0 {
		for _, value := range digests {
			if ref, ok := value.(string); ok && strings.HasSuffix(ref, "@"+digest) {
				return ref, true
			}
		}
	}
	return "", false
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

func (a ProductionAdapter) verifyOwnership(ctx context.Context, resources renderedResources, namespace string, command deploymentv1.AgentCommand) error {
	checks := []struct {
		kind string
		name string
		obj  map[string]any
	}{
		{kind: "namespace", name: namespace, obj: resources.NamespaceObject},
		{kind: "deployment", name: resources.DeploymentName, obj: resources.Deployment},
		{kind: "service", name: resources.ServiceName, obj: resources.Service},
	}
	for _, check := range checks {
		args := []string{"get", check.kind, check.name, "-o", "json", "--ignore-not-found", "--show-managed-fields"}
		if check.kind != "namespace" {
			args = []string{"get", check.kind, check.name, "-n", namespace, "-o", "json", "--ignore-not-found", "--show-managed-fields"}
		}
		out, err := a.Runner.Run(ctx, nil, a.KubectlPath, args...)
		if err != nil {
			return errors.New("Kubernetes ownership read failed")
		}
		if len(out) > a.kubernetesOutputLimit() {
			return errors.New("Kubernetes ownership response exceeded the allowed bound")
		}
		if len(bytes.TrimSpace(out)) == 0 {
			continue
		}
		var current map[string]any
		if err := decodeSingleJSON(out, &current); err != nil {
			return errors.New("existing Kubernetes resource returned invalid JSON")
		}
		metadata, _ := current["metadata"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)
		managedBy, _ := labels["app.kubernetes.io/managed-by"].(string)
		project, _ := labels["opsi.dev/project"].(string)
		environment, _ := labels["opsi.dev/environment"].(string)
		owned := managedBy == "opsi" && project == safeLabel(command.ProjectID) && environment == safeLabel(command.EnvironmentID)
		if check.kind != "namespace" {
			runtime, _ := labels["opsi.dev/runtime"].(string)
			service, _ := labels["opsi.dev/service"].(string)
			owned = owned && runtime == safeLabel(command.RuntimeID) && service == safeLabel(command.Workload.ServiceKey)
		}
		if !owned {
			return fmt.Errorf("existing %s/%s is not Opsi-owned", check.kind, check.name)
		}
	}
	return nil
}

func (a ProductionAdapter) waitReady(ctx context.Context, resources renderedResources, namespace string, command deploymentv1.AgentCommand) (bool, string, int32, error) {
	deadline := time.NewTimer(a.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(a.PollInterval)
	defer ticker.Stop()
	for {
		deploymentJSON, err := a.getJSON(ctx, "deployment", resources.DeploymentName, namespace)
		if err != nil {
			return false, "", 0, err
		}
		metadata, _ := deploymentJSON["metadata"].(map[string]any)
		status, _ := deploymentJSON["status"].(map[string]any)
		generation := number(metadata["generation"])
		observed := number(status["observedGeneration"])
		desired := int32(number(deploymentJSONNested(deploymentJSON, "spec", "replicas")))
		available := int32(number(status["availableReplicas"]))
		if desired == 0 {
			desired = command.Workload.Replicas
		}
		pods, err := a.getJSON(ctx, "pods", "", namespace, resources.Selector)
		if err != nil {
			return false, "", available, err
		}
		imageID, readyCount := applicationPodReadiness(pods, command.Image.Digest)
		if observed >= generation && available >= desired && int32(readyCount) >= desired {
			return true, imageID, available, nil
		}
		select {
		case <-ctx.Done():
			return false, imageID, available, ctx.Err()
		case <-deadline.C:
			return false, imageID, available, errors.New("deployment readiness timeout")
		case <-ticker.C:
		}
	}
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

func emitProduction(progress ProgressFunc, record Record, phase, message string, percent int32, err error) error {
	return emit(progress, record, phase, message, percent, err)
}

func productionFailure(record Record, progress ProgressFunc, code string, cause error) (Record, error) {
	record.Status = StatusFailed
	record.Error = code + ": " + RedactSensitive(cause.Error())
	record.FinishedAt = time.Now().UTC()
	_ = emitProduction(progress, record, PhaseFailed, record.Error, 100, errors.New(record.Error))
	return record, cause
}
