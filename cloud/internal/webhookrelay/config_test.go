package webhookrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validProductionConfig = `"production":true,"database_url":"postgres://secret-db.example.test/opsi","public_base_url":"https://cloud.example.test","bootstrap_worker_token":"secret-worker-token-123456789012","bootstrap_secret_key":"secret-bootstrap-key-12345678901","alerts":{"internal_token":"secret-alert-token-1234567890123"}`

func TestLoadConfigProductionRequiresDurableSecurity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{"production":true,"database_url":"postgres://example","bootstrap_worker_token":"short","bootstrap_secret_key":"also-short"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected production config without strong tokens to fail")
	}
	if err := os.WriteFile(path, []byte(`{`+validProductionConfig+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireAgentSignatures {
		t.Fatal("production must require agent request signatures")
	}
}

func TestLoadConfigProductionRejectsDevEchoAndHTTPURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{`+validProductionConfig+`,"otp":{"dev_echo":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected production dev echo to fail")
	}
	if err := os.WriteFile(path, []byte(`{`+validProductionConfig+`,"public_base_url":"http://cloud.example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected production http public url to fail")
	}
}

func TestLoadConfigProductionRejectsDebugUI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{`+validProductionConfig+`,"enable_debug_ui":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected production debug UI to fail")
	}
	if !strings.Contains(err.Error(), "production forbids enable_debug_ui") {
		t.Fatalf("expected debug UI validation error, got %q", err)
	}
	for _, secret := range []string{"secret-db", "secret-worker-token", "secret-bootstrap-key", "secret-alert-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validation error leaked sensitive config value %q: %q", secret, err)
		}
	}
}

func TestLoadConfigProductionDebugUIDisabledPasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{`+validProductionConfig+`,"enable_debug_ui":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableDebugUI {
		t.Fatal("production debug UI must stay disabled")
	}
}

func TestLoadConfigNonProductionDebugUIExplicitlyEnabledPasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{"enable_debug_ui":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableDebugUI {
		t.Fatal("non-production explicit debug UI should remain enabled")
	}
}

func TestLoadConfigDefaultDoesNotEnableDebugUI(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableDebugUI {
		t.Fatal("default config must not enable debug UI")
	}
}
