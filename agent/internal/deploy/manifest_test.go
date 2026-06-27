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

func TestRenderManifestInjectsBindingSecrets(t *testing.T) {
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
          envFrom:
            - secretRef:
                name: existing
`), manifestOptions{BindingSecrets: []string{"opsi-svc-mydb", "opsi-svc-cache"}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered)
	for _, want := range []string{"opsi.io/bindings-checksum: sha256:", "name: existing", "name: opsi-svc-mydb", "name: opsi-svc-cache"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in manifest:\n%s", want, out)
		}
	}
}
