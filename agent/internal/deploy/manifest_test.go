package deploy

import (
	"strings"
	"testing"
)

func TestRenderManifestInjectsDeploymentDefaults(t *testing.T) {
	rendered, err := renderManifest([]byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: api:old
          resources:
            requests:
              cpu: 200m
`), manifestOptions{
		ResourceRequestsJSON:          DefaultResourceRequestsJSON,
		ResourceLimitsJSON:            DefaultResourceLimitsJSON,
		TerminationGracePeriodSeconds: 45,
		IngressEnabled:                true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{"maxUnavailable: 0", "terminationGracePeriodSeconds: 45", "memory: 128Mi", "cpu: 500m", "sleep 10", "cpu: 200m"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in manifest:\n%s", want, out)
		}
	}
}

func TestRenderManifestLeavesNonDeployment(t *testing.T) {
	rendered, err := renderManifest([]byte(`apiVersion: v1
kind: Service
metadata:
  name: api
`), manifestOptions{IngressEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "preStop") || strings.Contains(string(rendered), "resources") {
		t.Fatalf("unexpected mutation:\n%s", rendered)
	}
}

func TestRenderManifestInjectsBindingEnvRefs(t *testing.T) {
	rendered, err := renderManifest([]byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: api:old
          env:
            - name: EXISTING
              value: "1"
`), manifestOptions{BindingDependencies: []ServiceDependency{
		{Name: "primary-db", EnvPrefix: "PRIMARY", EnvKeys: []string{"DATABASE_URL", "POSTGRES_HOST"}, ExposeAsDefault: true},
		{Name: "analytics-db", EnvPrefix: "ANALYTICS", EnvKeys: []string{"DATABASE_URL", "POSTGRES_HOST"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{
		"opsi.io/bindings-checksum: sha256:",
		"name: EXISTING",
		"name: OPSI_PRIMARY_DB_DATABASE_URL",
		"name: PRIMARY_DATABASE_URL",
		"name: DATABASE_URL",
		"name: ANALYTICS_DATABASE_URL",
		"name: opsi-svc-primary-db",
		"key: OPSI_PRIMARY_DB_DATABASE_URL",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in manifest:\n%s", want, out)
		}
	}
}

func TestBindingsChecksumChangesWithAliasPolicy(t *testing.T) {
	a := bindingsChecksum(bindingRefs([]ServiceDependency{{Name: "primary-db", EnvPrefix: "PRIMARY", EnvKeys: []string{"DATABASE_URL"}}}))
	b := bindingsChecksum(bindingRefs([]ServiceDependency{{Name: "primary-db", EnvPrefix: "DB", EnvKeys: []string{"DATABASE_URL"}}}))
	if a == b {
		t.Fatal("expected alias policy to affect checksum")
	}
}
