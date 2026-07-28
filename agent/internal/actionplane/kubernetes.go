package actionplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

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
			return actionv1.CurrentState{}, errors.New("incident factual-state adapter is unavailable")
		}
		incident, err := k.IncidentState(ctx, target, parameters.IncidentResolve.IncidentID)
		if err != nil {
			return actionv1.CurrentState{}, err
		}
		state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: target.ProjectID, Target: target, Incident: &incident}
		state.StateHash, err = actionv1.StateHash(state)
		return state, err
	}
	identity, err := k.identity(ctx, target)
	if err != nil {
		return actionv1.CurrentState{}, err
	}
	deployment, err := k.get(ctx, "deployment", identity.DeploymentName, identity.Namespace, "")
	if err != nil {
		return actionv1.CurrentState{}, err
	}
	pods, err := k.get(ctx, "pods", "", identity.Namespace, selector(identity.Selector))
	if err != nil {
		return actionv1.CurrentState{}, err
	}
	metadata, _ := deployment["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	if !owned(labels, target) {
		return actionv1.CurrentState{}, errors.New("Kubernetes workload is not Opsi-owned")
	}
	spec, _ := deployment["spec"].(map[string]any)
	status, _ := deployment["status"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	templateMetadata, _ := template["metadata"].(map[string]any)
	templateAnnotations, _ := templateMetadata["annotations"].(map[string]any)
	state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: target.ProjectID, Target: target, Workload: &actionv1.WorkloadState{UID: stringValue(metadata["uid"]), ResourceVersion: stringValue(metadata["resourceVersion"]), Generation: int64Value(metadata["generation"]), ObservedGeneration: int64Value(status["observedGeneration"]), DesiredReplicas: int32Value(spec["replicas"]), ObservedReplicas: int32Value(status["availableReplicas"]), ReadyReplicas: readyPods(pods), RestartToken: stringValue(templateAnnotations["opsi.dev/restarted-at"])}}
	if identity.Snapshot != nil && identity.Snapshot.Runtime.HasExternalExposure() {
		ingressName := identity.IngressName
		if ingressName == "" {
			ingressName = stableIngressName(target)
		}
		gateway, gatewayErr := k.get(ctx, "ingress", ingressName, identity.Namespace, "")
		if gatewayErr == nil {
			gatewayMetadata, _ := gateway["metadata"].(map[string]any)
			annotations, _ := gatewayMetadata["annotations"].(map[string]any)
			gatewayLabels, _ := gatewayMetadata["labels"].(map[string]any)
			state.Gateway = &actionv1.GatewayState{UID: stringValue(gatewayMetadata["uid"]), ResourceVersion: stringValue(gatewayMetadata["resourceVersion"]), SpecHash: stringValue(annotations["opsi.dev/spec-hash"]), BackendServiceID: identity.ServiceName, Owned: owned(gatewayLabels, target)}
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
	if k.Projection != nil {
		return k.Projection.WorkloadIdentity(ctx, target)
	}
	return deploy.ActionWorkloadIdentity{Namespace: stableNamespace(target), DeploymentName: stableDeployment(target), ServiceName: stableDeployment(target), IngressName: stableIngressName(target), Selector: deploy.ActionOwnershipLabels(target)}, nil
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
	if len(output) > deploy.MaxCommandOutputBytes {
		return nil, errors.New("Kubernetes output exceeded bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("Kubernetes returned invalid JSON")
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		return nil, errors.New("Kubernetes returned multiple JSON values")
	}
	return value, nil
}

func (k KubernetesRuntime) path() string {
	if k.KubectlPath != "" {
		return k.KubectlPath
	}
	return "kubectl"
}
func stableNamespace(target actionv1.TargetIdentity) string {
	return "opsi-" + safePart(target.ProjectID) + "-" + safePart(target.EnvironmentID)
}
func stableDeployment(target actionv1.TargetIdentity) string {
	return "opsi-" + safePart(target.ServiceID) + "-" + safePart(target.RuntimeID)
}
func stableIngressName(target actionv1.TargetIdentity) string {
	return "opsi-ingress-" + safePart(target.ServiceID) + "-" + safePart(target.RuntimeID)
}
func safePart(value string) string {
	value = strings.ToLower(value)
	value = strings.NewReplacer("_", "-", "/", "-", " ", "-").Replace(value)
	if len(value) > 40 {
		value = value[:40]
	}
	return strings.Trim(value, "-")
}
func selector(values map[string]string) string {
	return "app.kubernetes.io/managed-by=" + values["app.kubernetes.io/managed-by"] + ",opsi.dev/project=" + values["opsi.dev/project"]
}
func owned(labels map[string]any, target actionv1.TargetIdentity) bool {
	return stringValue(labels["app.kubernetes.io/managed-by"]) == "opsi" && stringValue(labels["opsi.dev/project"]) == safePart(target.ProjectID)
}
func readyPods(pods map[string]any) int32 {
	items, _ := pods["items"].([]any)
	var ready int32
	for _, item := range items {
		raw, _ := item.(map[string]any)
		status, _ := raw["status"].(map[string]any)
		statuses, _ := status["containerStatuses"].([]any)
		for _, value := range statuses {
			container, _ := value.(map[string]any)
			if stringValue(container["name"]) == "opsi-app" && boolValue(container["ready"]) {
				ready++
			}
		}
	}
	return ready
}
func stringValue(value any) string { result, _ := value.(string); return result }
func int64Value(value any) int64 {
	switch number := value.(type) {
	case json.Number:
		v, _ := number.Int64()
		return v
	case float64:
		return int64(number)
	case int64:
		return number
	}
	return 0
}
func int32Value(value any) int32 { return int32(int64Value(value)) }
func boolValue(value any) bool   { value, _ = value.(bool); result, _ := value.(bool); return result }
