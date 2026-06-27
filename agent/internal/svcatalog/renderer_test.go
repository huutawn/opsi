package svcatalog

import (
	"strings"
	"testing"
)

func TestRenderManagedPostgresManifest(t *testing.T) {
	rendered, err := RenderManagedWithReader(RenderRequest{
		ProjectID: "demo",
		Name:      "mydb",
		Type:      "postgres",
		Overrides: map[string]string{"database": "myapp"},
	}, strings.NewReader(strings.Repeat("a", 256)))
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered.YAML)
	for _, want := range []string{
		"kind: StatefulSet",
		"name: opsi-svc-mydb",
		"opsi.io/project-id: demo",
		"DATABASE_URL:",
		"OPSI_MYDB_DATABASE_URL:",
		"postgresql://opsi:",
		"@mydb.default.svc.cluster.local:5432/myapp",
		"storage: 5Gi",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in manifest:\n%s", want, out)
		}
	}
	if rendered.Service.SecretName != "opsi-svc-mydb" || rendered.Service.Host != "mydb.default.svc.cluster.local" {
		t.Fatalf("bad service metadata: %#v", rendered.Service)
	}
}

func TestRenderManagedRedisManifest(t *testing.T) {
	rendered, err := RenderManaged(RenderRequest{
		ProjectID: "demo",
		Name:      "cache",
		Type:      "redis",
		Namespace: "prod",
		Overrides: map[string]string{"max_memory": "128mb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(rendered.YAML)
	for _, want := range []string{
		"kind: Deployment",
		"image: redis:7-alpine",
		"REDIS_URL:",
		"OPSI_CACHE_REDIS_URL:",
		"redis://cache.prod.svc.cluster.local:6379",
		`"128mb"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in manifest:\n%s", want, out)
		}
	}
}

func TestRenderManagedRejectsUnsupportedAndUnsafe(t *testing.T) {
	if _, err := RenderManaged(RenderRequest{ProjectID: "demo", Name: "Bad_Name", Type: "redis"}); err == nil {
		t.Fatal("expected unsafe name error")
	}
	if _, err := RenderManaged(RenderRequest{ProjectID: "demo", Name: "broker", Type: "kafka"}); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected unsupported renderer, got %v", err)
	}
}
