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
	if err := os.WriteFile(path, []byte(`{"production":true,"database_url":"postgres://example","public_base_url":"https://cloud.example.test","bootstrap_worker_token":"12345678901234567890123456789012","bootstrap_secret_key":"abcdefghijklmnopqrstuvwxyz123456","alerts":{"internal_token":"12345678901234567890123456789012"}}`), 0600); err != nil {
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
	base := `"production":true,"database_url":"postgres://example","bootstrap_worker_token":"12345678901234567890123456789012","bootstrap_secret_key":"abcdefghijklmnopqrstuvwxyz123456","alerts":{"internal_token":"12345678901234567890123456789012"}`
	if err := os.WriteFile(path, []byte(`{`+base+`,"otp":{"dev_echo":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected production dev echo to fail")
	}
	if err := os.WriteFile(path, []byte(`{`+base+`,"public_base_url":"http://cloud.example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected production http public url to fail")
	}
}
