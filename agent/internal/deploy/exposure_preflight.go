package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

const (
	ExposureCreateEligible = "create_eligible"
	ExposureUnchanged      = "unchanged"
	ExposureUpdateEligible = "update_eligible"
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
	currentHash, err := objectSectionHash(current, "spec")
	if err != nil {
		return &ExposureError{Code: CodeKubernetesInvalidResponse, SafeAction: "inspect the Kubernetes Service response locally"}
	}
	expectedHash, err := objectSectionHash(expected.Service, "spec")
	if err != nil {
		return err
	}
	if currentHash != expectedHash {
		return exposureError(CodeBackendServiceMismatch, spec, "reconcile the authoritative R5-010 Service before exposing it")
	}
	return nil
}

func (a ProductionAdapter) getOptionalKubernetesObject(ctx context.Context, kind, name, namespace string) (map[string]any, bool, error) {
	out, err := a.Runner.Run(ctx, nil, a.KubectlPath, "get", kind, name, "-n", namespace, "-o", "json")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, false, nil
		}
		return nil, false, &ExposureError{Code: CodeKubernetesRead, SafeAction: "retry the read-only Kubernetes preflight"}
	}
	var object map[string]any
	if err := json.Unmarshal(out, &object); err != nil {
		return nil, false, &ExposureError{Code: CodeKubernetesInvalidResponse, SafeAction: "inspect the Kubernetes API response locally"}
	}
	return object, true, nil
}

func (a ProductionAdapter) listIngresses(ctx context.Context) ([]map[string]any, error) {
	out, err := a.Runner.Run(ctx, nil, a.KubectlPath, "get", "ingress", "--all-namespaces", "-o", "json")
	if err != nil {
		return nil, &ExposureError{Code: CodeKubernetesRead, SafeAction: "retry the read-only Kubernetes preflight"}
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, &ExposureError{Code: CodeKubernetesInvalidResponse, SafeAction: "inspect the Kubernetes API response locally"}
	}
	return list.Items, nil
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

func objectSectionHash(object map[string]any, section string) (string, error) {
	return logicalHash(object[section])
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
