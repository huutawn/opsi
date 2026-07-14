package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/adminbootstrap"
	"github.com/opsi-dev/opsi/cloud/internal/webhookrelay"
)

func TestBootstrapOwnerHelpListsIdentityFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"admin", "bootstrap-owner", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit=%d stderr=%s", code, stderr.String())
	}
	for _, flag := range []string{"--config", "--email", "--org-name", "--project-name", "--oauth-provider", "--oauth-subject", "--pat-output-file", "--json"} {
		if !strings.Contains(stderr.String(), flag) {
			t.Fatalf("help missing %s: %s", flag, stderr.String())
		}
	}
}

func TestBootstrapOwnerValidationFailsBeforeDatabaseAccess(t *testing.T) {
	tests := [][]string{
		{"admin", "bootstrap-owner"},
		{"admin", "bootstrap-owner", "--config", "missing.json", "--email", "owner@example.com", "--org-name", "Org", "--project-name", "Project"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("expected failure for %v", args)
		}
	}
}

func TestVersionAndCheckRemainAvailable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 || strings.TrimSpace(stdout.String()) == "" {
		t.Fatalf("version exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--check"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "configuration valid") {
		t.Fatalf("check exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCheckLoadsGitHubAppPrivateKey(t *testing.T) {
	for _, name := range []string{
		"OPSI_CLOUD_GITHUB_APP_CLIENT_ID",
		"OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET",
		"OPSI_CLOUD_GITHUB_APP_CALLBACK_URL",
		"OPSI_CLOUD_GITHUB_APP_ID",
		"OPSI_CLOUD_GITHUB_APP_PRIVATE_KEY_PATH",
		"OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET",
	} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "github-app.pem")
	configPath := filepath.Join(dir, "cloud.json")
	writeConfig := func() {
		data := fmt.Sprintf(`{"github_app":{"app_id":12345,"private_key_path":%q,"webhook_secret":"12345678901234567890123456789012"}}`, keyPath)
		if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	invalidContent := "sensitive-invalid-private-key-content"
	if err := os.WriteFile(keyPath, []byte(invalidContent), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--check", "--config", configPath}, &stdout, &stderr); code != 1 || strings.Contains(stderr.String(), invalidContent) {
		t.Fatalf("invalid key check exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--check", "--config", configPath}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "configuration valid") {
		t.Fatalf("valid key check exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPATOutputIsMode0600AndNeverOverwrites(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "opsi-pat-output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	target := filepath.Join(dir, "initial-owner.pat")
	output, err := preparePATOutput(target)
	if err != nil {
		t.Fatal(err)
	}
	raw := "opsi_pat_sensitive_test_value"
	if err := output.write(raw); err != nil {
		t.Fatal(err)
	}
	if err := output.finalize(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("PAT mode=%o", info.Mode().Perm())
	}
	existing, err := preparePATOutput(target)
	if err != nil || !existing.existed {
		t.Fatalf("existing output was not preserved: output=%+v err=%v", existing, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != raw {
		t.Fatalf("existing PAT changed: %q err=%v", data, err)
	}
}

func TestPATOutputCleanupRemovesTemporarySecret(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "opsi-pat-cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	target := filepath.Join(dir, "initial-owner.pat")
	output, err := preparePATOutput(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.write("opsi_pat_sensitive_test_value"); err != nil {
		t.Fatal(err)
	}
	temporary := output.temporary
	output.cleanup()
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary PAT survived cleanup: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target PAT unexpectedly exists: %v", err)
	}
}

func TestBootstrapOutputNeverContainsPATMaterial(t *testing.T) {
	var output bytes.Buffer
	writeBootstrapOwnerResult(&output, adminbootstrap.Result{UserID: "u", OrganizationID: "o", ProjectID: "p", MembershipRole: "Owner", PATCreated: true}, "/tmp/initial-owner.pat")
	if strings.Contains(output.String(), "opsi_pat_") {
		t.Fatalf("human output leaked PAT: %s", output.String())
	}
}

func TestConfigureGitHubAppEventSinkWiresSupportedMutationsAndFailsClosed(t *testing.T) {
	secret := strings.Repeat("s", 32)
	cfg := webhookrelay.Config{GitHubApp: webhookrelay.GitHubAppConfig{AppID: 123, WebhookSecret: secret}}
	server := webhookrelay.NewServer(cfg)
	if err := configureGitHubAppEventSink(server, cfg); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"action":"created","installation":{"id":101,"account":{"id":202,"login":"example","type":"Organization"}}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github-app", bytes.NewReader(body))
	request.Header.Set("X-GitHub-Event", "installation")
	request.Header.Set("X-GitHub-Delivery", "main-wiring")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	server.Registry = nil
	if err := configureGitHubAppEventSink(server, cfg); err == nil {
		t.Fatal("enabled GitHub installation integration accepted nil registry")
	}
}
