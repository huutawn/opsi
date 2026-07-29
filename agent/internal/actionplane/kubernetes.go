package actionplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

var ErrFactualStateUnavailable = errors.New("factual state is unavailable")

var errOwnershipMismatch = errors.New("Kubernetes ownership or backend identity mismatch")

type KubernetesRuntime struct {
	Runner        deploy.CommandRunner
	KubectlPath   string
	Timeout       time.Duration
	Projection    *deploy.ActionProjection
	Incident      func(context.Context, actionv1.TargetIdentity, string) error
	IncidentState func(context.Context, actionv1.TargetIdentity, string) (actionv1.IncidentState, error)
	PollInterval  time.Duration
}

func (k KubernetesRuntime) CurrentState(ctx context.Context, target actionv1.TargetIdentity, kind actionv1.ActionKind, parameters actionv1.ActionParameters) (actionv1.CurrentState, error) {
	if kind == actionv1.ActionIncidentResolve {
		if k.IncidentState == nil || parameters.IncidentResolve == nil {
			return actionv1.CurrentState{}, fmt.Errorf("%w: incident factual-state adapter is unavailable", ErrFactualStateUnavailable)
		}
		incident, err := k.IncidentState(ctx, target, parameters.IncidentResolve.IncidentID)
		if err != nil {
			return actionv1.CurrentState{}, fmt.Errorf("%w: %w", ErrFactualStateUnavailable, err)
		}
		state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: target.ProjectID, Target: target, Incident: &incident}
		state.StateHash, err = actionv1.StateHash(state)
		return state, err
	}
	identity, err := k.identity(ctx, target)
	if err != nil {
		return actionv1.CurrentState{}, fmt.Errorf("%w: %v", ErrFactualStateUnavailable, err)
	}
	deployment, err := k.get(ctx, "deployment", identity.DeploymentName, identity.Namespace, "")
	if err != nil {
		return actionv1.CurrentState{}, fmt.Errorf("%w: %v", ErrFactualStateUnavailable, err)
	}
	pods, err := k.get(ctx, "pods", "", identity.Namespace, selector(identity.Selector))
	if err != nil {
		return actionv1.CurrentState{}, fmt.Errorf("%w: %v", ErrFactualStateUnavailable, err)
	}
	workload, err := workloadState(deployment, pods, identity, target)
	if err != nil {
		if errors.Is(err, errOwnershipMismatch) {
			return actionv1.CurrentState{}, err
		}
		return actionv1.CurrentState{}, fmt.Errorf("%w: %v", ErrFactualStateUnavailable, err)
	}
	state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: target.ProjectID, Target: target, Workload: workload}
	if identity.Snapshot != nil && identity.Snapshot.Runtime.HasExternalExposure() {
		if identity.IngressName == "" {
			return actionv1.CurrentState{}, fmt.Errorf("%w: authoritative ingress identity is missing", ErrFactualStateUnavailable)
		}
		gateway, gatewayErr := k.get(ctx, "ingress", identity.IngressName, identity.Namespace, "")
		if gatewayErr != nil {
			return actionv1.CurrentState{}, fmt.Errorf("%w: %v", ErrFactualStateUnavailable, gatewayErr)
		}
		state.Gateway, err = gatewayState(gateway, identity)
		if err != nil {
			if errors.Is(err, errOwnershipMismatch) {
				return actionv1.CurrentState{}, err
			}
			return actionv1.CurrentState{}, fmt.Errorf("%w: %v", ErrFactualStateUnavailable, err)
		}
	}
	state.StateHash, err = actionv1.StateHash(state)
	return state, err
}

func (k KubernetesRuntime) RestartWorkload(ctx context.Context, target actionv1.TargetIdentity) error {
	identity, err := k.identity(ctx, target)
	if err != nil {
		return err
	}
	patch := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"opsi.dev/restarted-at":%q}}}}}`, time.Now().UTC().Format(time.RFC3339Nano)))
	_, err = k.run(ctx, nil, "patch", "deployment", identity.DeploymentName, "-n", identity.Namespace, "--type=merge", "-p", string(patch))
	return err
}

func (k KubernetesRuntime) ScaleWorkload(ctx context.Context, target actionv1.TargetIdentity, replicas int32) error {
	if replicas < 0 || replicas > actionv1.MaxReplicas {
		return errors.New("replicas exceed ActionPlane bounds")
	}
	identity, err := k.identity(ctx, target)
	if err != nil {
		return err
	}
	_, err = k.run(ctx, nil, "scale", "deployment", identity.DeploymentName, "-n", identity.Namespace, fmt.Sprintf("--replicas=%d", replicas))
	return err
}

func (k KubernetesRuntime) GatewayReconcile(ctx context.Context, target actionv1.TargetIdentity) error {
	if k.Projection == nil {
		return errors.New("gateway projection is unavailable")
	}
	return k.Projection.ReconcileGateway(ctx, target)
}

func (k KubernetesRuntime) ResolveIncident(ctx context.Context, target actionv1.TargetIdentity, incidentID string) error {
	if k.Incident == nil {
		return errors.New("incident ActionPlane adapter is unavailable")
	}
	return k.Incident(ctx, target, incidentID)
}

func (k KubernetesRuntime) PostCheck(ctx context.Context, target actionv1.TargetIdentity, kind actionv1.ActionKind, parameters actionv1.ActionParameters, before actionv1.CurrentState) (actionv1.CurrentState, error) {
	timeout := k.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := k.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	var state actionv1.CurrentState
	var err error
	for {
		state, err = k.CurrentState(ctx, target, kind, parameters)
		if err == nil {
			valid := true
			switch kind {
			case actionv1.ActionScaleWorkload:
				valid = state.Workload != nil && state.Workload.DesiredReplicas == parameters.ScaleWorkload.Replicas && state.Workload.ObservedReplicas == parameters.ScaleWorkload.Replicas && state.Workload.ReadyReplicas == parameters.ScaleWorkload.Replicas && state.Workload.ObservedGeneration >= state.Workload.Generation
			case actionv1.ActionRestartWorkload:
				valid = state.Workload != nil && state.Workload.ReadyReplicas == state.Workload.DesiredReplicas && state.Workload.ObservedGeneration >= state.Workload.Generation && (before.Workload == nil || state.Workload.Generation > before.Workload.Generation || state.Workload.RestartToken != before.Workload.RestartToken)
			case actionv1.ActionGatewayReconcile:
				identity, identityErr := k.identity(ctx, target)
				valid = identityErr == nil && state.Gateway != nil && state.Gateway.Owned && state.Gateway.BackendServiceID == identity.ServiceName
			case actionv1.ActionIncidentResolve:
				valid = state.Incident != nil && parameters.IncidentResolve != nil && state.Incident.IncidentID == parameters.IncidentResolve.IncidentID && state.Incident.Status == "resolved"
			}
			if valid {
				return state, nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return state, err
			}
			switch kind {
			case actionv1.ActionScaleWorkload:
				return state, errors.New("scaled workload is not ready")
			case actionv1.ActionRestartWorkload:
				return state, errors.New("restart rollout was not observed")
			case actionv1.ActionGatewayReconcile:
				return state, errors.New("gateway ownership post-check failed")
			default:
				return state, errors.New("incident was not resolved")
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return state, ctx.Err()
		case <-timer.C:
		}
	}
}

func (k KubernetesRuntime) identity(ctx context.Context, target actionv1.TargetIdentity) (deploy.ActionWorkloadIdentity, error) {
	if k.Projection == nil {
		return deploy.ActionWorkloadIdentity{}, errors.New("authoritative ActionProjection is unavailable")
	}
	identity, err := k.Projection.WorkloadIdentity(ctx, target)
	if err != nil {
		return deploy.ActionWorkloadIdentity{}, err
	}
	for _, key := range []string{"app.kubernetes.io/managed-by", "opsi.dev/project", "opsi.dev/environment", "opsi.dev/service", "opsi.dev/runtime"} {
		if identity.Selector[key] == "" {
			return deploy.ActionWorkloadIdentity{}, errors.New("authoritative Kubernetes selector is incomplete")
		}
	}
	if identity.Namespace == "" || identity.DeploymentName == "" || identity.ServiceName == "" || identity.Snapshot == nil {
		return deploy.ActionWorkloadIdentity{}, errors.New("authoritative Kubernetes identity is incomplete")
	}
	return identity, nil
}

func (k KubernetesRuntime) run(parent context.Context, input []byte, args ...string) ([]byte, error) {
	if k.Runner == nil {
		return nil, errors.New("Kubernetes command runner is unavailable")
	}
	timeout := k.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return k.Runner.Run(ctx, input, k.path(), args...)
}

func (k KubernetesRuntime) get(ctx context.Context, kind, name, namespace, selectorValue string) (map[string]any, error) {
	args := []string{"get", kind}
	if name != "" {
		args = append(args, name)
	}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	if selectorValue != "" {
		args = append(args, "-l", selectorValue)
	}
	args = append(args, "-o", "json")
	output, err := k.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	return decodeKubernetesJSON(output)
}

func (k KubernetesRuntime) path() string {
	if k.KubectlPath != "" {
		return k.KubectlPath
	}
	return "kubectl"
}
func selector(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, ",")
}
func owned(labels map[string]any, expected map[string]string) bool {
	for key, value := range expected {
		if stringValue(labels[key]) != value {
			return false
		}
	}
	return true
}
func readyPods(pods map[string]any, expected map[string]string) (int32, error) {
	items, ok := pods["items"].([]any)
	if !ok {
		return 0, errors.New("Kubernetes Pod list is invalid")
	}
	var ready int32
	for _, item := range items {
		raw, _ := item.(map[string]any)
		metadata, _ := raw["metadata"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)
		if !owned(labels, expected) {
			continue
		}
		status, _ := raw["status"].(map[string]any)
		statuses, _ := status["containerStatuses"].([]any)
		for _, value := range statuses {
			container, _ := value.(map[string]any)
			if stringValue(container["name"]) == "opsi-app" && boolValue(container["ready"]) {
				ready++
			}
		}
	}
	return ready, nil
}
func stringValue(value any) string { result, _ := value.(string); return result }
func requiredString(value any, field string) (string, error) {
	result, ok := value.(string)
	if !ok || strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("Kubernetes %s is missing or invalid", field)
	}
	return result, nil
}
func numberValue(value any, field string, required bool) (int64, error) {
	if value == nil && !required {
		return 0, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("Kubernetes %s is missing or invalid", field)
	}
	result, err := number.Int64()
	if err != nil {
		return 0, fmt.Errorf("Kubernetes %s is malformed", field)
	}
	return result, nil
}
func boolValue(value any) bool { value, _ = value.(bool); result, _ := value.(bool); return result }

func decodeKubernetesJSON(output []byte) (map[string]any, error) {
	if len(output) > deploy.MaxCommandOutputBytes {
		return nil, errors.New("Kubernetes output exceeded bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("Kubernetes returned invalid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("Kubernetes returned trailing data")
	}
	return value, nil
}

func workloadState(deployment, pods map[string]any, identity deploy.ActionWorkloadIdentity, _ actionv1.TargetIdentity) (*actionv1.WorkloadState, error) {
	metadata, ok := deployment["metadata"].(map[string]any)
	if !ok {
		return nil, errors.New("Kubernetes Deployment metadata is invalid")
	}
	labels, _ := metadata["labels"].(map[string]any)
	if !owned(labels, identity.Selector) {
		return nil, errOwnershipMismatch
	}
	uid, err := requiredString(metadata["uid"], "Deployment metadata.uid")
	if err != nil {
		return nil, err
	}
	resourceVersion, err := requiredString(metadata["resourceVersion"], "Deployment metadata.resourceVersion")
	if err != nil {
		return nil, err
	}
	generation, err := numberValue(metadata["generation"], "Deployment metadata.generation", true)
	if err != nil {
		return nil, err
	}
	spec, ok := deployment["spec"].(map[string]any)
	if !ok {
		return nil, errors.New("Kubernetes Deployment spec is invalid")
	}
	desired, err := numberValue(spec["replicas"], "Deployment spec.replicas", true)
	if err != nil || desired < 0 || desired > int64(actionv1.MaxReplicas) {
		return nil, errors.New("Kubernetes Deployment replicas are invalid")
	}
	status, _ := deployment["status"].(map[string]any)
	observedGeneration, err := numberValue(status["observedGeneration"], "Deployment status.observedGeneration", false)
	if err != nil {
		return nil, err
	}
	available, err := numberValue(status["availableReplicas"], "Deployment status.availableReplicas", false)
	if err != nil || available < 0 || available > int64(actionv1.MaxReplicas) {
		return nil, errors.New("Kubernetes Deployment availableReplicas are invalid")
	}
	ready, err := readyPods(pods, identity.Selector)
	if err != nil {
		return nil, err
	}
	template, _ := spec["template"].(map[string]any)
	templateMetadata, _ := template["metadata"].(map[string]any)
	annotations, _ := templateMetadata["annotations"].(map[string]any)
	return &actionv1.WorkloadState{UID: uid, ResourceVersion: resourceVersion, Generation: generation, ObservedGeneration: observedGeneration, DesiredReplicas: int32(desired), ObservedReplicas: int32(available), ReadyReplicas: ready, RestartToken: stringValue(annotations["opsi.dev/restarted-at"])}, nil
}

func gatewayState(ingress map[string]any, identity deploy.ActionWorkloadIdentity) (*actionv1.GatewayState, error) {
	metadata, ok := ingress["metadata"].(map[string]any)
	if !ok {
		return nil, errors.New("Kubernetes Ingress metadata is invalid")
	}
	labels, _ := metadata["labels"].(map[string]any)
	if !owned(labels, identity.Selector) || !ingressBackendMatches(ingress, identity.ServiceName) {
		return nil, errOwnershipMismatch
	}
	uid, err := requiredString(metadata["uid"], "Ingress metadata.uid")
	if err != nil {
		return nil, err
	}
	resourceVersion, err := requiredString(metadata["resourceVersion"], "Ingress metadata.resourceVersion")
	if err != nil {
		return nil, err
	}
	generation, err := numberValue(metadata["generation"], "Ingress metadata.generation", true)
	if err != nil {
		return nil, err
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	return &actionv1.GatewayState{UID: uid, ResourceVersion: resourceVersion, Generation: generation, SpecHash: stringValue(annotations["opsi.dev/spec-hash"]), BackendServiceID: identity.ServiceName, Owned: true}, nil
}

func ingressBackendMatches(ingress map[string]any, serviceName string) bool {
	spec, _ := ingress["spec"].(map[string]any)
	if backend, ok := spec["defaultBackend"].(map[string]any); ok && !backendServiceMatches(backend, serviceName) {
		return false
	}
	rules, _ := spec["rules"].([]any)
	found := false
	for _, ruleValue := range rules {
		rule, _ := ruleValue.(map[string]any)
		httpValue, _ := rule["http"].(map[string]any)
		paths, _ := httpValue["paths"].([]any)
		for _, pathValue := range paths {
			path, _ := pathValue.(map[string]any)
			backend, _ := path["backend"].(map[string]any)
			if !backendServiceMatches(backend, serviceName) {
				return false
			}
			found = true
		}
	}
	return found
}

func backendServiceMatches(backend map[string]any, serviceName string) bool {
	service, _ := backend["service"].(map[string]any)
	return stringValue(service["name"]) == serviceName
}
