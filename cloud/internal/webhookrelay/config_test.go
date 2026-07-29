package webhookrelay

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const validProductionPrefix = `"production":true,"require_agent_signatures":true,"database_url":"postgres://secret-db.example.test/opsi","public_base_url":"https://cloud.example.test","bootstrap_worker_token":"secret-worker-token-123456789012","bootstrap_secret_key":"secret-bootstrap-key-12345678901","alerts":{"internal_token":"secret-alert-token-1234567890123"},"smtp":{"host":"smtp.example.test","port":"587","from":"opsi@example.test"}`

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
	"OPSI_CLOUD_GITHUB_APP_ID",
	"OPSI_CLOUD_GITHUB_APP_PRIVATE_KEY_PATH",
	"OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET",
	"OPSI_CLOUD_DATABASE_URL_FILE",
	"OPSI_CLOUD_SMTP_PASSWORD_FILE",
	"OPSI_CLOUD_ALERTS_INTERNAL_TOKEN_FILE",
	"OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN_FILE",
	"OPSI_CLOUD_BOOTSTRAP_SECRET_KEY_FILE",
	"OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET_FILE",
	"OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET_FILE",
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

func writePrivateKeyStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "github-app.pem")
	if err := os.WriteFile(path, []byte("private-key-stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validProductionConfig(t *testing.T) string {
	t.Helper()
	return validProductionPrefix + fmt.Sprintf(
		`,"github_app":{"client_id":"client","client_secret":"secret-client","callback_url":"https://cloud.example.test/v1/auth/browser/callback","app_id":12345,"private_key_path":%q,"webhook_secret":"%s"},"github_oidc":{"enabled":true,"audience":"https://cloud.example.test/v1/build-records","workloads":[{"repository_id":1,"service_key":"api","workflow_refs":["huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer"],"refs":["refs/heads/developer"],"events":["push"],"oci_repositories":["ghcr.io/huutawn/opsi/api"]}]}`,
		writePrivateKeyStub(t), strings.Repeat("w", 32),
	)
}

func TestLoadConfigProductionBindsAudienceToCanonicalBuildRecordEndpoint(t *testing.T) {
	clearCloudEnv(t)
	for name, mutate := range map[string]func(string) string{
		"scheme": func(value string) string {
			return strings.Replace(value, "https://cloud.example.test/v1/build-records", "http://cloud.example.test/v1/build-records", 1)
		},
		"host": func(value string) string {
			return strings.Replace(value, "https://cloud.example.test/v1/build-records", "https://other.example.test/v1/build-records", 1)
		},
		"port": func(value string) string {
			return strings.Replace(value, "https://cloud.example.test/v1/build-records", "https://cloud.example.test:8443/v1/build-records", 1)
		},
		"path": func(value string) string {
			return strings.Replace(value, "/v1/build-records", "/v1/build-records/extra", 1)
		},
		"query": func(value string) string {
			return strings.Replace(value, "/v1/build-records", "/v1/build-records?x=1", 1)
		},
		"fragment": func(value string) string {
			return strings.Replace(value, "/v1/build-records", "/v1/build-records#x", 1)
		},
		"trailing slash": func(value string) string { return strings.Replace(value, "/v1/build-records", "/v1/build-records/", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeCloudConfig(t, `{`+mutate(validProductionConfig(t))+`}`))
			if err == nil || !strings.Contains(err.Error(), "github_oidc.audience must exactly match") {
				t.Fatalf("audience mismatch error=%v", err)
			}
		})
	}
}

func TestLoadConfigProductionNormalizesPublicBaseURLForAudience(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig(t), `"public_base_url":"https://cloud.example.test"`, `"public_base_url":"https://CLOUD.EXAMPLE.TEST:443/"`, 1)
	cfg, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicBaseURL != "https://cloud.example.test" || cfg.GitHubOIDC.Audience != cfg.PublicBaseURL+buildRecordPath {
		t.Fatalf("public_base_url=%q audience=%q", cfg.PublicBaseURL, cfg.GitHubOIDC.Audience)
	}
}

func TestLoadConfigProductionRejectsPublicBaseURLPathQueryAndFragment(t *testing.T) {
	clearCloudEnv(t)
	for _, publicURL := range []string{
		"https://cloud.example.test/tenant",
		"https://cloud.example.test/?x=1",
		"https://cloud.example.test/#fragment",
	} {
		t.Run(publicURL, func(t *testing.T) {
			config := strings.Replace(validProductionConfig(t), "https://cloud.example.test", publicURL, 1)
			if _, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`)); err == nil || !strings.Contains(err.Error(), "public_base_url") {
				t.Fatalf("invalid public_base_url error=%v", err)
			}
		})
	}
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
	cfg, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig(t)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireAgentSignatures {
		t.Fatal("production must require agent request signatures")
	}
}

func TestLoadConfigProductionRejectsDisabledAgentSignatures(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig(t), `"require_agent_signatures":true`, `"require_agent_signatures":false`, 1)
	_, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
	if err == nil || !strings.Contains(err.Error(), "require_agent_signatures=true") {
		t.Fatalf("disabled Agent signatures error=%v", err)
	}
}

func TestLoadConfigProductionRejectsPlaceholderSecrets(t *testing.T) {
	clearCloudEnv(t)
	for name, config := range map[string]string{
		"worker token":  strings.Replace(validProductionConfig(t), "secret-worker-token-123456789012", "REPLACE_WITH_RANDOM_WORKER_TOKEN_123", 1),
		"bootstrap key": strings.Replace(validProductionConfig(t), "secret-bootstrap-key-12345678901", "CHANGE_ME_BOOTSTRAP_KEY_123456789", 1),
		"public URL":    strings.Replace(validProductionConfig(t), "https://cloud.example.test", "https://example.invalid", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
			if err == nil || !strings.Contains(err.Error(), "placeholder") {
				t.Fatalf("placeholder config error=%v", err)
			}
		})
	}
}

func TestLoadConfigReadsSecretsFromFiles(t *testing.T) {
	clearCloudEnv(t)
	writeSecret := func(name, value string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN_FILE", writeSecret("worker-token", strings.Repeat("w", 32)))
	t.Setenv("OPSI_CLOUD_BOOTSTRAP_SECRET_KEY_FILE", writeSecret("bootstrap-key", strings.Repeat("b", 32)))
	t.Setenv("OPSI_CLOUD_ALERTS_INTERNAL_TOKEN_FILE", writeSecret("alert-token", strings.Repeat("a", 32)))
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BootstrapWorkerToken != strings.Repeat("w", 32) || cfg.BootstrapSecretKey != strings.Repeat("b", 32) || cfg.Alerts.InternalToken != strings.Repeat("a", 32) {
		t.Fatal("file-backed secrets were not loaded")
	}
}

func TestLoadConfigProductionReadsAllFileBackedValues(t *testing.T) {
	clearCloudEnv(t)
	writeSecret := func(name, value string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	values := map[string]string{
		"OPSI_CLOUD_DATABASE_URL_FILE":              "postgres://file-db.example.test/opsi",
		"OPSI_CLOUD_SMTP_PASSWORD_FILE":             "smtp-file-password",
		"OPSI_CLOUD_ALERTS_INTERNAL_TOKEN_FILE":     strings.Repeat("a", 32),
		"OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN_FILE":    strings.Repeat("w", 32),
		"OPSI_CLOUD_BOOTSTRAP_SECRET_KEY_FILE":      strings.Repeat("b", 32),
		"OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET_FILE":  strings.Repeat("c", 32),
		"OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET_FILE": strings.Repeat("h", 32),
	}
	for name, value := range values {
		t.Setenv(name, writeSecret(strings.ToLower(name), value))
	}
	cfg, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig(t)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != values["OPSI_CLOUD_DATABASE_URL_FILE"] ||
		cfg.SMTP.Password != values["OPSI_CLOUD_SMTP_PASSWORD_FILE"] ||
		cfg.Alerts.InternalToken != values["OPSI_CLOUD_ALERTS_INTERNAL_TOKEN_FILE"] ||
		cfg.BootstrapWorkerToken != values["OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN_FILE"] ||
		cfg.BootstrapSecretKey != values["OPSI_CLOUD_BOOTSTRAP_SECRET_KEY_FILE"] ||
		cfg.GitHubApp.ClientSecret != values["OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET_FILE"] ||
		cfg.GitHubApp.WebhookSecret != values["OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET_FILE"] {
		t.Fatal("production file-backed configuration was not loaded")
	}
}

func TestLoadConfigRejectsSecretValueAndFileTogether(t *testing.T) {
	clearCloudEnv(t)
	path := filepath.Join(t.TempDir(), "worker-token")
	if err := os.WriteFile(path, []byte(strings.Repeat("w", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN", strings.Repeat("x", 32))
	t.Setenv("OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN_FILE", path)
	_, err := LoadConfig("")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("value/file conflict error=%v", err)
	}
}

func TestLoadConfigProductionRejectsDevEchoAndHTTPURL(t *testing.T) {
	clearCloudEnv(t)
	if _, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig(t)+`,"otp":{"dev_echo":true}}`)); err == nil {
		t.Fatal("expected production dev echo to fail")
	}
	if _, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig(t)+`,"public_base_url":"http://cloud.example.test"}`)); err == nil {
		t.Fatal("expected production http public url to fail")
	}
}

func TestLoadConfigProductionRequiresSMTPAndForbidsOTPOutbox(t *testing.T) {
	clearCloudEnv(t)
	withoutSMTP := strings.Replace(validProductionConfig(t), `,"smtp":{"host":"smtp.example.test","port":"587","from":"opsi@example.test"}`, "", 1)
	if _, err := LoadConfig(writeCloudConfig(t, `{`+withoutSMTP+`}`)); err == nil || !strings.Contains(err.Error(), "requires smtp") {
		t.Fatalf("missing SMTP error=%v", err)
	}
	if _, err := LoadConfig(writeCloudConfig(t, `{`+validProductionConfig(t)+`,"otp":{"outbox_path":"/tmp/otp.log"}}`)); err == nil || !strings.Contains(err.Error(), "forbids otp.outbox_path") {
		t.Fatalf("production OTP outbox error=%v", err)
	}
}

func TestLoadConfigRejectsRetiredDeliveryKeys(t *testing.T) {
	for _, data := range []string{
		`{"enable_debug_ui":true}`,
		`{"enable_debug_ui":false}`,
		`{"routes":[]}`,
		`{"routes":[{"webhook_secret":"retired"}]}`,
	} {
		t.Run(data, func(t *testing.T) {
			clearCloudEnv(t)
			_, err := LoadConfig(writeCloudConfig(t, data))
			if err == nil || !strings.Contains(err.Error(), "removed") {
				t.Fatalf("retired delivery config error=%v", err)
			}
		})
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

func TestLoadConfigGitHubAppIDEnvironmentOverridesJSON(t *testing.T) {
	clearCloudEnv(t)
	keyPath := writePrivateKeyStub(t)
	path := writeCloudConfig(t, fmt.Sprintf(`{"github_app":{"app_id":111,"private_key_path":%q,"webhook_secret":"%s"}}`, keyPath, strings.Repeat("w", 32)))
	t.Setenv("OPSI_CLOUD_GITHUB_APP_ID", "222")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubApp.AppID != 222 {
		t.Fatalf("App ID=%d", cfg.GitHubApp.AppID)
	}
}

func TestLoadConfigRejectsInvalidGitHubAppIDEnvironment(t *testing.T) {
	for _, value := range []string{"not-an-integer", "0", "-1"} {
		clearCloudEnv(t)
		t.Setenv("OPSI_CLOUD_GITHUB_APP_ID", value)
		_, err := LoadConfig("")
		if err == nil || !strings.Contains(err.Error(), "OPSI_CLOUD_GITHUB_APP_ID") {
			t.Fatalf("invalid App ID %q error=%v", value, err)
		}
	}
}

func TestLoadConfigRejectsPartialGitHubInstallationConfig(t *testing.T) {
	keyPath := writePrivateKeyStub(t)
	secret := strings.Repeat("w", 32)
	for _, github := range []string{
		`"app_id":123`,
		fmt.Sprintf(`"private_key_path":%q`, keyPath),
		fmt.Sprintf(`"webhook_secret":%q`, secret),
		fmt.Sprintf(`"app_id":123,"private_key_path":%q`, keyPath),
		fmt.Sprintf(`"app_id":123,"webhook_secret":%q`, secret),
		fmt.Sprintf(`"private_key_path":%q,"webhook_secret":%q`, keyPath, secret),
	} {
		clearCloudEnv(t)
		_, err := LoadConfig(writeCloudConfig(t, `{"github_app":{`+github+`}}`))
		if err == nil || !strings.Contains(err.Error(), "configured together") {
			t.Fatalf("partial installation config %q error=%v", github, err)
		}
	}
}

func TestLoadConfigDevelopmentAllowsEmptyGitHubInstallationConfig(t *testing.T) {
	clearCloudEnv(t)
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubApp.InstallationEnabled() {
		t.Fatalf("installation integration unexpectedly enabled: %#v", cfg.GitHubApp)
	}
}

func TestLoadConfigProductionRequiresGitHubInstallationConfig(t *testing.T) {
	clearCloudEnv(t)
	config := validProductionPrefix + `,"github_app":{"client_id":"client","client_secret":"secret-client","callback_url":"https://cloud.example.test/v1/auth/browser/callback"}`
	_, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
	if err == nil || !strings.Contains(err.Error(), "app_id, private_key_path and webhook_secret") {
		t.Fatalf("missing installation config error=%v", err)
	}
}

func TestLoadConfigRejectsUnsafeGitHubInstallationConfig(t *testing.T) {
	secret := strings.Repeat("w", 32)
	t.Run("short webhook secret", func(t *testing.T) {
		clearCloudEnv(t)
		data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":%q,"webhook_secret":"short"}}`, writePrivateKeyStub(t))
		_, err := LoadConfig(writeCloudConfig(t, data))
		if err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
			t.Fatalf("short secret error=%v", err)
		}
	})
	t.Run("relative key path", func(t *testing.T) {
		clearCloudEnv(t)
		data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":"relative.pem","webhook_secret":%q}}`, secret)
		_, err := LoadConfig(writeCloudConfig(t, data))
		if err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Fatalf("relative path error=%v", err)
		}
	})
	t.Run("missing key file", func(t *testing.T) {
		clearCloudEnv(t)
		missing := filepath.Join(t.TempDir(), "missing.pem")
		data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":%q,"webhook_secret":%q}}`, missing, secret)
		_, err := LoadConfig(writeCloudConfig(t, data))
		if err == nil || !strings.Contains(err.Error(), "cannot be accessed") {
			t.Fatalf("missing path error=%v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		clearCloudEnv(t)
		target := writePrivateKeyStub(t)
		link := filepath.Join(t.TempDir(), "key-link.pem")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":%q,"webhook_secret":%q}}`, link, secret)
		_, err := LoadConfig(writeCloudConfig(t, data))
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink error=%v", err)
		}
	})
	t.Run("group writable", func(t *testing.T) {
		clearCloudEnv(t)
		keyPath := writePrivateKeyStub(t)
		if err := os.Chmod(keyPath, 0o620); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":%q,"webhook_secret":%q}}`, keyPath, secret)
		_, err := LoadConfig(writeCloudConfig(t, data))
		if err == nil || !strings.Contains(err.Error(), "group/world writable") {
			t.Fatalf("writable key error=%v", err)
		}
	})
	t.Run("owner unreadable", func(t *testing.T) {
		clearCloudEnv(t)
		keyPath := writePrivateKeyStub(t)
		if err := os.Chmod(keyPath, 0o200); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":%q,"webhook_secret":%q}}`, keyPath, secret)
		_, err := LoadConfig(writeCloudConfig(t, data))
		if err == nil || !strings.Contains(err.Error(), "readable by its owner") {
			t.Fatalf("owner-unreadable key error=%v", err)
		}
	})
	t.Run("empty key file", func(t *testing.T) {
		clearCloudEnv(t)
		keyPath := filepath.Join(t.TempDir(), "empty.pem")
		if err := os.WriteFile(keyPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":%q,"webhook_secret":%q}}`, keyPath, secret)
		_, err := LoadConfig(writeCloudConfig(t, data))
		if err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("empty key error=%v", err)
		}
	})
}

func TestGitHubInstallationValidationErrorsDoNotLeakSecrets(t *testing.T) {
	clearCloudEnv(t)
	secret := "webhook-secret-value-that-must-not-leak"
	pemContent := "BEGIN-SENSITIVE-PRIVATE-KEY-CONTENT"
	keyPath := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(keyPath, []byte(pemContent), 0o600); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"github_app":{"app_id":123,"private_key_path":%q,"webhook_secret":%q}}`, keyPath, secret)
	cfg, err := LoadConfig(writeCloudConfig(t, data))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewGitHubAppClient(cfg.GitHubApp, nil, nil)
	if err == nil {
		t.Fatal("expected invalid PEM error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), pemContent) {
		t.Fatalf("error leaked sensitive configuration: %q", err)
	}
}

func TestLoadConfigP07UserAuthorizationRemainsIndependent(t *testing.T) {
	clearCloudEnv(t)
	cfg, err := LoadConfig(writeCloudConfig(t, `{"github_app":{"client_id":"client","client_secret":"secret","callback_url":"https://cloud.example.test/v1/auth/browser/callback"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubApp.ClientID != "client" || cfg.GitHubApp.InstallationEnabled() {
		t.Fatalf("independent GitHub configuration=%#v", cfg.GitHubApp)
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
	withoutGitHub := validProductionPrefix
	_, err := LoadConfig(writeCloudConfig(t, `{`+withoutGitHub+`}`))
	if err == nil || !strings.Contains(err.Error(), "production requires github_app") {
		t.Fatalf("missing GitHub App error=%v", err)
	}
}

func TestLoadConfigProductionRejectsHTTPGitHubCallback(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig(t), "https://cloud.example.test/v1/auth/browser/callback", "http://cloud.example.test/v1/auth/browser/callback", 1)
	_, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
	if err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("HTTP callback error=%v", err)
	}
}

func TestLoadConfigProductionRejectsGitHubCallbackHostMismatch(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig(t), "https://cloud.example.test/v1/auth/browser/callback", "https://other.example.test/v1/auth/browser/callback", 1)
	_, err := LoadConfig(writeCloudConfig(t, `{`+config+`}`))
	if err == nil || !strings.Contains(err.Error(), "must match public_base_url") {
		t.Fatalf("callback host mismatch error=%v", err)
	}
}

func TestLoadConfigProductionNormalizesDefaultHTTPSPort(t *testing.T) {
	clearCloudEnv(t)
	config := strings.Replace(validProductionConfig(t), "https://cloud.example.test/v1/auth/browser/callback", "https://cloud.example.test:443/v1/auth/browser/callback", 1)
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

func TestLoadConfigRejectsRetiredDebugUIEnvironment(t *testing.T) {
	clearCloudEnv(t)
	t.Setenv("OPSI_CLOUD_ENABLE_DEBUG_UI", "false")
	_, err := LoadConfig("")
	if err == nil || !strings.Contains(err.Error(), "OPSI_CLOUD_ENABLE_DEBUG_UI") {
		t.Fatalf("retired debug UI environment error=%v", err)
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
