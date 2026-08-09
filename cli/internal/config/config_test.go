package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cli.yaml")
	data := []byte(`agent_addr: 127.0.0.1:9443
cloud_url: https://cloud.example.test
tls:
  ca_cert_path: ./ca.crt
  client_cert_path: ./client.crt
  client_key_path: ./client.key
  pinned_server_cert_sha256: "aa:bb"
  server_name: localhost
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentAddr != "127.0.0.1:9443" || cfg.CloudURL != "https://cloud.example.test" || !cfg.TLS.Enabled() {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestValidateRejectsPartialClientCert(t *testing.T) {
	cfg := Default()
	cfg.TLS.ClientCertPath = "client.crt"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestDefaultUsesHostedBetaCloudWithoutInventingAgentAddress(t *testing.T) {
	cfg := Default()
	if cfg.CloudURL != "https://opsidev.site" || cfg.AgentAddr != "" {
		t.Fatalf("default config=%+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRequiresSelectedConfigAndCloudURL(t *testing.T) {
	if _, err := Load(""); err == nil {
		t.Fatal("missing selected config was accepted")
	}
	path := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(path, []byte("agent_addr: 127.0.0.1:9443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSelected(path); err == nil {
		t.Fatal("selected config silently defaulted cloud_url")
	}
}
