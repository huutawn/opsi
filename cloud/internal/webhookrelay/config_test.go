package webhookrelay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigProductionRequiresDurableSecurity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{"production":true,"database_url":"postgres://example","bootstrap_worker_token":"short","bootstrap_secret_key":"also-short"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected production config without strong tokens to fail")
	}
	if err := os.WriteFile(path, []byte(`{"production":true,"database_url":"postgres://example","bootstrap_worker_token":"12345678901234567890123456789012","bootstrap_secret_key":"abcdefghijklmnopqrstuvwxyz123456"}`), 0600); err != nil {
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
