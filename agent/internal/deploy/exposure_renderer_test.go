package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func testExposureSpec(t *testing.T) exposurev1.ExposureSpec {
	t.Helper()
	command := testAgentCommand(t)
	spec, err := (exposurev1.ExposureSpec{
		SchemaVersion:   exposurev1.SchemaVersion,
		ProjectID:       command.ProjectID,
		EnvironmentID:   command.EnvironmentID,
		RuntimeID:       command.RuntimeID,
		ServiceKey:      command.Workload.ServiceKey,
		DeploymentJobID: command.JobID,
		Hostname:        "API.Example.COM",
		Path:            "/api/",
		ServicePort:     command.Workload.ContainerPort,
		TLS:             exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled},
	}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestRenderExposureIsDeterministicAndTargetsExactOwnedService(t *testing.T) {
	command := testAgentCommand(t)
	spec := testExposureSpec(t)
	first, err := renderExposure(context.Background(), command, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		next, err := renderExposure(context.Background(), command, spec, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(next, first) {
			t.Fatalf("renderer changed on iteration %d", index)
		}
	}
	if first.FieldManager != ExposureFieldManager || first.SpecHash != spec.SpecHash || first.ServicePort != command.Workload.ContainerPort {
		t.Fatalf("render identity=%+v", first)
	}
	if first.Namespace == "" || first.IngressName == "" || first.BackendServiceName == "" {
		t.Fatalf("missing derived resource identity: %+v", first)
	}
	if first.Ingress["apiVersion"] != "networking.k8s.io/v1" || first.Ingress["kind"] != "Ingress" {
		t.Fatalf("unexpected canonical resource: %+v", first.Ingress)
	}
	ingressSpec := first.Ingress["spec"].(map[string]any)
	if ingressSpec["ingressClassName"] != TraefikIngressClass {
		t.Fatalf("ingressClassName=%v", ingressSpec["ingressClassName"])
	}
	if _, exists := ingressSpec["tls"]; exists {
		t.Fatal("disabled TLS rendered a TLS section")
	}
	rules := ingressSpec["rules"].([]any)
	path := rules[0].(map[string]any)["http"].(map[string]any)["paths"].([]any)[0].(map[string]any)
	backend := path["backend"].(map[string]any)["service"].(map[string]any)
	backendPort, exact := exactInteger(backend["port"].(map[string]any)["number"])
	if path["path"] != "/api" || path["pathType"] != "Prefix" || backend["name"] != first.BackendServiceName || !exact || backendPort != int64(command.Workload.ContainerPort) {
		t.Fatalf("unexpected route/backend: path=%+v backend=%+v", path, backend)
	}
	metadata := first.Ingress["metadata"].(map[string]any)
	annotations := stringMap(metadata["annotations"])
	if len(annotations) != 3 || annotations["opsi.dev/spec-hash"] != spec.SpecHash || annotations["opsi.dev/workload-spec-hash"] != command.SpecHash {
		t.Fatalf("annotations=%v", annotations)
	}
	for key := range annotations {
		if strings.Contains(strings.ToLower(key), "traefik") || strings.Contains(strings.ToLower(key), "nginx") {
			t.Fatalf("renderer emitted controller-specific annotation %q", key)
		}
	}
}

func TestRenderExposureTLSReferenceBoundary(t *testing.T) {
	command := testAgentCommand(t)
	spec := testExposureSpec(t)
	spec.TLS = exposurev1.TLSConfig{Mode: exposurev1.TLSSecretRef, SecretReference: "tlsref-1"}
	spec.SpecHash = ""
	var err error
	spec, err = spec.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	base := VerifiedTLSSecret{Reference: spec.TLS.SecretReference, Exists: true, OpsiOwned: true, Type: KubernetesTLSSecret}
	_, workload, namespace, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	base.Namespace = namespace
	base.Name = "opsi-tls-api"
	rendered, err := renderExposure(context.Background(), command, spec, staticTLSResolver{secret: base})
	if err != nil {
		t.Fatal(err)
	}
	ingressSpec := rendered.Ingress["spec"].(map[string]any)
	tls := ingressSpec["tls"].([]any)[0].(map[string]any)
	if tls["secretName"] != base.Name || tls["hosts"].([]any)[0] != spec.Hostname {
		t.Fatalf("tls=%+v workload=%+v", tls, workload)
	}
	cases := []struct {
		name   string
		secret VerifiedTLSSecret
		code   string
	}{
		{name: "missing", secret: VerifiedTLSSecret{Reference: spec.TLS.SecretReference}, code: CodeTLSSecretMissing},
		{name: "foreign", secret: withTLS(base, func(value *VerifiedTLSSecret) { value.OpsiOwned = false }), code: CodeTLSSecretForeign},
		{name: "namespace", secret: withTLS(base, func(value *VerifiedTLSSecret) { value.Namespace = "other" }), code: CodeTLSSecretNamespace},
		{name: "type", secret: withTLS(base, func(value *VerifiedTLSSecret) { value.Type = "Opaque" }), code: CodeTLSSecretType},
		{name: "raw name", secret: withTLS(base, func(value *VerifiedTLSSecret) { value.Name = "raw/name" }), code: CodeTLSSecretType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderExposure(context.Background(), command, spec, staticTLSResolver{secret: tc.secret})
			if exposureCode(err) != tc.code {
				t.Fatalf("err=%v code=%q", err, exposureCode(err))
			}
			if err != nil && strings.Contains(err.Error(), "other-project") {
				t.Fatalf("TLS error leaked foreign identity: %v", err)
			}
		})
	}
}

func TestRenderExposureRejectsCrossWorkloadIdentityAndPort(t *testing.T) {
	command := testAgentCommand(t)
	base := testExposureSpec(t)
	for _, mutate := range []func(*exposurev1.ExposureSpec){
		func(value *exposurev1.ExposureSpec) { value.ProjectID = "other-project" },
		func(value *exposurev1.ExposureSpec) { value.EnvironmentID = "other-env" },
		func(value *exposurev1.ExposureSpec) { value.RuntimeID = "other-runtime" },
		func(value *exposurev1.ExposureSpec) { value.ServiceKey = "worker" },
		func(value *exposurev1.ExposureSpec) { value.DeploymentJobID = "dep-other" },
		func(value *exposurev1.ExposureSpec) { value.ServicePort++ },
	} {
		spec := base
		spec.SpecHash = ""
		mutate(&spec)
		canonical, err := spec.Canonicalize()
		if err != nil {
			t.Fatal(err)
		}
		_, err = renderExposure(context.Background(), command, canonical, nil)
		if exposureCode(err) != CodeExposureIdentityMismatch {
			t.Fatalf("spec=%+v err=%v", canonical, err)
		}
	}
}

type staticTLSResolver struct {
	secret VerifiedTLSSecret
	err    error
}

func (s staticTLSResolver) ResolveTLSSecret(context.Context, string, string) (VerifiedTLSSecret, error) {
	return s.secret, s.err
}

func withTLS(value VerifiedTLSSecret, mutate func(*VerifiedTLSSecret)) VerifiedTLSSecret {
	mutate(&value)
	return value
}

func cloneObject(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func exposureCode(err error) string {
	var exposureErr *ExposureError
	if errors.As(err, &exposureErr) {
		return exposureErr.Code
	}
	return ""
}
