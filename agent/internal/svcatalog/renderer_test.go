package svcatalog

import (
	"strings"
	"testing"
)

func TestRenderExternalDNSAndIP(t *testing.T) {
	dns, err := RenderExternal(RegisterExternalRequest{
		ProjectID: "demo",
		Name:      "legacy-db",
		Type:      "postgres",
		Host:      "host.k3s.internal",
		Overrides: map[string]string{"database": "myapp", "username": "postgres", "password": "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out := string(dns.YAML); !strings.Contains(out, "type: ExternalName") || !strings.Contains(out, "externalName: host.k3s.internal") || !strings.Contains(out, "DATABASE_URL:") {
		t.Fatalf("bad dns external manifest:\n%s", out)
	}
	ip, err := RenderExternal(RegisterExternalRequest{
		ProjectID: "demo",
		Name:      "legacy-cache",
		Type:      "redis",
		Host:      "192.168.1.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out := string(ip.YAML); !strings.Contains(out, "kind: EndpointSlice") || !strings.Contains(out, `"192.168.1.10"`) {
		t.Fatalf("bad ip external manifest:\n%s", out)
	}
}

func TestRenderExternalRequiresPasswordWhenSchemaNeedsSecret(t *testing.T) {
	_, err := RenderExternal(RegisterExternalRequest{ProjectID: "demo", Name: "legacy-db", Type: "postgres", Host: "host.k3s.internal"})
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected password error, got %v", err)
	}
}
