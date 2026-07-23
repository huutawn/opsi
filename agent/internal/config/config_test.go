package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDefaultUsesImmutableDeploymentRuntime(t *testing.T) {
	cfg := Default()
	if cfg.Deployment.RolloutTimeout != "10m" || cfg.Deployment.PollInterval != "5s" || cfg.Deployment.Namespace != "default" {
		t.Fatalf("unexpected builder defaults: %+v", cfg.Deployment)
	}
}

func TestLoadRejectsRemovedIngressEnabledConfig(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "agent.yaml")
			data := []byte("cloud_relay:\n  agent_token: do-not-leak\ndeployment:\n  ingress_enabled: " + value + "\n")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatal("expected removed config key to be rejected")
			}
			if !strings.Contains(err.Error(), "deployment.ingress_enabled has been removed") {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "agent_token") {
				t.Fatalf("error leaked config data: %v", err)
			}
		})
	}
}

func TestLoadRejectsLegacyDeploymentConfig(t *testing.T) {
	for _, key := range []string{"repo_url", "dockerfile", "manifest_path", "builder_mode", "build_root"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, []byte("deployment:\n  "+key+": retired\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "has been removed") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateRejectsInsecureNonLoopback(t *testing.T) {
	cfg := Default()
	cfg.ListenAddr = "0.0.0.0:9443"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-loopback without TLS/auth to fail")
	}
}

func TestValidateProductionGuardrails(t *testing.T) {
	cfg := Default()
	cfg.Mode = "production"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected insecure production config to fail")
	}
	cfg.Auth.Enabled = true
	cfg.TLS.ServerCertPath = "server.crt"
	cfg.TLS.ServerKeyPath = "server.key"
	cfg.Secret.EncryptionAtRestConfirmed = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected hardened production config to pass: %v", err)
	}
}
