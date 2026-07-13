package webhookrelay

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const validProductionConfig = `"production":true,"database_url":"postgres://secret-db.example.test/opsi","public_base_url":"https://cloud.example.test","bootstrap_worker_token":"secret-worker-token-123456789012","bootstrap_secret_key":"secret-bootstrap-key-12345678901","alerts":{"internal_token":"secret-alert-token-1234567890123"},"smtp":{"host":"smtp.example.test","port":"587","from":"opsi@example.test"},"auth":{"provider":"generic","client_id":"client","client_secret":"secret-client","auth_url":"https://auth.example.test/authorize","token_url":"https://auth.example.test/token","user_info_url":"https://auth.example.test/userinfo","redirect_url":"https://cloud.example.test/v1/auth/browser/callback"}`

var cloudEnvNames = []string{
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
	"OPSI_CLOUD_AUTH_PROVIDER",
	"OPSI_CLOUD_AUTH_CLIENT_ID",
	"OPSI_CLOUD_AUTH_CLIENT_SECRET",
	"OPSI_CLOUD_AUTH_AUTH_URL",
	"OPSI_CLOUD_AUTH_TOKEN_URL",
	"OPSI_CLOUD_AUTH_USERINFO_URL",
	"OPSI_CLOUD_AUTH_REDIRECT_URL",
	"OPSI_CLOUD_AUTH_SCOPES",
	"OPSI_CLOUD_AGENT_TOKENS",
}

func clearCloudEnv(t *testing.T) {
	t.Helper()
	for _, name := range cloudEnvNames {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

func TestLoadConfigProductionRequiresDurableSecurity(t *testing.T) {
	clearCloudEnv(t)
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{"production":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "production requires database_url") {
		t.Fatalf("expected production database_url failure, got %v", err)
	}
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
	clearCloudEnv(t)
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

func TestLoadConfigProductionRequiresSMTPAndForbidsOTPOutbox(t *testing.T) {
	clearCloudEnv(t)
	path := filepath.Join(t.TempDir(), "cloud.json")
	withoutSMTP := strings.Replace(validProductionConfig, `,"smtp":{"host":"smtp.example.test","port":"587","from":"opsi@example.test"}`, "", 1)
	if err := os.WriteFile(path, []byte(`{`+withoutSMTP+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "requires smtp") {
		t.Fatalf("missing SMTP error=%v", err)
	}
	if err := os.WriteFile(path, []byte(`{`+validProductionConfig+`,"otp":{"outbox_path":"/tmp/otp.log"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "forbids otp.outbox_path") {
		t.Fatalf("production OTP outbox error=%v", err)
	}
}

func TestLoadConfigProductionRejectsDebugUI(t *testing.T) {
	clearCloudEnv(t)
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
	clearCloudEnv(t)
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
	clearCloudEnv(t)
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

func TestLoadConfigRejectsUnsignedWebhookRoute(t *testing.T) {
	clearCloudEnv(t)
	path := filepath.Join(t.TempDir(), "cloud.json")
	data := `{"routes":[{"project_id":"proj-1","service_id":"svc-1","repo_full_name":"example/api","branch":"main"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "webhook_secret") {
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

func TestLoadConfigEnvironmentOverridesJSON(t *testing.T) {
	clearCloudEnv(t)
	path := filepath.Join(t.TempDir(), "cloud.json")
	data := `{"database_url":"postgres://json.example/opsi","smtp":{"username":"json-user"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPSI_CLOUD_DATABASE_URL", "postgres://env.example/opsi")
	t.Setenv("OPSI_CLOUD_SMTP_USERNAME", "")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://env.example/opsi" {
		t.Fatalf("database URL=%q", cfg.DatabaseURL)
	}
	if cfg.SMTP.Username != "" {
		t.Fatalf("explicit empty SMTP username did not override JSON: %q", cfg.SMTP.Username)
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
		"OPSI_CLOUD_AUTH_PROVIDER":            "generic",
		"OPSI_CLOUD_AUTH_CLIENT_ID":           "client-id",
		"OPSI_CLOUD_AUTH_CLIENT_SECRET":       "client-secret",
		"OPSI_CLOUD_AUTH_AUTH_URL":            "https://auth.example.test/authorize",
		"OPSI_CLOUD_AUTH_TOKEN_URL":           "https://auth.example.test/token",
		"OPSI_CLOUD_AUTH_USERINFO_URL":        "https://auth.example.test/userinfo",
		"OPSI_CLOUD_AUTH_REDIRECT_URL":        "http://cloud.example.test/callback",
		"OPSI_CLOUD_AUTH_SCOPES":              "openid, profile",
		"OPSI_CLOUD_AGENT_TOKENS":             "agent-one, agent-two",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	expected := Config{
		TTL:                    Duration(90 * time.Minute),
		DatabaseURL:            "postgres://env.example/opsi",
		PublicBaseURL:          "http://cloud.example.test",
		EnableDebugUI:          true,
		RequireAgentSignatures: true,
		OTP: OTPConfig{
			DevEcho:    true,
			OutboxPath: "/tmp/otp.log",
		},
		SMTP: SMTPConfig{
			Host:     "smtp.example.test",
			Port:     "587",
			Username: "smtp-user",
			Password: "smtp-password",
			From:     "opsi@example.test",
		},
		Alerts: AlertConfig{
			WebhookURL:    "https://alerts.example.test/hook",
			MinSeverity:   "high",
			OutboxPath:    "/tmp/alerts.log",
			InternalToken: "alert-token",
		},
		AgentTokens:          []string{"agent-one", "agent-two"},
		BootstrapWorkerToken: "worker-token",
		BootstrapSecretKey:   "bootstrap-key",
		Auth: AuthConfig{
			Provider:     "generic",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			AuthURL:      "https://auth.example.test/authorize",
			TokenURL:     "https://auth.example.test/token",
			UserInfoURL:  "https://auth.example.test/userinfo",
			RedirectURL:  "http://cloud.example.test/callback",
			Scopes:       []string{"openid", "profile"},
		},
	}
	if !reflect.DeepEqual(cfg, expected) {
		t.Fatalf("config mismatch:\n got: %#v\nwant: %#v", cfg, expected)
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

func TestLoadConfigSplitsOAuthScopesEnvironment(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_AUTH_SCOPES", " openid, profile, ,email ,")

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Auth.Scopes, []string{"openid", "profile", "email"}) {
		t.Fatalf("OAuth scopes=%q", cfg.Auth.Scopes)
	}
}

func TestLoadConfigSplitsAgentTokensEnvironment(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_AGENT_TOKENS", " token-one, token-two, ,token-three ")

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AgentTokens, []string{"token-one", "token-two", "token-three"}) {
		t.Fatalf("agent tokens=%q", cfg.AgentTokens)
	}
}

func TestLoadConfigValidatesProductionAfterEnvironmentOverride(t *testing.T) {
	clearCloudEnv(t)
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{`+validProductionConfig+`,"enable_debug_ui":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPSI_CLOUD_ENABLE_DEBUG_UI", "false")

	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("environment override should run before production validation: %v", err)
	}
}

func TestLoadConfigEnvironmentErrorDoesNotLeakSecrets(t *testing.T) {
	clearCloudEnv(t)
	secrets := map[string]string{
		"OPSI_CLOUD_DATABASE_URL":           "postgres://secret-database-value/opsi",
		"OPSI_CLOUD_SMTP_PASSWORD":          "secret-smtp-password",
		"OPSI_CLOUD_ALERTS_INTERNAL_TOKEN":  "secret-alert-token",
		"OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN": "secret-worker-token",
		"OPSI_CLOUD_BOOTSTRAP_SECRET_KEY":   "secret-bootstrap-key",
		"OPSI_CLOUD_AUTH_CLIENT_SECRET":     "secret-oauth-client",
		"OPSI_CLOUD_AGENT_TOKENS":           "secret-agent-token",
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
