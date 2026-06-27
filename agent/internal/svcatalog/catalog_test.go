package svcatalog

import (
	"strings"
	"testing"
)

func TestBuiltInCatalogRendersPostgresDefaults(t *testing.T) {
	schema, ok := BuiltInCatalog().Get("postgres")
	if !ok {
		t.Fatal("postgres alias not found")
	}
	rendered, err := schema.RenderWithReader(map[string]string{
		"host": "mydb.default.svc.cluster.local",
	}, strings.NewReader(strings.Repeat("a", 256)))
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Config["database"] != "app" || rendered.Config["port"] != "5432" {
		t.Fatalf("bad postgres defaults: %#v", rendered.Config)
	}
	if len(rendered.Secrets["password"]) != 32 {
		t.Fatalf("bad password length: %d", len(rendered.Secrets["password"]))
	}
	if !strings.Contains(rendered.Env["DATABASE_URL"], "@mydb.default.svc.cluster.local:5432/app") {
		t.Fatalf("bad database url: %s", rendered.Env["DATABASE_URL"])
	}
}

func TestCatalogRejectsUnknownOverride(t *testing.T) {
	_, err := BuiltInCatalog().Render("redis", map[string]string{"wat": "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown config key") {
		t.Fatalf("expected unknown config key, got %v", err)
	}
}

func TestBuiltInCatalogTypes(t *testing.T) {
	types := BuiltInCatalog().Types()
	want := []string{"kafka", "mongodb", "mysql", "postgresql", "rabbitmq", "redis"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("types = %#v", types)
	}
}
