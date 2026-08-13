package svcatalog

import (
	"context"
	"encoding/base64"
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
	evidence, err := r.apply(ctx, lease.Spec, lease.Credential)
	if err != nil {
		result.Status, result.FailureCode, result.FailureMessageRedacted = "failed", failureCode(err), err.Error()
		return result
	}
	result.Status, result.Evidence = "ready", evidence
	return result
}

func (r ManagedResourceReconciler) apply(ctx context.Context, spec resourcev1.ManagedResourceSpec, credential *resourcev1.ManagedResourceCredential) (*resourcev1.ManagedResourceEvidence, error) {
	if spec.ResourceType == resourcev1.TypeRedis && (credential == nil || credential.Validate() != nil || credential.CredentialID != spec.CredentialID) {
		return nil, errors.New("managed resource credential is unavailable")
	}
	if err := r.ensureNamespace(ctx, spec); err != nil {
		return nil, err
	}
	for _, object := range managedResourceObjects(spec, credential) {
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
			if manifest["kind"] == "Secret" {
				return nil, secretApplyError{}
			}
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
	for _, kind := range []string{"deployment", "service", "secret"} {
		if kind == "secret" && spec.ResourceType != resourcev1.TypeRedis {
			continue
		}
		name := spec.Connection.ServiceName
		if kind == "secret" {
			name = managedResourceSecretName(spec)
		}
		current, err := r.get(ctx, kind, name, managedResourceNamespace(spec))
		if err != nil {
			return nil, err
		}
		if current == nil {
			continue
		}
		if !exactManagedResourceOwnership(current, spec) {
			return nil, errors.New("refusing to delete Kubernetes object with different Opsi managed-resource ownership")
		}
		if _, err := r.run(ctx, nil, "delete", kind, name, "-n", managedResourceNamespace(spec), "--wait=true", "--timeout=2m"); err != nil {
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
		if err != nil {
			var auth authError
			var secret secretApplyError
			if errors.As(err, &auth) || errors.As(err, &secret) {
				return evidence, err
			}
		}
		if err == nil && evidence.WorkloadReady && evidence.PodReady && evidence.ServiceReady && (spec.ResourceType != resourcev1.TypeRedis || evidence.SecretReady && evidence.AuthReady) && evidence.Image == spec.Image {
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
	secretReady, authReady := spec.ResourceType != resourcev1.TypeRedis, spec.ResourceType != resourcev1.TypeRedis
	if spec.ResourceType == resourcev1.TypeRedis {
		secret, secretErr := r.get(ctx, "secret", managedResourceSecretName(spec), namespace)
		if secretErr != nil || secret == nil || !exactManagedResourceOwnership(secret, spec) {
			if secretErr != nil {
				return &resourcev1.ManagedResourceEvidence{}, secretErr
			}
			return &resourcev1.ManagedResourceEvidence{}, secretApplyError{}
		}
		secretReady = true
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
	image, imageID, podReady := managedPodEvidence(pods, spec)
	workloadReady := available >= spec.Replicas && number(nested(deployment, "status", "observedGeneration")) >= number(nested(deployment, "metadata", "generation"))
	serviceReady := nested(service, "spec", "clusterIP") != nil && serviceHasPort(service, spec.Ports[0].Port)
	if spec.ResourceType == resourcev1.TypeRedis && workloadReady && podReady >= spec.Replicas && serviceReady {
		out, authErr := r.run(ctx, nil, "exec", "deployment/"+spec.Connection.ServiceName, "-n", namespace, "-c", "redis", "--", "sh", "-ec", `u=$(cat /run/opsi-valkey/username); export REDISCLI_AUTH=$(cat /run/opsi-valkey/password); valkey-cli --user "$u" -h 127.0.0.1 PING`)
		if authErr != nil || !strings.Contains(string(out), "PONG") {
			return &resourcev1.ManagedResourceEvidence{ObservedSpecHash: spec.SpecHash, WorkloadReady: workloadReady, PodReady: podReady >= spec.Replicas, ServiceReady: serviceReady, SecretReady: secretReady, Image: image, ImageID: imageID, AvailableReplicas: available, ObservedAt: time.Now().UTC()}, authError{}
		}
		authReady = true
	}
	return &resourcev1.ManagedResourceEvidence{ObservedSpecHash: spec.SpecHash, WorkloadReady: workloadReady, PodReady: podReady >= spec.Replicas, ServiceReady: serviceReady, SecretReady: secretReady, AuthReady: authReady, Image: image, ImageID: imageID, AvailableReplicas: available, ObservedAt: time.Now().UTC()}, nil
}

func managedResourceObjects(spec resourcev1.ManagedResourceSpec, credential *resourcev1.ManagedResourceCredential) []map[string]any {
	namespace := managedResourceNamespace(spec)
	labels := managedResourceLabels(spec)
	selector := managedResourceOwnershipLabels(spec)
	annotations := managedResourceAnnotations(spec)
	containerName := string(spec.ResourceType)
	container := map[string]any{
		"name": containerName, "image": spec.Image, "imagePullPolicy": "IfNotPresent",
		"ports":          []any{map[string]any{"name": spec.Ports[0].Name, "containerPort": spec.Ports[0].Port, "protocol": "TCP"}},
		"resources":      map[string]any{"requests": map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)}, "limits": map[string]any{"cpu": fmt.Sprintf("%dm", spec.CPUMillicores), "memory": strconv.FormatInt(spec.MemoryBytes, 10)}},
		"readinessProbe": map[string]any{"tcpSocket": map[string]any{"port": spec.Ports[0].Name}, "initialDelaySeconds": 1, "periodSeconds": 2, "timeoutSeconds": 1, "failureThreshold": 10},
	}
	if spec.ResourceType == resourcev1.TypeNATS {
		container["args"] = []any{"--port", strconv.Itoa(int(spec.Ports[0].Port))}
	} else {
		container["command"] = []any{"valkey-server", "/run/opsi-valkey/valkey.conf"}
		container["volumeMounts"] = []any{map[string]any{"name": "acl", "mountPath": "/run/opsi-valkey", "readOnly": true}}
	}
	podSpec := map[string]any{"containers": []any{container}}
	objects := []map[string]any{}
	if spec.ResourceType == resourcev1.TypeRedis {
		if credential == nil || credential.Validate() != nil || credential.CredentialID != spec.CredentialID {
			return nil
		}
		config := "bind 0.0.0.0\nport 6379\nprotected-mode yes\nuser default off\nuser " + credential.Username + " on >" + credential.Password + " ~* &* +@all\n"
		secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": map[string]any{"name": managedResourceSecretName(spec), "namespace": namespace, "labels": labels, "annotations": annotations}, "data": map[string]any{"username": base64.StdEncoding.EncodeToString([]byte(credential.Username)), "password": base64.StdEncoding.EncodeToString([]byte(credential.Password)), "valkey.conf": base64.StdEncoding.EncodeToString([]byte(config))}}
		objects = append(objects, secret)
		podSpec["volumes"] = []any{map[string]any{"name": "acl", "secret": map[string]any{"secretName": managedResourceSecretName(spec)}}}
	}
	deployment := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": spec.Connection.ServiceName, "namespace": namespace, "labels": labels, "annotations": annotations}, "spec": map[string]any{"replicas": spec.Replicas, "selector": map[string]any{"matchLabels": selector}, "template": map[string]any{"metadata": map[string]any{"labels": labels, "annotations": annotations}, "spec": podSpec}}}
	service := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": spec.Connection.ServiceName, "namespace": namespace, "labels": labels, "annotations": annotations}, "spec": map[string]any{"type": "ClusterIP", "selector": selector, "ports": []any{map[string]any{"name": spec.Ports[0].Name, "port": spec.Ports[0].Port, "targetPort": spec.Ports[0].Name, "protocol": "TCP"}}}}
	return append(objects, deployment, service)
}

func managedResourceSecretName(spec resourcev1.ManagedResourceSpec) string {
	return deploymentv1.StableDNSName(spec.Connection.ServiceName, "server-acl")
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

func managedPodEvidence(pods map[string]any, spec resourcev1.ManagedResourceSpec) (string, string, int32) {
	var ready int32
	image := ""
	imageID := ""
	items, _ := pods["items"].([]any)
	for _, raw := range items {
		pod, _ := raw.(map[string]any)
		statuses, _ := nested(pod, "status", "containerStatuses").([]any)
		for _, rawStatus := range statuses {
			status, _ := rawStatus.(map[string]any)
			if status["name"] != string(spec.ResourceType) {
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
			if id != "" {
				imageID = id
			}
		}
	}
	return image, imageID, ready
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
	var secret secretApplyError
	if errors.As(err, &secret) {
		return resourcev1.FailureSecretApplyFailed
	}
	var auth authError
	if errors.As(err, &auth) {
		return resourcev1.FailureAuthFailed
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "credential") {
		return resourcev1.FailureCredentialUnavailable
	}
	if strings.Contains(message, "image") {
		return "MANAGED_RESOURCE_IMAGE_UNAVAILABLE"
	}
	if strings.Contains(message, "readiness") {
		return "MANAGED_RESOURCE_READINESS_FAILED"
	}
	return "MANAGED_RESOURCE_APPLY_FAILED"
}

type runtimeMismatchError struct{}

type secretApplyError struct{}

func (secretApplyError) Error() string { return "managed resource secret apply failed" }

type authError struct{}

func (authError) Error() string { return "managed resource authentication check failed" }

func (runtimeMismatchError) Error() string {
	return "managed resource runtime image does not match compiled intent"
}
