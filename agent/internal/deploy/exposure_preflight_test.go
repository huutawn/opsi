package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func TestExposurePreflightAbsentUnchangedAndChanged(t *testing.T) {
	command := testAgentCommand(t)
	spec := testExposureSpec(t)
	rendered, service := exposureFixtures(t, command, spec)

	absent := newExposureRunner(t, rendered, service, nil, nil)
	result, err := (ProductionAdapter{Runner: absent}).PreflightExposure(context.Background(), command, spec, nil)
	if err != nil || result.State != ExposureCreateEligible || len(result.Diff) != 0 {
		t.Fatalf("absent result=%+v err=%v", result, err)
	}

	unchanged := newExposureRunner(t, rendered, service, rendered.Ingress, []map[string]any{rendered.Ingress})
	result, err = (ProductionAdapter{Runner: unchanged}).PreflightExposure(context.Background(), command, spec, nil)
	if err != nil || result.State != ExposureUnchanged || len(result.Diff) != 0 {
		t.Fatalf("unchanged result=%+v err=%v", result, err)
	}

	changedIngress := cloneObject(t, rendered.Ingress)
	changedIngress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"].([]any)[0].(map[string]any)["path"] = "/v2"
	changed := newExposureRunner(t, rendered, service, changedIngress, []map[string]any{changedIngress})
	result, err = (ProductionAdapter{Runner: changed}).PreflightExposure(context.Background(), command, spec, nil)
	if err != nil || result.State != ExposureUpdateEligible || len(result.Diff) != 1 || result.Diff[0].Field != "spec" || result.Diff[0].CurrentHash == result.Diff[0].DesiredHash {
		t.Fatalf("changed result=%+v err=%v", result, err)
	}
}

func TestExposurePreflightRejectsBackendFailures(t *testing.T) {
	command := testAgentCommand(t)
	spec := testExposureSpec(t)
	rendered, service := exposureFixtures(t, command, spec)
	cases := []struct {
		name    string
		service map[string]any
		code    string
	}{
		{name: "missing", service: nil, code: CodeBackendServiceMissing},
		{name: "foreign", service: mutateObject(t, service, func(value map[string]any) {
			value["metadata"].(map[string]any)["labels"].(map[string]any)["app.kubernetes.io/managed-by"] = "foreign"
		}), code: CodeBackendServiceForeign},
		{name: "port", service: mutateObject(t, service, func(value map[string]any) {
			value["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"] = float64(9090)
		}), code: CodeBackendServicePort},
		{name: "selector", service: mutateObject(t, service, func(value map[string]any) {
			value["spec"].(map[string]any)["selector"].(map[string]any)["opsi.dev/service"] = "other"
		}), code: CodeBackendServiceMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newExposureRunner(t, rendered, tc.service, nil, nil)
			_, err := (ProductionAdapter{Runner: runner}).PreflightExposure(context.Background(), command, spec, nil)
			if exposureCode(err) != tc.code {
				t.Fatalf("err=%v code=%q", err, exposureCode(err))
			}
		})
	}
}

func TestExposurePreflightRejectsSameNameAndRouteConflicts(t *testing.T) {
	command := testAgentCommand(t)
	spec := testExposureSpec(t)
	rendered, service := exposureFixtures(t, command, spec)

	foreignSameName := cloneObject(t, rendered.Ingress)
	foreignSameName["metadata"].(map[string]any)["labels"].(map[string]any)["app.kubernetes.io/managed-by"] = "foreign"
	runner := newExposureRunner(t, rendered, service, foreignSameName, []map[string]any{foreignSameName})
	if _, err := (ProductionAdapter{Runner: runner}).PreflightExposure(context.Background(), command, spec, nil); exposureCode(err) != CodeIngressForeign {
		t.Fatalf("same-name foreign err=%v", err)
	}

	unsupported := cloneObject(t, rendered.Ingress)
	unsupported["metadata"].(map[string]any)["annotations"].(map[string]any)["traefik.ingress.kubernetes.io/router.middlewares"] = "foreign-chain"
	runner = newExposureRunner(t, rendered, service, unsupported, []map[string]any{unsupported})
	if _, err := (ProductionAdapter{Runner: runner}).PreflightExposure(context.Background(), command, spec, nil); exposureCode(err) != CodeIngressUnsupported {
		t.Fatalf("unsupported annotation err=%v", err)
	}

	for _, tc := range []struct {
		name   string
		labels map[string]any
		path   string
		code   string
	}{
		{name: "opsi descendant", labels: map[string]any{"app.kubernetes.io/managed-by": "opsi"}, path: "/api/v1", code: CodeOpsiRouteConflict},
		{name: "foreign ancestor", labels: map[string]any{"app.kubernetes.io/managed-by": "other"}, path: "/", code: CodeForeignRouteConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conflict := ingressFixture("other-ns", "other-ingress", spec.Hostname, tc.path, tc.labels)
			runner := newExposureRunner(t, rendered, service, nil, []map[string]any{conflict})
			_, err := (ProductionAdapter{Runner: runner}).PreflightExposure(context.Background(), command, spec, nil)
			if exposureCode(err) != tc.code || (err != nil && strings.Contains(err.Error(), "other-project")) {
				t.Fatalf("route conflict err=%v", err)
			}
		})
	}
	nonConflict := ingressFixture("other-ns", "other-ingress", spec.Hostname, "/api2", map[string]any{"app.kubernetes.io/managed-by": "other"})
	runner = newExposureRunner(t, rendered, service, nil, []map[string]any{nonConflict})
	result, err := (ProductionAdapter{Runner: runner}).PreflightExposure(context.Background(), command, spec, nil)
	if err != nil || result.State != ExposureCreateEligible {
		t.Fatalf("component-aware non-conflict result=%+v err=%v", result, err)
	}
	for _, hostname := range []string{"", "*.example.com"} {
		conflict := ingressFixture("other-ns", "catch-all", hostname, "/api", map[string]any{"app.kubernetes.io/managed-by": "other"})
		runner = newExposureRunner(t, rendered, service, nil, []map[string]any{conflict})
		if _, err := (ProductionAdapter{Runner: runner}).PreflightExposure(context.Background(), command, spec, nil); exposureCode(err) != CodeForeignRouteConflict {
			t.Fatalf("hostname %q conflict err=%v", hostname, err)
		}
	}
	defaultBackend := map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata":   map[string]any{"namespace": "other-ns", "name": "default", "labels": map[string]any{}},
		"spec":       map[string]any{"defaultBackend": map[string]any{"service": map[string]any{"name": "fallback", "port": map[string]any{"number": 80}}}},
	}
	runner = newExposureRunner(t, rendered, service, nil, []map[string]any{defaultBackend})
	if _, err := (ProductionAdapter{Runner: runner}).PreflightExposure(context.Background(), command, spec, nil); exposureCode(err) != CodeForeignRouteConflict {
		t.Fatalf("default backend conflict err=%v", err)
	}
}

func TestExposurePreflightConcurrentResultsAreDeterministic(t *testing.T) {
	command := testAgentCommand(t)
	spec := testExposureSpec(t)
	rendered, service := exposureFixtures(t, command, spec)
	runner := newExposureRunner(t, rendered, service, rendered.Ingress, []map[string]any{rendered.Ingress})
	adapter := ProductionAdapter{Runner: runner}
	const count = 32
	results := make(chan ExposurePreflightResult, count)
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := adapter.PreflightExposure(context.Background(), command, spec, nil)
			results <- result
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *ExposurePreflightResult
	for result := range results {
		if first == nil {
			copy := result
			first = &copy
			continue
		}
		if !reflect.DeepEqual(*first, result) {
			t.Fatalf("concurrent preflight changed: first=%+v next=%+v", *first, result)
		}
	}
}

func exposureFixtures(t *testing.T, command deploymentv1.AgentCommand, spec exposurev1.ExposureSpec) (RenderedExposure, map[string]any) {
	t.Helper()
	rendered, err := renderExposure(context.Background(), command, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, resources, _, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	return rendered, cloneObject(t, resources.Service)
}

func newExposureRunner(t *testing.T, rendered RenderedExposure, service, ingress map[string]any, ingresses []map[string]any) *exposureRunner {
	t.Helper()
	runner := &exposureRunner{outputs: map[string][]byte{}, errors: map[string]error{}}
	serviceKey := kubectlKey("get", "service", rendered.BackendServiceName, "-n", rendered.Namespace, "-o", "json")
	if service == nil {
		runner.errors[serviceKey] = errors.New("not found")
	} else {
		runner.outputs[serviceKey] = mustJSON(t, service)
	}
	ingressKey := kubectlKey("get", "ingress", rendered.IngressName, "-n", rendered.Namespace, "-o", "json")
	if ingress == nil {
		runner.errors[ingressKey] = errors.New("not found")
	} else {
		runner.outputs[ingressKey] = mustJSON(t, ingress)
	}
	runner.outputs[kubectlKey("get", "ingress", "--all-namespaces", "-o", "json")] = mustJSON(t, map[string]any{"items": ingresses})
	return runner
}

type exposureRunner struct {
	outputs map[string][]byte
	errors  map[string]error
}

func (r *exposureRunner) Run(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
	key := kubectlKey(args...)
	if err := r.errors[key]; err != nil {
		return nil, err
	}
	data, exists := r.outputs[key]
	if !exists {
		return nil, errors.New("unexpected kubectl read: " + key)
	}
	return append([]byte(nil), data...), nil
}

func kubectlKey(args ...string) string { return strings.Join(args, "\x00") }

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateObject(t *testing.T, value map[string]any, mutate func(map[string]any)) map[string]any {
	t.Helper()
	copy := cloneObject(t, value)
	mutate(copy)
	return copy
}

func ingressFixture(namespace, name, hostname, path string, labels map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata":   map[string]any{"namespace": namespace, "name": name, "labels": labels},
		"spec": map[string]any{"rules": []any{map[string]any{
			"host": hostname,
			"http": map[string]any{"paths": []any{map[string]any{"path": path, "pathType": "Prefix"}}},
		}}},
	}
}
