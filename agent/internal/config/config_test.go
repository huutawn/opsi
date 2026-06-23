package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	data := []byte(`node_id: node-1
listen_addr: 127.0.0.1:9443
health_addr: 127.0.0.1:9080
cloud_endpoint: https://cloud.example.test
sqlite_path: ./agent.sqlite
tls:
  ca_cert_path: ./ca.crt
  server_cert_path: ./server.crt
  server_key_path: ./server.key
  require_client_cert: true
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "node-1" || cfg.ListenAddr == "" || !cfg.TLS.RequireClientCert {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestValidateRejectsMissingClientCA(t *testing.T) {
	cfg := Default()
	cfg.TLS.RequireClientCert = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestDefaultUsesContainerdBuilder(t *testing.T) {
	cfg := Default()
	if cfg.Deployment.BuilderMode != "containerd" || cfg.Deployment.ContainerdNS != "k8s.io" || cfg.Deployment.NerdctlPath != "nerdctl" {
		t.Fatalf("unexpected builder defaults: %+v", cfg.Deployment)
	}
}

func TestValidateRejectsUnknownBuilderMode(t *testing.T) {
	cfg := Default()
	cfg.Deployment.BuilderMode = "bad"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected builder mode validation error")
	}
}
