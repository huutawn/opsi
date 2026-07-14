package webhookrelay

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const validProductionConfig = `"production":true,"database_url":"postgres://secret-db.example.test/opsi","public_base_url":"https://cloud.example.test","bootstrap_worker_token":"secret-worker-token-123456789012","bootstrap_secret_key":"secret-bootstrap-key-12345678901","alerts":{"internal_token":"secret-alert-token-1234567890123"},"smtp":{"host":"smtp.example.test","port":"587","from":"opsi@example.test"},"github_app":{"client_id":"client","client_secret":"secret-client","callback_url":"https://cloud.example.test/v1/auth/browser/callback"}`

var legacyAuthEnvNames = []string{
	"OPSI_CLOUD_AUTH_PROVIDER",
	"OPSI_CLOUD_AUTH_CLIENT_ID",
	"OPSI_CLOUD_AUTH_CLIENT_SECRET",
	"OPSI_CLOUD_AUTH_AUTH_URL",
	"OPSI_CLOUD_AUTH_TOKEN_URL",
	"OPSI_CLOUD_AUTH_USERINFO_URL",
	"OPSI_CLOUD_AUTH_REDIRECT_URL",
	"OPSI_CLOUD_AUTH_SCOPES",
}

var cloudEnvNames = append([]string{
	"OPSI_CLOUD_TTL",
	"OPSI_CLOUD_DATABASE_URL",
	"OPSI_CLOUD_PUBLIC_BASE_URL",
	"OPSI_CLOUD_PRODUCTION",
	"OPSI_CLOUD_ENABLE_DEBUG_UI",
	"OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES",
	"OPSI_CLOUD_OTP_DEV_ECHO",
	"OPSI_CLOUD_OTP_OUTBOX_PATH",
	"OPSI_CLOUD_SMTP_HOST",
	"OPSI_CLOUD_SMTP_PORT",
	"OPSI_CLOUD_SMTP_USERNAME",
	"OPSI_CLOUD_SMTP_PASSWORD",
	"OPSI_CLOUD_SMTP_FROM",
	"OPSI_CLOUD_ALERTS_WEBHOOK_URL",
	"OPSI_CLOUD_ALERTS_MIN_SEVERITY",
	"OPSI_CLOUD_ALERTS_OUTBOX_PATH",
	"OPSI_CLOUD_ALERTS_INTERNAL_TOKEN",
	"OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN",
	"OPSI_CLOUD_BOOTSTRAP_SECRET_KEY",
	"OPSI_CLOUD_GITHUB_APP_CLIENT_ID",
	"OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET",
	"OPSI_CLOUD_GITHUB_APP_CALLBACK_URL",
}, legacyAuthEnvNames...)

func clearCloudEnv(t *testing.T) {
	t.Helper()
	for _, name := range cloudEnvNames {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

func writeCloudConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigProductionRequiresDurableSecurity(t *testing.T) {
	clearCloudEnv(t)
	path := writeCloudConfig(t, `{"production":true}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "production requires database_url") {
		t.Fatalf("expected production database_url failure, got %v", err)
	}
	path = writeCloudConfig(t, `{"production":true,"database_url":"postgres://example","bootstrap_worker_token":"short","bootstrap_secret_key":"also-short"}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected production config without strong tokens to fail")
	}
	cfg, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireAgentSignatures {
		t.Fatal("production must require agent request signatures")
	}
}

func TestLoadConfigProductionRejectsDevEchoAndHTTPURL(t *testing.T) {
	clearCloudEnv(t)
	if _, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig+`,"otp":{"dev_echo":true}}`)); err == nil {
		t.Fatal("expected production dev echo to fail")
	}
	if _, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig+`,"public_base_url":"http://cloud.example.test"}`)); err == nil {
		t.Fatal("expected production http public url to fail")
	}
}

func TestLoadConfigProductionRequiresSMTPAndForbidsOTPOutbox(t *testing.T) {
	clearCloudEnv(t)
	withoutSMTP := strings.Replace(validProductionConfig, `,"smtp":{"host":"smtp.example.test","port":"587","from":"opsi@example.test"}`, "", 1)
	if _, err := LoadConfig(writeCloudConfig(t, `{`+withoutSMTP+`}`)); err == nil || !strings.Contains(err.Error(), "requires smtp") {
		t.Fatalf("missing SMTP error=%v", err)
	}
	if _, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig+`,"otp":{"outbox_path":"/tmp/otp.log"}}`)); err == nil || !strings.Contains(err.Error(), "forbids otp.outbox_path") {
		t.Fatalf("production OTP outbox error=%v", err)
	}
}

func TestLoadConfigProductionRejectsDebugUI(t *testing.T) {
	clearCloudEnv(t)
	_, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig+`,"enable_debug_ui":true}`))
	if err == nil || !strings.Contains(err.Error(), "production forbids enable_debug_ui") {
		t.Fatalf("expected debug UI validation error, got %v", err)
	}
	for _, secret := range []string{"secret-db", "secret-worker-token", "secret-bootstrap-key", "secret-alert-token", "secret-client"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validation error leaked sensitive config value %q: %q", secret, err)
		}
	}
}

func TestLoadConfigProductionDebugUIDisabledPasses(t *testing.T) {
	clearCloudEnv(t)
	cfg, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig+`,"enable_debug_ui":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableDebugUI {
		t.Fatal("production debug UI must stay disabled")
	}
}

func TestLoadConfigNonProductionDebugUIExplicitlyEnabledPasses(t *testing.T) {
	clearCloudEnv(t)
	cfg, err := LoadConfig(writeCloudConfig(t, `{"enable_debug_ui":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableDebugUI {
		t.Fatal("non-production explicit debug UI should remain enabled")
	}
}

func TestLoadConfigRejectsUnsignedWebhookRoute(t *testing.T) {
	clearCloudEnv(t)
	data := `{"routes":[{"project_id":"proj-1","service_id":"svc-1","repo_full_name":"example/api","branch":"main"}]}`
	if _, err := LoadConfig(writeCloudConfig(t, data)); err == nil || !strings.Contains(err.Error(), "webhook_secret") {
		t.Fatalf("unsigned webhook route error=%v", err)
	}
}

func TestLoadConfigDefaultDoesNotEnableDebugUI(t *testing.T) {
	clearCloudEnv(t)
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableDebugUI {
		t.Fatal("default config must not enable debug UI")
	}
}

func TestLoadConfigGitHubAppEnvironmentOverridesJSON(t *testing.T) {
	clearCloudEnv(t)
	path := writeCloudConfig(t, `{"github_app":{"client_id":"json-client","client_secret":"json-secret","callback_url":"https://json.example.test/v1/auth/browser/callback"}}`)
	t.Setenv("OPSI_CLOUD_GITHUB_APP_CLIENT_ID", "env-client")
	t.Setenv("OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET", "env-secret")
	t.Setenv("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", "http://localhost:8080/v1/auth/browser/callback")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := GitHubAppConfig{ClientID: "env-client", ClientSecret: "env-secret", CallbackURL: "http://localhost:8080/v1/auth/browser/callback"}
	if cfg.GitHubApp != want {
		t.Fatalf("GitHub App config=%#v, want %#v", cfg.GitHubApp, want)
	}
}

func TestLoadConfigEnvironmentOverridesJSON(t *testing.T) {
	clearCloudEnv(t)
	path := writeCloudConfig(t, `{"database_url":"postgres://json.example/opsi","smtp":{"username":"json-user"}}`)
	t.Setenv("OPSI_CLOUD_DATABASE_URL", "postgres://env.example/opsi")
	t.Setenv("OPSI_CLOUD_SMTP_USERNAME", "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://env.example/opsi" || cfg.SMTP.Username != "" {
		t.Fatalf("environment overrides not applied: %#v", cfg)
	}
}

func TestLoadConfigEnvironmentOnly(t *testing.T) {
	clearCloudEnv(t)
	values := map[string]string{
		"OPSI_CLOUD_TTL":                      "90m",
		"OPSI_CLOUD_DATABASE_URL":             "postgres://env.example/opsi",
		"OPSI_CLOUD_PUBLIC_BASE_URL":          "http://cloud.example.test",
		"OPSI_CLOUD_PRODUCTION":               "false",
		"OPSI_CLOUD_ENABLE_DEBUG_UI":          "true",
		"OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES": "true",
		"OPSI_CLOUD_OTP_DEV_ECHO":             "true",
		"OPSI_CLOUD_OTP_OUTBOX_PATH":          "/tmp/otp.log",
		"OPSI_CLOUD_SMTP_HOST":                "smtp.example.test",
		"OPSI_CLOUD_SMTP_PORT":                "587",
		"OPSI_CLOUD_SMTP_USERNAME":            "smtp-user",
		"OPSI_CLOUD_SMTP_PASSWORD":            "smtp-password",
		"OPSI_CLOUD_SMTP_FROM":                "opsi@example.test",
		"OPSI_CLOUD_ALERTS_WEBHOOK_URL":       "https://alerts.example.test/hook",
		"OPSI_CLOUD_ALERTS_MIN_SEVERITY":      "high",
		"OPSI_CLOUD_ALERTS_OUTBOX_PATH":       "/tmp/alerts.log",
		"OPSI_CLOUD_ALERTS_INTERNAL_TOKEN":    "alert-token",
		"OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN":   "worker-token",
		"OPSI_CLOUD_BOOTSTRAP_SECRET_KEY":     "bootstrap-key",
		"OPSI_CLOUD_GITHUB_APP_CLIENT_ID":     "client-id",
		"OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET": "client-secret",
		"OPSI_CLOUD_GITHUB_APP_CALLBACK_URL":  "http://127.0.0.1:8080/v1/auth/browser/callback",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TTL != Duration(90*time.Minute) || cfg.DatabaseURL != values["OPSI_CLOUD_DATABASE_URL"] ||
		cfg.GitHubApp.ClientID != "client-id" || cfg.GitHubApp.ClientSecret != "client-secret" ||
		cfg.GitHubApp.CallbackURL != values["OPSI_CLOUD_GITHUB_APP_CALLBACK_URL"] {
		t.Fatalf("environment-only config mismatch: %#v", cfg)
	}
}

func TestLoadConfigDevelopmentAllowsEmptyGitHubCredentials(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_GITHUB_APP_CLIENT_ID", "")
	t.Setenv("OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET", "")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubApp.ClientID != "" || cfg.GitHubApp.ClientSecret != "" {
		t.Fatalf("GitHub auth unexpectedly enabled: %#v", cfg.GitHubApp)
	}
}

func TestLoadConfigDevelopmentAllowsCallbackOnly(t *testing.T) {
	clearCloudEnv(t)
	cfg, err := LoadConfig(writeCloudConfig(t, `{"github_app":{"callback_url":"http://localhost:9876/v1/auth/browser/callback"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubApp.CallbackURL != "http://localhost:9876/v1/auth/browser/callback" {
		t.Fatalf("callback URL=%q", cfg.GitHubApp.CallbackURL)
	}
}

func TestLoadConfigRejectsPartialGitHubCredentials(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
	}{
		{name: "missing secret", config: `{"github_app":{"client_id":"client"}}`},
		{name: "missing client ID", config: `{"github_app":{"client_secret":"secret"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearCloudEnv(t)
			if _, err := LoadConfig(writeCloudConfig(t, test.config)); err == nil || !strings.Contains(err.Error(), "configured together") {
				t.Fatalf("partial credentials error=%v", err)
			}
		})
	}
}

func TestLoadConfigNormalizesGitHubClientIDAndRejectsUnsafeCredentials(t *testing.T) {
	clearCloudEnv(t)
	cfg, err := LoadConfig(writeCloudConfig(t, `{"github_app":{"client_id":"  client-id  ","client_secret":"secret","callback_url":"https://cloud.example.test/v1/auth/browser/callback"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubApp.ClientID != "client-id" {
		t.Fatalf("client ID was not trimmed: %q", cfg.GitHubApp.ClientID)
	}
	for _, data := range []string{
		`{"github_app":{"client_id":"client id","client_secret":"secret","callback_url":"https://cloud.example.test/v1/auth/browser/callback"}}`,
		"{\"github_app\":{\"client_id\":\"client\",\"client_secret\":\"secret\\nvalue\",\"callback_url\":\"https://cloud.example.test/v1/auth/browser/callback\"}}",
	} {
		if _, err := LoadConfig(writeCloudConfig(t, data)); err == nil {
			t.Fatalf("unsafe GitHub credential config was accepted: %s", data)
		}
	}
}

func TestLoadConfigRejectsEnabledGitHubAuthWithoutCallback(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", "")
	_, err := LoadConfig(writeCloudConfig(t, `{"github_app":{"client_id":"client","client_secret":"secret"}}`))
	if err == nil || !strings.Contains(err.Error(), "callback_url is required") {
		t.Fatalf("missing callback error=%v", err)
	}
}

func TestLoadConfigRejectsInvalidGitHubCallback(t *testing.T) {
	for _, callback := range []string{
		"/v1/auth/browser/callback",
		"http://example.test/v1/auth/browser/callback",
		"https://example.test/wrong",
		"https://user@example.test/v1/auth/browser/callback",
		"https://example.test/v1/auth/browser/callback?x=1",
		"https://example.test/v1/auth/browser/callback#fragment",
	} {
		t.Run(callback, func(t *testing.T) {
			clearCloudEnv(t)
			data := `{"github_app":{"client_id":"client","client_secret":"secret","callback_url":"` + callback + `"}}`
			if _, err := LoadConfig(writeCloudConfig(t, data)); err == nil {
				t.Fatalf("invalid callback %q was accepted", callback)
			}
		})
	}
}

func TestLoadConfigProductionRequiresFullGitHubAppConfig(t *testing.T) {
	clearCloudEnv(t)
	withoutGitHub := strings.Replace(validProductionConfig, `,"github_app":{"client_id":"client","client_secret":"secret-client","callback_url":"https://cloud.example.test/v1/auth/browser/callback"}`, "", 1)
	_, err := LoadConfig(writeCloudConfig(t, `{`+withoutGitHub+`}`))
	if err == nil || !strings.Contains(err.Error(), "production requires github_app") {
		t.Fatalf("missing GitHub App error=%v", err)
	}
}

func TestLoadConfigProductionRejectsHTTPGitHubCallback(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig, "https://cloud.example.test/v1/auth/browser/callback", "http://cloud.example.test/v1/auth/browser/callback", 1)
	_, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
	if err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("HTTP callback error=%v", err)
	}
}

func TestLoadConfigProductionRejectsGitHubCallbackHostMismatch(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig, "https://cloud.example.test/v1/auth/browser/callback", "https://other.example.test/v1/auth/browser/callback", 1)
	_, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
	if err == nil || !strings.Contains(err.Error(), "must match public_base_url") {
		t.Fatalf("callback host mismatch error=%v", err)
	}
}

func TestLoadConfigProductionNormalizesDefaultHTTPSPort(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig, "https://cloud.example.test/v1/auth/browser/callback", "https://cloud.example.test:443/v1/auth/browser/callback", 1)
	if _, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`)); err != nil {
		t.Fatalf("default HTTPS port should match: %v", err)
	}
}

func TestLoadConfigGitHubSecretErrorsDoNotLeakSecret(t *testing.T) {
	clearCloudEnv(t)
	secret := "secret-client-value"
	_, err := LoadConfig(writeCloudConfig(t, `{"github_app":{"client_secret":"`+secret+`"}}`))
	if err == nil {
		t.Fatal("expected partial GitHub App config to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error leaked client secret: %q", err)
	}
}

func TestLoadConfigRejectsLegacyAuthEnvironmentEvenWhenEmpty(t *testing.T) {
	for _, name := range legacyAuthEnvNames {
		t.Run(name, func(t *testing.T) {
			clearCloudEnv(t)
			t.Setenv(name, "")
			_, err := LoadConfig("")
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "OPSI_CLOUD_GITHUB_APP_*") {
				t.Fatalf("legacy environment error=%v", err)
			}
		})
	}
}

func TestLoadConfigRejectsNonEmptyLegacyAuthJSON(t *testing.T) {
	clearCloudEnv(t)
	_, err := LoadConfig(writeCloudConfig(t, `{"auth":{"client_id":"legacy"}}`))
	if err == nil || err.Error() != "legacy auth config is no longer supported; use github_app" {
		t.Fatalf("legacy JSON error=%v", err)
	}
	if _, err := LoadConfig(writeCloudConfig(t, `{"auth":{}}`)); err != nil {
		t.Fatalf("empty legacy auth object should not enable a fallback: %v", err)
	}
}

func TestConfigHasNoRuntimeAuthField(t *testing.T) {
	typeOfConfig := reflect.TypeOf(Config{})
	if _, ok := typeOfConfig.FieldByName("Auth"); ok {
		t.Fatal("Config still exposes the generic Auth field")
	}
	if _, ok := typeOfConfig.FieldByName("GitHubApp"); !ok {
		t.Fatal("Config is missing GitHubApp")
	}
}

func TestLoadConfigParsesBooleanEnvironment(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_ENABLE_DEBUG_UI", "true")
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableDebugUI {
		t.Fatal("expected enable debug UI environment override")
	}
}

func TestLoadConfigRejectsInvalidBooleanEnvironment(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_PRODUCTION", "not-a-boolean")
	_, err := LoadConfig("")
	if err == nil || !strings.Contains(err.Error(), "OPSI_CLOUD_PRODUCTION") {
		t.Fatalf("invalid boolean error=%v", err)
	}
}

func TestLoadConfigRejectsInvalidDurationEnvironment(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_TTL", "not-a-duration")
	_, err := LoadConfig("")
	if err == nil || !strings.Contains(err.Error(), "OPSI_CLOUD_TTL") {
		t.Fatalf("invalid duration error=%v", err)
	}
}

func TestLoadConfigValidatesProductionAfterEnvironmentOverride(t *testing.T) {
	clearCloudEnv(t)
	path := writeCloudConfig(t, `{`+validProductionConfig+`,"enable_debug_ui":true}`)
	t.Setenv("OPSI_CLOUD_ENABLE_DEBUG_UI", "false")
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("environment override should run before production validation: %v", err)
	}
}

func TestLoadConfigEnvironmentErrorDoesNotLeakSecrets(t *testing.T) {
	clearCloudEnv(t)
	secrets := map[string]string{
		"OPSI_CLOUD_DATABASE_URL":             "postgres://secret-database-value/opsi",
		"OPSI_CLOUD_SMTP_PASSWORD":            "secret-smtp-password",
		"OPSI_CLOUD_ALERTS_INTERNAL_TOKEN":    "secret-alert-token",
		"OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN":   "secret-worker-token",
		"OPSI_CLOUD_BOOTSTRAP_SECRET_KEY":     "secret-bootstrap-key",
		"OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET": "secret-github-client",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	t.Setenv("OPSI_CLOUD_PRODUCTION", "invalid")
	_, err := LoadConfig("")
	if err == nil {
		t.Fatal("expected invalid boolean error")
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("environment error leaked secret %q: %q", secret, err)
		}
	}
}
