package svcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const managedResourceFieldManager = "opsi-p07b1-managed-resource"

type ManagedResourceReconciler struct {
	Runner       deploy.CommandRunner
	KubectlPath  string
	Timeout      time.Duration
	PollInterval time.Duration
}

func (r ManagedResourceReconciler) Reconcile(ctx context.Context, lease cloudrelay.ManagedResourceLease) cloudrelay.ManagedResourceResult {
	result := cloudrelay.ManagedResourceResult{LeaseToken: lease.LeaseToken}
	if err := lease.Spec.Validate(); err != nil {
		result.Status, result.FailureCode, result.FailureMessageRedacted = "failed", "MANAGED_RESOURCE_SPEC_INVALID", err.Error()
		return result
	}
	if lease.Action == "delete" {
		evidence, err := r.delete(ctx, lease.Spec)
		if err != nil {
			result.Status, result.FailureCode, result.FailureMessageRedacted = "failed", "MANAGED_RESOURCE_DELETE_FAILED", err.Error()
			return result
		}
		result.Status, result.Evidence = "deleted", evidence
		return result
	}
	evidence, err := r.apply(ctx, lease.Spec)
	if err != nil {
		result.Status, result.FailureCode, result.FailureMessageRedacted = "failed", failureCode(err), err.Error()
		return result
	}
	result.Status, result.Evidence = "ready", evidence
	return result
}

func (r ManagedResourceReconciler) apply(ctx context.Context, spec resourcev1.ManagedResourceSpec) (*resourcev1.ManagedResourceEvidence, error) {
	if err := r.ensureNamespace(ctx, spec); err != nil {
		return nil, err
	}
	for _, object := range managedResourceObjects(spec) {
		current, err := r.get(ctx, strings.ToLower(object["kind"].(string)), metadataString(object, "name"), metadataString(object, "namespace"))
		if err != nil {
			return nil, err
		}
		if current != nil && !exactManagedResourceOwnership(current, spec) {
			return nil, errors.New("existing Kubernetes object has different Opsi managed-resource ownership")
		}
		manifest := cloneObject(object)
		verb := "create"
		if current != nil {
			verb = "replace"
			preserveManagedMetadata(current, manifest)
			if manifest["kind"] == "Service" {
				preserveManagedServiceSpec(current, manifest)
			}
		}
		data, _ := json.Marshal(manifest)
		if _, err := r.run(ctx, data, verb, "--field-manager="+managedResourceFieldManager, "-f", "-"); err != nil {
			return nil, err
		}
	}
	return r.waitReady(ctx, spec)
}

func (r ManagedResourceReconciler) ensureNamespace(ctx context.Context, spec resourcev1.ManagedResourceSpec) error {
	name := managedResourceNamespace(spec)
	current, err := r.get(ctx, "namespace", name, "")
	if err != nil {
		return err
	}
	labels := map[string]string{"app.kubernetes.io/managed-by": "opsi", "opsi.dev/project": managedLabel(spec.ProjectID), "opsi.dev/environment": managedLabel(spec.EnvironmentID)}
	if current != nil {
		actual := stringMap(nested(current, "metadata", "labels"))
		for key, value := range labels {
			if actual[key] != value {
				return errors.New("existing Kubernetes namespace has different Opsi ownership")
			}
		}
		return nil
	}
	object := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name, "labels": labels}}
	data, _ := json.Marshal(object)
	_, err = r.run(ctx, data, "create", "--field-manager="+managedResourceFieldManager, "-f", "-")
	return err
}

func (r ManagedResourceReconciler) delete(ctx context.Context, spec resourcev1.ManagedResourceSpec) (*resourcev1.ManagedResourceEvidence, error) {
	for _, kind := range []string{"deployment", "service"} {
		current, err := r.get(ctx, kind, spec.Connection.ServiceName, managedResourceNamespace(spec))
		if err != nil {
			return nil, err
		}
		if current == nil {
			continue
		}
		if !exactManagedResourceOwnership(current, spec) {
			return nil, errors.New("refusing to delete Kubernetes object with different Opsi managed-resource ownership")
		}
		if _, err := r.run(ctx, nil, "delete", kind, spec.Connection.ServiceName, "-n", managedResourceNamespace(spec), "--wait=true", "--timeout=2m"); err != nil {
			return nil, err
		}
	}
	return &resourcev1.ManagedResourceEvidence{ObservedSpecHash: spec.SpecHash, Deleted: true, ObservedAt: time.Now().UTC()}, nil
}

func (r ManagedResourceReconciler) waitReady(ctx context.Context, spec resourcev1.ManagedResourceSpec) (*resourcev1.ManagedResourceEvidence, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	interval := r.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		evidence, err := r.observe(ctx, spec)
		if err == nil && evidence.WorkloadReady && evidence.PodReady && evidence.ServiceReady && evidence.Image == spec.Image {
			return evidence, nil
		}
		if err == nil && evidence.Image != "" && evidence.Image != spec.Image {
			return evidence, runtimeMismatchError{}
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("managed resource readiness cancelled")
		case <-deadline.C:
			return evidence, errors.New("managed resource readiness timed out")
		case <-ticker.C:
		}
	}
}

func (r ManagedResourceReconciler) observe(ctx context.Context, spec resourcev1.ManagedResourceSpec) (*resourcev1.ManagedResourceEvidence, error) {
	namespace := managedResourceNamespace(spec)
	deployment, err := r.get(ctx, "deployment", spec.Connection.ServiceName, namespace)
	if err != nil || deployment == nil || !exactManagedResourceOwnership(deployment, spec) {
		return &resourcev1.ManagedResourceEvidence{}, err
	}
	service, err := r.get(ctx, "service", spec.Connection.ServiceName, namespace)
	if err != nil || service == nil || !exactManagedResourceOwnership(service, spec) {
		return &resourcev1.ManagedResourceEvidence{}, err
	}
	podsRaw, err := r.run(ctx, nil, "get", "pods", "-n", namespace, "-l", selectorString(managedResourceLabels(spec)), "-o", "json")
	if err != nil {
		return nil, err
	}
	var pods map[string]any
	if json.Unmarshal(podsRaw, &pods) != nil {
		return nil, errors.New("invalid Kubernetes pod evidence")
	}
	available := int32(number(nested(deployment, "status", "availableReplicas")))
	image, podReady := managedPodEvidence(pods, spec)
	workloadReady := available >= spec.Replicas && number(nested(deployment, "status", "observedGeneration")) >= number(nested(deployment, "metadata", "generation"))
	serviceReady := nested(service, "spec", "clusterIP") != nil && serviceHasPort(service, spec.Ports[0].Port)
	return &resourcev1.ManagedResourceEvidence{ObservedSpecHash: spec.SpecHash, WorkloadReady: workloadReady, PodReady: podReady >= spec.Replicas, ServiceReady: serviceReady, Image: image, AvailableReplicas: available, ObservedAt: time.Now().UTC()}, nil
}

func managedResourceObjects(spec resourcev1.ManagedResourceSpec) []map[string]any {
	namespace := managedResourceNamespace(spec)
	labels := managedResourceLabels(spec)
	selector := managedResourceOwnershipLabels(spec)
	annotations := managedResourceAnnotations(spec)
	container := map[string]any{
		"name": "nats", "image": spec.Image, "imagePullPolicy": "IfNotPresent", "args": []any{"--port", strconv.Itoa(int(spec.Ports[0].Port))},
		"ports":          []any{map[string]any{"name": "nats", "containerPort": spec.Ports[0].Port, "protocol": "TCP"}},
		"resources":      map[string]any{"requests": map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)}, "limits": map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)}},
		"readinessProbe": map[string]any{"tcpSocket": map[string]any{"port": "nats"}, "initialDelaySeconds": 1, "periodSeconds": 2, "timeoutSeconds": 1, "failureThreshold": 10},
	}
	deployment := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": spec.Connection.ServiceName, "namespace": namespace, "labels": labels, "annotations": annotations}, "spec": map[string]any{"replicas": spec.Replicas, "selector": map[string]any{"matchLabels": selector}, "template": map[string]any{"metadata": map[string]any{"labels": labels, "annotations": annotations}, "spec": map[string]any{"containers": []any{container}}}}}
	service := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": spec.Connection.ServiceName, "namespace": namespace, "labels": labels, "annotations": annotations}, "spec": map[string]any{"type": "ClusterIP", "selector": selector, "ports": []any{map[string]any{"name": "nats", "port": spec.Ports[0].Port, "targetPort": "nats", "protocol": "TCP"}}}}
	return []map[string]any{deployment, service}
}

func managedResourceNamespace(spec resourcev1.ManagedResourceSpec) string {
	return deploymentv1.StableDNSName("opsi", spec.ProjectID, spec.EnvironmentID)
}

func managedResourceLabels(spec resourcev1.ManagedResourceSpec) map[string]string {
	labels := managedResourceOwnershipLabels(spec)
	labels["opsi.dev/managed-resource-spec"] = managedLabel(spec.SpecHash)
	return labels
}

func managedResourceOwnershipLabels(spec resourcev1.ManagedResourceSpec) map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "opsi", "opsi.dev/project": managedLabel(spec.ProjectID), "opsi.dev/environment": managedLabel(spec.EnvironmentID), "opsi.dev/managed-resource-id": managedLabel(spec.ResourceID), "opsi.dev/managed-resource-type": string(spec.ResourceType)}
}

func exactManagedResourceOwnership(object map[string]any, spec resourcev1.ManagedResourceSpec) bool {
	labels := stringMap(nested(object, "metadata", "labels"))
	for key, value := range managedResourceOwnershipLabels(spec) {
		if labels[key] != value {
			return false
		}
	}
	annotations := stringMap(nested(object, "metadata", "annotations"))
	for key, value := range managedResourceOwnershipAnnotations(spec) {
		if annotations[key] != value {
			return false
		}
	}
	return true
}

func managedResourceAnnotations(spec resourcev1.ManagedResourceSpec) map[string]string {
	annotations := managedResourceOwnershipAnnotations(spec)
	annotations["opsi.dev/managed-resource-spec-hash"] = spec.SpecHash
	annotations["opsi.dev/topology-hash"] = spec.TopologyHash
	annotations["opsi.dev/configuration-hash"] = spec.ConfigurationHash
	return annotations
}

func managedResourceOwnershipAnnotations(spec resourcev1.ManagedResourceSpec) map[string]string {
	return map[string]string{"opsi.dev/project-id": spec.ProjectID, "opsi.dev/environment-id": spec.EnvironmentID, "opsi.dev/managed-resource-id": spec.ResourceID, "opsi.dev/managed-resource-type": string(spec.ResourceType)}
}

func (r ManagedResourceReconciler) get(ctx context.Context, kind, name, namespace string) (map[string]any, error) {
	args := []string{"get", kind, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "json", "--ignore-not-found")
	out, err := r.run(ctx, nil, args...)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return nil, err
	}
	var object map[string]any
	if json.Unmarshal(out, &object) != nil {
		return nil, errors.New("invalid Kubernetes object")
	}
	return object, nil
}

func (r ManagedResourceReconciler) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	runner := r.Runner
	if runner == nil {
		runner = deploy.ExecCommandRunner{}
	}
	return runner.Run(ctx, input, defaultString(r.KubectlPath, "kubectl"), args...)
}

func managedPodEvidence(pods map[string]any, spec resourcev1.ManagedResourceSpec) (string, int32) {
	var ready int32
	image := ""
	items, _ := pods["items"].([]any)
	for _, raw := range items {
		pod, _ := raw.(map[string]any)
		statuses, _ := nested(pod, "status", "containerStatuses").([]any)
		for _, rawStatus := range statuses {
			status, _ := rawStatus.(map[string]any)
			if status["name"] != "nats" {
				continue
			}
			id, _ := status["imageID"].(string)
			if imageMatches(id, spec.Image) {
				image = spec.Image
				if value, _ := status["ready"].(bool); value {
					ready++
				}
			} else if id != "" {
				image = id
			}
		}
	}
	return image, ready
}

func imageMatches(imageID, reference string) bool {
	parts := strings.Split(reference, "@")
	return len(parts) == 2 && (strings.HasSuffix(imageID, "@"+parts[1]) || strings.HasSuffix(imageID, "://"+parts[1]) || imageID == parts[1])
}
func serviceHasPort(service map[string]any, port int32) bool {
	ports, _ := nested(service, "spec", "ports").([]any)
	for _, raw := range ports {
		item, _ := raw.(map[string]any)
		if number(item["port"]) == int(port) {
			return true
		}
	}
	return false
}
func metadataString(object map[string]any, key string) string {
	value, _ := nested(object, "metadata", key).(string)
	return value
}
func nested(object map[string]any, keys ...string) any {
	var value any = object
	for _, key := range keys {
		current, _ := value.(map[string]any)
		value = current[key]
	}
	return value
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
func stringMap(value any) map[string]string {
	result := map[string]string{}
	switch values := value.(type) {
	case map[string]string:
		return values
	case map[string]any:
		for key, raw := range values {
			if text, ok := raw.(string); ok {
				result[key] = text
			}
		}
	}
	return result
}
func cloneObject(value map[string]any) map[string]any {
	data, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}
func preserveManagedMetadata(current, desired map[string]any) {
	currentMetadata, _ := current["metadata"].(map[string]any)
	desiredMetadata, _ := desired["metadata"].(map[string]any)
	desiredMetadata["uid"] = currentMetadata["uid"]
	desiredMetadata["resourceVersion"] = currentMetadata["resourceVersion"]
}
func preserveManagedServiceSpec(current, desired map[string]any) {
	currentSpec, _ := current["spec"].(map[string]any)
	desiredSpec, _ := desired["spec"].(map[string]any)
	for _, key := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "internalTrafficPolicy"} {
		if value, ok := currentSpec[key]; ok {
			desiredSpec[key] = value
		}
	}
}
func selectorString(values map[string]string) string {
	parts := make([]string, 0, len(values))
	for key, value := range values {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
func managedLabel(value string) string {
	value = strings.ToLower(value)
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, value)
	value = strings.Trim(value, "-_.")
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}
func failureCode(err error) string {
	var mismatch runtimeMismatchError
	if errors.As(err, &mismatch) {
		return resourcev1.FailureRuntimeMismatch
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "image") {
		return "MANAGED_RESOURCE_IMAGE_UNAVAILABLE"
	}
	if strings.Contains(message, "readiness") {
		return "MANAGED_RESOURCE_READINESS_FAILED"
	}
	return "MANAGED_RESOURCE_APPLY_FAILED"
}

type runtimeMismatchError struct{}

func (runtimeMismatchError) Error() string {
	return "managed resource runtime image does not match compiled intent"
}
