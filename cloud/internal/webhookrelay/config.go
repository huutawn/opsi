package webhookrelay

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	githubCallbackPath  = "/v1/auth/browser/callback"
	legacyAuthEnvPrefix = "OPSI_CLOUD_" + "AUTH_"
)

type Config struct {
	TTL                    Duration        `json:"ttl"`
	DatabaseURL            string          `json:"database_url"`
	PublicBaseURL          string          `json:"public_base_url"`
	Production             bool            `json:"production"`
	EnableDebugUI          bool            `json:"enable_debug_ui"`
	OTP                    OTPConfig       `json:"otp"`
	SMTP                   SMTPConfig      `json:"smtp"`
	Alerts                 AlertConfig     `json:"alerts"`
	Routes                 []Route         `json:"routes"`
	BootstrapWorkerToken   string          `json:"bootstrap_worker_token"`
	BootstrapSecretKey     string          `json:"bootstrap_secret_key"`
	RequireAgentSignatures bool            `json:"require_agent_signatures"`
	GitHubApp              GitHubAppConfig `json:"github_app"`
}

type OTPConfig struct {
	DevEcho    bool   `json:"dev_echo"`
	OutboxPath string `json:"outbox_path"`
}

type SMTPConfig struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
}

type AlertConfig struct {
	WebhookURL    string `json:"webhook_url"`
	MinSeverity   string `json:"min_severity"`
	OutboxPath    string `json:"outbox_path"`
	InternalToken string `json:"internal_token"`
}

type GitHubAppConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	CallbackURL  string `json:"callback_url"`
}

type Route struct {
	ProjectID     string `json:"project_id"`
	ServiceID     string `json:"service_id"`
	ServiceName   string `json:"service_name"`
	ServiceType   string `json:"service_type"`
	RepoURL       string `json:"repo_url"`
	RepoFullName  string `json:"repo_full_name"`
	Branch        string `json:"branch"`
	WebhookSecret string `json:"webhook_secret"`
}

type Duration time.Duration

func LoadConfig(path string) (Config, error) {
	cfg := Config{
		TTL: Duration(24 * time.Hour),
		GitHubApp: GitHubAppConfig{
			CallbackURL: "http://127.0.0.1:8080" + githubCallbackPath,
		},
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read cloud config: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse cloud config: %w", err)
		}
		if err := rejectLegacyAuthJSON(data); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnvOverrides(&cfg); err != nil {
		return Config{}, err
	}
	if err := validateConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) error {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, legacyAuthEnvPrefix) {
			return fmt.Errorf("%s is no longer supported; use OPSI_CLOUD_GITHUB_APP_*", name)
		}
	}
	if err := applyDurationEnv("OPSI_CLOUD_TTL", &cfg.TTL); err != nil {
		return err
	}
	applyStringEnv("OPSI_CLOUD_DATABASE_URL", &cfg.DatabaseURL)
	applyStringEnv("OPSI_CLOUD_PUBLIC_BASE_URL", &cfg.PublicBaseURL)
	if err := applyBoolEnv("OPSI_CLOUD_PRODUCTION", &cfg.Production); err != nil {
		return err
	}
	if err := applyBoolEnv("OPSI_CLOUD_ENABLE_DEBUG_UI", &cfg.EnableDebugUI); err != nil {
		return err
	}
	if err := applyBoolEnv("OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES", &cfg.RequireAgentSignatures); err != nil {
		return err
	}
	if err := applyBoolEnv("OPSI_CLOUD_OTP_DEV_ECHO", &cfg.OTP.DevEcho); err != nil {
		return err
	}
	applyStringEnv("OPSI_CLOUD_OTP_OUTBOX_PATH", &cfg.OTP.OutboxPath)
	applyStringEnv("OPSI_CLOUD_SMTP_HOST", &cfg.SMTP.Host)
	applyStringEnv("OPSI_CLOUD_SMTP_PORT", &cfg.SMTP.Port)
	applyStringEnv("OPSI_CLOUD_SMTP_USERNAME", &cfg.SMTP.Username)
	applyStringEnv("OPSI_CLOUD_SMTP_PASSWORD", &cfg.SMTP.Password)
	applyStringEnv("OPSI_CLOUD_SMTP_FROM", &cfg.SMTP.From)
	applyStringEnv("OPSI_CLOUD_ALERTS_WEBHOOK_URL", &cfg.Alerts.WebhookURL)
	applyStringEnv("OPSI_CLOUD_ALERTS_MIN_SEVERITY", &cfg.Alerts.MinSeverity)
	applyStringEnv("OPSI_CLOUD_ALERTS_OUTBOX_PATH", &cfg.Alerts.OutboxPath)
	applyStringEnv("OPSI_CLOUD_ALERTS_INTERNAL_TOKEN", &cfg.Alerts.InternalToken)
	applyStringEnv("OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN", &cfg.BootstrapWorkerToken)
	applyStringEnv("OPSI_CLOUD_BOOTSTRAP_SECRET_KEY", &cfg.BootstrapSecretKey)
	applyStringEnv("OPSI_CLOUD_GITHUB_APP_CLIENT_ID", &cfg.GitHubApp.ClientID)
	applyStringEnv("OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET", &cfg.GitHubApp.ClientSecret)
	applyStringEnv("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", &cfg.GitHubApp.CallbackURL)
	return nil
}

func applyStringEnv(name string, target *string) {
	if value, ok := os.LookupEnv(name); ok {
		*target = value
	}
}

func applyBoolEnv(name string, target *bool) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid boolean", name)
	}
	*target = parsed
	return nil
}

func applyDurationEnv(name string, target *Duration) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid duration", name)
	}
	*target = Duration(parsed)
	return nil
}

func validateConfig(cfg *Config) error {
	if time.Duration(cfg.TTL) <= 0 {
		cfg.TTL = Duration(24 * time.Hour)
	}
	if time.Duration(cfg.TTL) > 24*time.Hour {
		return fmt.Errorf("ttl must be <= 24h")
	}
	for i, route := range cfg.Routes {
		if route.ProjectID == "" || route.ServiceID == "" || route.RepoFullName == "" || route.Branch == "" {
			return fmt.Errorf("routes[%d] requires project_id, service_id, repo_full_name and branch", i)
		}
		if len(route.WebhookSecret) < 32 {
			return fmt.Errorf("routes[%d].webhook_secret must contain at least 32 bytes", i)
		}
	}
	if cfg.Production {
		if cfg.DatabaseURL == "" {
			return fmt.Errorf("production requires database_url")
		}
		if len(cfg.BootstrapWorkerToken) < 32 {
			return fmt.Errorf("production requires bootstrap_worker_token with at least 32 bytes")
		}
		if len(cfg.BootstrapSecretKey) < 32 {
			return fmt.Errorf("production requires bootstrap_secret_key with at least 32 bytes")
		}
		if len(cfg.Alerts.InternalToken) < 32 {
			return fmt.Errorf("production requires alerts.internal_token with at least 32 bytes")
		}
		if cfg.OTP.DevEcho {
			return fmt.Errorf("production forbids otp.dev_echo")
		}
		if cfg.OTP.OutboxPath != "" {
			return fmt.Errorf("production forbids otp.outbox_path")
		}
		if cfg.SMTP.Host == "" || cfg.SMTP.Port == "" || cfg.SMTP.From == "" {
			return fmt.Errorf("production requires smtp host, port and from")
		}
		if cfg.EnableDebugUI {
			return fmt.Errorf("production forbids enable_debug_ui")
		}
		if cfg.PublicBaseURL == "" || !strings.HasPrefix(cfg.PublicBaseURL, "https://") {
			return fmt.Errorf("production requires an https public_base_url")
		}
		cfg.RequireAgentSignatures = true
	}
	return validateGitHubAppConfig(cfg)
}

func rejectLegacyAuthJSON(data []byte) error {
	var envelope struct {
		Auth json.RawMessage `json:"auth"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Auth) == 0 {
		return nil
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Auth, &legacy); err == nil && len(legacy) > 0 {
		return fmt.Errorf("legacy auth config is no longer supported; use github_app")
	}
	return nil
}

func validateGitHubAppConfig(cfg *Config) error {
	github := &cfg.GitHubApp
	github.ClientID = strings.TrimSpace(github.ClientID)
	if strings.IndexFunc(github.ClientID, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return fmt.Errorf("github_app.client_id must not contain whitespace or control characters")
	}
	if strings.IndexFunc(github.ClientSecret, unicode.IsControl) >= 0 {
		return fmt.Errorf("github_app.client_secret must not contain control characters")
	}
	if (github.ClientID == "") != (github.ClientSecret == "") {
		return fmt.Errorf("github_app.client_id and github_app.client_secret must be configured together")
	}

	enabled := github.ClientID != ""
	if cfg.Production && !enabled {
		return fmt.Errorf("production requires github_app client_id, client_secret and callback_url")
	}
	if enabled && github.CallbackURL == "" {
		return fmt.Errorf("github_app.callback_url is required when GitHub user authorization is enabled")
	}
	if github.CallbackURL != "" {
		callback, err := validateGitHubCallbackURL(github.CallbackURL, cfg.Production)
		if err != nil {
			return err
		}
		if cfg.Production {
			publicURL, err := url.Parse(cfg.PublicBaseURL)
			if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" {
				return fmt.Errorf("production requires an https public_base_url")
			}
			if !sameURLOrigin(callback, publicURL) {
				return fmt.Errorf("production github_app.callback_url must match public_base_url scheme and host")
			}
		}
	}
	if cfg.Production && github.CallbackURL == "" {
		return fmt.Errorf("production requires github_app client_id, client_secret and callback_url")
	}
	return nil
}

func validateGitHubCallbackURL(raw string, production bool) (*url.URL, error) {
	callback, err := url.Parse(raw)
	if err != nil || !callback.IsAbs() || callback.Host == "" {
		return nil, fmt.Errorf("github_app.callback_url must be an absolute URL")
	}
	if callback.User != nil || callback.RawQuery != "" || callback.ForceQuery || callback.Fragment != "" {
		return nil, fmt.Errorf("github_app.callback_url must not contain user info, query or fragment")
	}
	if callback.Path != githubCallbackPath {
		return nil, fmt.Errorf("github_app.callback_url path must be %s", githubCallbackPath)
	}
	if production {
		if callback.Scheme != "https" {
			return nil, fmt.Errorf("production github_app.callback_url must use https")
		}
		return callback, nil
	}
	if callback.Scheme == "https" {
		return callback, nil
	}
	host := strings.ToLower(callback.Hostname())
	if callback.Scheme != "http" || (host != "127.0.0.1" && host != "localhost") {
		return nil, fmt.Errorf("development github_app.callback_url must use https or loopback http")
	}
	return callback, nil
}

func sameURLOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectivePort(left) == effectivePort(right)
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
