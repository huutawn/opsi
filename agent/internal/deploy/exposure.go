package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

const (
	ExposureFieldManager = "opsi-r5-011-exposure"
	TraefikIngressClass  = "traefik"
	KubernetesTLSSecret  = "kubernetes.io/tls"
)

const (
	CodeExposureIdentityMismatch    = "EXPOSURE_WORKLOAD_IDENTITY_MISMATCH"
	CodeBackendServiceMissing       = "EXPOSURE_BACKEND_SERVICE_MISSING"
	CodeBackendServiceForeign       = "EXPOSURE_BACKEND_SERVICE_FOREIGN"
	CodeBackendServicePort          = "EXPOSURE_BACKEND_SERVICE_PORT_MISMATCH"
	CodeBackendServiceMismatch      = "EXPOSURE_BACKEND_SERVICE_MISMATCH"
	CodeTLSSecretResolution         = "EXPOSURE_TLS_SECRET_RESOLUTION_FAILED"
	CodeTLSSecretMissing            = "EXPOSURE_TLS_SECRET_MISSING"
	CodeTLSSecretForeign            = "EXPOSURE_TLS_SECRET_FOREIGN"
	CodeTLSSecretNamespace          = "EXPOSURE_TLS_SECRET_WRONG_NAMESPACE"
	CodeTLSSecretType               = "EXPOSURE_TLS_SECRET_WRONG_TYPE"
	CodeIngressForeign              = "EXPOSURE_INGRESS_FOREIGN"
	CodeIngressUnsupported          = "EXPOSURE_INGRESS_UNSUPPORTED_METADATA"
	CodeOpsiRouteConflict           = "EXPOSURE_OPSI_ROUTE_CONFLICT"
	CodeForeignRouteConflict        = "EXPOSURE_FOREIGN_ROUTE_CONFLICT"
	CodeKubernetesRead              = "EXPOSURE_KUBERNETES_READ_FAILED"
	CodeKubernetesTimeout           = "EXPOSURE_KUBERNETES_READ_TIMEOUT"
	CodeKubernetesResponseTooLarge  = "EXPOSURE_KUBERNETES_RESPONSE_TOO_LARGE"
	CodeKubernetesInventoryOverflow = "EXPOSURE_KUBERNETES_INVENTORY_OVERFLOW"
	CodeKubernetesInvalidResponse   = "EXPOSURE_KUBERNETES_INVALID_RESPONSE"
)

var dnsSubdomainPattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9.]*[a-z0-9])?$`)

type ExposureError struct {
	Code       string
	Hostname   string
	Path       string
	SafeAction string
}

func (e *ExposureError) Error() string {
	message := e.Code
	if e.Hostname != "" {
		message += ": route " + e.Hostname + e.Path
	}
	if e.SafeAction != "" {
		message += "; " + e.SafeAction
	}
	return message
}

type VerifiedTLSSecret struct {
	Reference string
	Namespace string
	Name      string
	Type      string
	Exists    bool
	OpsiOwned bool
}

// TLSSecretResolver is the only boundary from an opaque Opsi reference to a
// namespace-local Kubernetes TLS Secret identity. It never returns key data.
type TLSSecretResolver interface {
	ResolveTLSSecret(context.Context, string, string) (VerifiedTLSSecret, error)
}

type RenderedExposure struct {
	Manifest           []byte
	Ingress            map[string]any
	Namespace          string
	IngressName        string
	BackendServiceName string
	ServicePort        int32
	SpecHash           string
	FieldManager       string
}

func renderExposure(ctx context.Context, command deploymentv1.AgentCommand, spec exposurev1.ExposureSpec, resolver TLSSecretResolver) (RenderedExposure, error) {
	canonical, err := spec.Canonicalize()
	if err != nil {
		return RenderedExposure{}, err
	}
	if err := validateExposureWorkloadIdentity(command, canonical); err != nil {
		return RenderedExposure{}, err
	}
	_, workload, namespace, err := renderProductionResources(command)
	if err != nil {
		return RenderedExposure{}, err
	}
	if !serviceObjectHasExactPort(workload.Service, canonical.ServicePort) {
		return RenderedExposure{}, exposureError(CodeBackendServicePort, canonical, "redeploy the authoritative workload with the requested service port")
	}
	ingressName := stableDNSName("opsi-ingress", canonical.ServiceKey, canonical.RuntimeID)
	labels := cloneStringMap(workload.Selector)
	labels["app.kubernetes.io/name"] = ingressName
	labels["opsi.dev/exposure"] = ingressName
	labels["opsi.dev/deployment-job"] = safeLabel(canonical.DeploymentJobID)
	annotations := map[string]string{
		"opsi.dev/spec-hash":          canonical.SpecHash,
		"opsi.dev/workload-spec-hash": command.SpecHash,
		"opsi.dev/identity-hash":      exposureIdentityHash(canonical),
	}
	specObject := map[string]any{
		"ingressClassName": TraefikIngressClass,
		"rules": []any{map[string]any{
			"host": canonical.Hostname,
			"http": map[string]any{"paths": []any{map[string]any{
				"path":     canonical.Path,
				"pathType": "Prefix",
				"backend": map[string]any{"service": map[string]any{
					"name": workload.ServiceName,
					"port": map[string]any{"number": canonical.ServicePort},
				}},
			}}},
		}},
	}
	if canonical.TLS.Mode == exposurev1.TLSSecretRef {
		secretName, err := resolveTLSSecret(ctx, resolver, canonical, namespace)
		if err != nil {
			return RenderedExposure{}, err
		}
		specObject["tls"] = []any{map[string]any{"hosts": []any{canonical.Hostname}, "secretName": secretName}}
	}
	ingress := map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata": map[string]any{
			"name":        ingressName,
			"namespace":   namespace,
			"labels":      labels,
			"annotations": annotations,
		},
		"spec": specObject,
	}
	data, err := json.Marshal(ingress)
	if err != nil {
		return RenderedExposure{}, err
	}
	return RenderedExposure{Manifest: data, Ingress: ingress, Namespace: namespace, IngressName: ingressName, BackendServiceName: workload.ServiceName, ServicePort: canonical.ServicePort, SpecHash: canonical.SpecHash, FieldManager: ExposureFieldManager}, nil
}

func validateExposureWorkloadIdentity(command deploymentv1.AgentCommand, spec exposurev1.ExposureSpec) error {
	workloadHash, err := command.Workload.Hash()
	if err != nil || workloadHash != command.SpecHash || command.JobID != spec.DeploymentJobID || command.ProjectID != spec.ProjectID || command.EnvironmentID != spec.EnvironmentID || command.RuntimeID != spec.RuntimeID || command.Workload.ServiceKey != spec.ServiceKey || command.Workload.ContainerPort != spec.ServicePort {
		return exposureError(CodeExposureIdentityMismatch, spec, "refresh the exposure from the authoritative deployment result")
	}
	return nil
}

func resolveTLSSecret(ctx context.Context, resolver TLSSecretResolver, spec exposurev1.ExposureSpec, namespace string) (string, error) {
	if resolver == nil {
		return "", exposureError(CodeTLSSecretResolution, spec, "configure the protected TLS reference resolver")
	}
	secret, err := resolver.ResolveTLSSecret(ctx, namespace, spec.TLS.SecretReference)
	if err != nil {
		return "", exposureError(CodeTLSSecretResolution, spec, "verify the protected TLS reference and retry")
	}
	if !secret.Exists || secret.Reference != spec.TLS.SecretReference {
		return "", exposureError(CodeTLSSecretMissing, spec, "select an existing protected TLS reference")
	}
	if !secret.OpsiOwned {
		return "", exposureError(CodeTLSSecretForeign, spec, "select an Opsi-owned TLS reference")
	}
	if secret.Namespace != namespace {
		return "", exposureError(CodeTLSSecretNamespace, spec, "select a TLS reference scoped to the workload namespace")
	}
	if secret.Type != KubernetesTLSSecret || !validDNSSubdomain(secret.Name) {
		return "", exposureError(CodeTLSSecretType, spec, "select a verified kubernetes.io/tls secret reference")
	}
	return secret.Name, nil
}

func exposureIdentityHash(spec exposurev1.ExposureSpec) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{spec.ProjectID, spec.EnvironmentID, spec.RuntimeID, spec.ServiceKey}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func serviceObjectHasExactPort(service map[string]any, port int32) bool {
	spec, _ := service["spec"].(map[string]any)
	ports, _ := spec["ports"].([]any)
	if len(ports) != 1 {
		return false
	}
	item, _ := ports[0].(map[string]any)
	value, exact := exactInteger(item["port"])
	return exact && value == int64(port) && item["protocol"] == "TCP"
}

func exactInteger(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case float64:
		return int64(number), number == math.Trunc(number)
	case json.Number:
		result, err := number.Int64()
		return result, err == nil
	default:
		return 0, false
	}
}

func validDNSSubdomain(value string) bool {
	if len(value) == 0 || len(value) > 253 || !dnsSubdomainPattern.MatchString(value) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}

func exposureError(code string, spec exposurev1.ExposureSpec, action string) error {
	return &ExposureError{Code: code, Hostname: spec.Hostname, Path: spec.Path, SafeAction: action}
}
