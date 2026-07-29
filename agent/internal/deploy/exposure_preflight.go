package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

const (
	ExposureCreateEligible       = "create_eligible"
	ExposureUnchanged            = "unchanged"
	ExposureUpdateEligible       = "update_eligible"
	DefaultKubernetesReadTimeout = 10 * time.Second
	DefaultKubernetesOutputBytes = 256 * 1024
	MaxIngressInventoryItems     = 256
)

type ExposureDifference struct {
	Field       string
	CurrentHash string
	DesiredHash string
}

type ExposurePreflightResult struct {
	State    string
	Rendered RenderedExposure
	Diff     []ExposureDifference
}

// PreflightExposure is read-only. It uses the same CommandRunner/kubectl
// boundary as the R5-010 production adapter and never applies or mutates state.
func (a ProductionAdapter) PreflightExposure(ctx context.Context, command deploymentv1.AgentCommand, spec exposurev1.ExposureSpec, resolver TLSSecretResolver) (ExposurePreflightResult, error) {
	if a.Runner == nil {
		a.Runner = ExecCommandRunner{}
	}
	if a.KubectlPath == "" {
		a.KubectlPath = "kubectl"
	}
	canonical, err := spec.Canonicalize()
	if err != nil {
		return ExposurePreflightResult{}, err
	}
	rendered, err := renderExposure(ctx, command, canonical, resolver)
	if err != nil {
		return ExposurePreflightResult{}, err
	}
	if err := a.preflightBackendService(ctx, command, canonical, rendered); err != nil {
		return ExposurePreflightResult{}, err
	}
	current, exists, err := a.getOptionalKubernetesObject(ctx, "ingress", rendered.IngressName, rendered.Namespace)
	if err != nil {
		return ExposurePreflightResult{}, err
	}
	if exists {
		if !ownedExposureIdentity(current, canonical, rendered.IngressName) {
			return ExposurePreflightResult{}, exposureError(CodeIngressForeign, canonical, "choose another service identity or remove the foreign collision outside Opsi")
		}
		if hasUnsupportedIngressAnnotations(current) {
			return ExposurePreflightResult{}, exposureError(CodeIngressUnsupported, canonical, "remove unsupported annotations before Opsi manages this exposure")
		}
	}
	all, err := a.listIngresses(ctx)
	if err != nil {
		return ExposurePreflightResult{}, err
	}
	if err := preflightRouteConflicts(all, canonical, rendered); err != nil {
		return ExposurePreflightResult{}, err
	}
	result := ExposurePreflightResult{State: ExposureCreateEligible, Rendered: rendered}
	if !exists {
		return result, nil
	}
	result.Diff, err = ingressDiff(current, rendered.Ingress)
	if err != nil {
		return ExposurePreflightResult{}, &ExposureError{Code: CodeKubernetesInvalidResponse, SafeAction: "inspect the Kubernetes API response locally"}
	}
	if len(result.Diff) == 0 {
		result.State = ExposureUnchanged
	} else {
		result.State = ExposureUpdateEligible
	}
	return result, nil
}

func (a ProductionAdapter) preflightBackendService(ctx context.Context, command deploymentv1.AgentCommand, spec exposurev1.ExposureSpec, rendered RenderedExposure) error {
	current, exists, err := a.getOptionalKubernetesObject(ctx, "service", rendered.BackendServiceName, rendered.Namespace)
	if err != nil {
		return err
	}
	if !exists {
		return exposureError(CodeBackendServiceMissing, spec, "deploy the authoritative R5-010 workload before exposing it")
	}
	_, expected, _, err := renderProductionResources(command)
	if err != nil {
		return err
	}
	if !ownedWorkloadObject(current, expected.Service) {
		return exposureError(CodeBackendServiceForeign, spec, "restore the Opsi-owned ClusterIP Service before exposing it")
	}
	if !serviceObjectHasExactPort(current, spec.ServicePort) {
		return exposureError(CodeBackendServicePort, spec, "redeploy the authoritative workload with the requested service port")
	}
	currentHash := serviceFunctionalHash(current)
	expectedHash := serviceFunctionalHash(expected.Service)
	if currentHash != expectedHash {
		return exposureError(CodeBackendServiceMismatch, spec, "reconcile the authoritative R5-010 Service before exposing it")
	}
	return nil
}

func serviceFunctionalHash(service map[string]any) string {
	spec, _ := service["spec"].(map[string]any)
	functional := map[string]any{"type": spec["type"], "selector": spec["selector"], "ports": spec["ports"]}
	return hashValue(functional)
}

func (a ProductionAdapter) getOptionalKubernetesObject(ctx context.Context, kind, name, namespace string) (map[string]any, bool, error) {
	readCtx, cancel := context.WithTimeout(ctx, a.kubernetesReadTimeout())
	defer cancel()
	out, err := a.Runner.Run(readCtx, nil, a.KubectlPath, "get", kind, name, "-n", namespace, "-o", "json", "--ignore-not-found")
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return nil, false, &ExposureError{Code: CodeKubernetesTimeout, SafeAction: "retry the read-only Kubernetes preflight"}
		}
		return nil, false, &ExposureError{Code: CodeKubernetesRead, SafeAction: "retry the read-only Kubernetes preflight"}
	}
	if len(out) > a.kubernetesOutputLimit() {
		return nil, false, &ExposureError{Code: CodeKubernetesResponseTooLarge, SafeAction: "reduce the Kubernetes response and retry"}
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, false, nil
	}
	var object map[string]any
	if err := decodeSingleJSON(out, &object); err != nil || len(object) == 0 {
		return nil, false, &ExposureError{Code: CodeKubernetesInvalidResponse, SafeAction: "inspect the Kubernetes API response locally"}
	}
	return object, true, nil
}

func (a ProductionAdapter) listIngresses(ctx context.Context) ([]map[string]any, error) {
	readCtx, cancel := context.WithTimeout(ctx, a.kubernetesReadTimeout())
	defer cancel()
	out, err := a.Runner.Run(readCtx, nil, a.KubectlPath, "get", "ingress", "--all-namespaces", "-o", "json")
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return nil, &ExposureError{Code: CodeKubernetesTimeout, SafeAction: "retry the read-only Kubernetes preflight"}
		}
		return nil, &ExposureError{Code: CodeKubernetesRead, SafeAction: "retry the read-only Kubernetes preflight"}
	}
	if len(out) > a.kubernetesOutputLimit() {
		return nil, &ExposureError{Code: CodeKubernetesResponseTooLarge, SafeAction: "reduce the Kubernetes response and retry"}
	}
	var list struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Metadata   json.RawMessage  `json:"metadata,omitempty"`
		Items      []map[string]any `json:"items"`
	}
	if err := decodeSingleJSON(out, &list); err != nil || list.Items == nil {
		return nil, &ExposureError{Code: CodeKubernetesInvalidResponse, SafeAction: "inspect the Kubernetes API response locally"}
	}
	if len(list.Items) > MaxIngressInventoryItems {
		return nil, &ExposureError{Code: CodeKubernetesInventoryOverflow, SafeAction: "narrow the Kubernetes inventory and retry"}
	}
	sort.Slice(list.Items, func(i, j int) bool {
		left, _ := list.Items[i]["metadata"].(map[string]any)
		right, _ := list.Items[j]["metadata"].(map[string]any)
		leftKey, _ := left["namespace"].(string)
		leftName, _ := left["name"].(string)
		rightKey, _ := right["namespace"].(string)
		rightName, _ := right["name"].(string)
		return leftKey+"\x00"+leftName < rightKey+"\x00"+rightName
	})
	return list.Items, nil
}

func (a ProductionAdapter) kubernetesReadTimeout() time.Duration {
	if a.ReadTimeout > 0 {
		return a.ReadTimeout
	}
	return DefaultKubernetesReadTimeout
}

func (a ProductionAdapter) kubernetesOutputLimit() int {
	if a.MaxOutputBytes > 0 {
		return a.MaxOutputBytes
	}
	return DefaultKubernetesOutputBytes
}

func decodeSingleJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Kubernetes response contains trailing JSON")
	}
	return nil
}

func preflightRouteConflicts(items []map[string]any, desired exposurev1.ExposureSpec, rendered RenderedExposure) error {
	for _, item := range items {
		metadata, _ := item["metadata"].(map[string]any)
		if metadata["namespace"] == rendered.Namespace && metadata["name"] == rendered.IngressName {
			continue
		}
		for _, route := range ingressRoutes(item) {
			if !ingressHostConflicts(route.hostname, desired.Hostname) {
				continue
			}
			path, err := exposurev1.NormalizePath(route.path)
			if err != nil {
				path = "/"
			}
			if !exposurev1.PathsConflict(path, desired.Path) {
				continue
			}
			code := CodeForeignRouteConflict
			labels := stringMap(metadata["labels"])
			if labels["app.kubernetes.io/managed-by"] == "opsi" {
				code = CodeOpsiRouteConflict
			}
			return exposureError(code, desired, "choose a non-conflicting hostname/path and retry")
		}
	}
	return nil
}

func ingressHostConflicts(current, desired string) bool {
	current = strings.ToLower(current)
	if current == "" || current == desired {
		return true
	}
	if !strings.HasPrefix(current, "*.") {
		return false
	}
	suffix := strings.TrimPrefix(current, "*")
	if !strings.HasSuffix(desired, suffix) {
		return false
	}
	prefix := strings.TrimSuffix(desired, suffix)
	return prefix != "" && !strings.Contains(prefix, ".")
}

type ingressRoute struct {
	hostname string
	path     string
}

func ingressRoutes(object map[string]any) []ingressRoute {
	spec, _ := object["spec"].(map[string]any)
	var routes []ingressRoute
	if _, exists := spec["defaultBackend"]; exists {
		routes = append(routes, ingressRoute{path: "/"})
	}
	rules, _ := spec["rules"].([]any)
	for _, rawRule := range rules {
		rule, _ := rawRule.(map[string]any)
		host, _ := rule["host"].(string)
		http, _ := rule["http"].(map[string]any)
		paths, _ := http["paths"].([]any)
		for _, rawPath := range paths {
			pathObject, _ := rawPath.(map[string]any)
			pathValue, _ := pathObject["path"].(string)
			if pathValue == "" {
				pathValue = "/"
			}
			routes = append(routes, ingressRoute{hostname: host, path: pathValue})
		}
	}
	return routes
}

func ownedExposureIdentity(object map[string]any, spec exposurev1.ExposureSpec, name string) bool {
	metadata, _ := object["metadata"].(map[string]any)
	labels := stringMap(metadata["labels"])
	annotations := stringMap(metadata["annotations"])
	return labels["app.kubernetes.io/managed-by"] == "opsi" &&
		labels["opsi.dev/project"] == safeLabel(spec.ProjectID) &&
		labels["opsi.dev/environment"] == safeLabel(spec.EnvironmentID) &&
		labels["opsi.dev/runtime"] == safeLabel(spec.RuntimeID) &&
		labels["opsi.dev/service"] == safeLabel(spec.ServiceKey) &&
		labels["opsi.dev/exposure"] == name &&
		annotations["opsi.dev/identity-hash"] == exposureIdentityHash(spec)
}

func ownedWorkloadObject(current, expected map[string]any) bool {
	currentMetadata, _ := current["metadata"].(map[string]any)
	expectedMetadata, _ := expected["metadata"].(map[string]any)
	currentLabels := stringMap(currentMetadata["labels"])
	expectedLabels := stringMap(expectedMetadata["labels"])
	for _, key := range []string{"app.kubernetes.io/managed-by", "opsi.dev/project", "opsi.dev/environment", "opsi.dev/runtime", "opsi.dev/service", "opsi.dev/workload"} {
		if currentLabels[key] != expectedLabels[key] {
			return false
		}
	}
	return true
}

func hasUnsupportedIngressAnnotations(object map[string]any) bool {
	metadata, _ := object["metadata"].(map[string]any)
	for key := range stringMap(metadata["annotations"]) {
		switch key {
		case "opsi.dev/spec-hash", "opsi.dev/workload-spec-hash", "opsi.dev/identity-hash":
		default:
			return true
		}
	}
	return false
}

func ingressDiff(current, desired map[string]any) ([]ExposureDifference, error) {
	currentMetadata, _ := current["metadata"].(map[string]any)
	desiredMetadata, _ := desired["metadata"].(map[string]any)
	sections := []struct {
		field   string
		current any
		desired any
	}{
		{field: "metadata.labels", current: stringMap(currentMetadata["labels"]), desired: stringMap(desiredMetadata["labels"])},
		{field: "metadata.annotations", current: stringMap(currentMetadata["annotations"]), desired: stringMap(desiredMetadata["annotations"])},
		{field: "spec", current: current["spec"], desired: desired["spec"]},
	}
	var diff []ExposureDifference
	for _, section := range sections {
		currentHash, err := logicalHash(section.current)
		if err != nil {
			return nil, err
		}
		desiredHash, err := logicalHash(section.desired)
		if err != nil {
			return nil, err
		}
		if currentHash != desiredHash {
			diff = append(diff, ExposureDifference{Field: section.field, CurrentHash: currentHash, DesiredHash: desiredHash})
		}
	}
	sort.Slice(diff, func(i, j int) bool { return diff[i].Field < diff[j].Field })
	return diff, nil
}

func logicalHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func stringMap(value any) map[string]string {
	out := map[string]string{}
	switch values := value.(type) {
	case map[string]string:
		for key, item := range values {
			out[key] = item
		}
	case map[string]any:
		for key, item := range values {
			if text, ok := item.(string); ok {
				out[key] = text
			}
		}
	}
	return out
}
