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

	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
)

const (
	githubCallbackPath  = "/v1/auth/browser/callback"
	buildRecordPath     = "/v1/build-records"
	legacyAuthEnvPrefix = "OPSI_CLOUD_" + "AUTH_"
	maxConfigFileBytes  = 64 * 1024
)

type Config struct {
	TTL                    Duration          `json:"ttl"`
	DatabaseURL            string            `json:"database_url"`
	PublicBaseURL          string            `json:"public_base_url"`
	Production             bool              `json:"production"`
	EnableDebugUI          bool              `json:"enable_debug_ui"`
	OTP                    OTPConfig         `json:"otp"`
	SMTP                   SMTPConfig        `json:"smtp"`
	Alerts                 AlertConfig       `json:"alerts"`
	Routes                 []Route           `json:"routes"`
	BootstrapWorkerToken   string            `json:"bootstrap_worker_token"`
	BootstrapSecretKey     string            `json:"bootstrap_secret_key"`
	RequireAgentSignatures bool              `json:"require_agent_signatures"`
	GitHubApp              GitHubAppConfig   `json:"github_app"`
	GitHubOIDC             githuboidc.Config `json:"github_oidc"`
	Placement              PlacementConfig   `json:"placement"`
}

type PlacementConfig struct {
	HeartbeatTTL        Duration `json:"heartbeat_ttl"`
	ReservedCPUMilli    int64    `json:"reserved_cpu_millicores"`
	ReservedMemoryBytes int64    `json:"reserved_memory_bytes"`
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
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	CallbackURL    string `json:"callback_url"`
	AppID          int64  `json:"app_id"`
	PrivateKeyPath string `json:"private_key_path"`
	WebhookSecret  string `json:"webhook_secret"`
}

func (c GitHubAppConfig) InstallationEnabled() bool {
	return c.AppID != 0 || c.PrivateKeyPath != "" || c.WebhookSecret != ""
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
		TTL:        Duration(24 * time.Hour),
		GitHubOIDC: githuboidc.DefaultConfig(),
		Placement: PlacementConfig{
			HeartbeatTTL: Duration(2 * time.Minute), ReservedCPUMilli: 250, ReservedMemoryBytes: 256 << 20,
		},
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
	if err := applyStringOrFileEnv("OPSI_CLOUD_DATABASE_URL", "OPSI_CLOUD_DATABASE_URL_FILE", &cfg.DatabaseURL); err != nil {
		return err
	}
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
	if err := applyStringOrFileEnv("OPSI_CLOUD_SMTP_PASSWORD", "OPSI_CLOUD_SMTP_PASSWORD_FILE", &cfg.SMTP.Password); err != nil {
		return err
	}
	applyStringEnv("OPSI_CLOUD_SMTP_FROM", &cfg.SMTP.From)
	applyStringEnv("OPSI_CLOUD_ALERTS_WEBHOOK_URL", &cfg.Alerts.WebhookURL)
	applyStringEnv("OPSI_CLOUD_ALERTS_MIN_SEVERITY", &cfg.Alerts.MinSeverity)
	applyStringEnv("OPSI_CLOUD_ALERTS_OUTBOX_PATH", &cfg.Alerts.OutboxPath)
	if err := applyStringOrFileEnv("OPSI_CLOUD_ALERTS_INTERNAL_TOKEN", "OPSI_CLOUD_ALERTS_INTERNAL_TOKEN_FILE", &cfg.Alerts.InternalToken); err != nil {
		return err
	}
	if err := applyStringOrFileEnv("OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN", "OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN_FILE", &cfg.BootstrapWorkerToken); err != nil {
		return err
	}
	if err := applyStringOrFileEnv("OPSI_CLOUD_BOOTSTRAP_SECRET_KEY", "OPSI_CLOUD_BOOTSTRAP_SECRET_KEY_FILE", &cfg.BootstrapSecretKey); err != nil {
		return err
	}
	applyStringEnv("OPSI_CLOUD_GITHUB_APP_CLIENT_ID", &cfg.GitHubApp.ClientID)
	if err := applyStringOrFileEnv("OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET", "OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET_FILE", &cfg.GitHubApp.ClientSecret); err != nil {
		return err
	}
	applyStringEnv("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", &cfg.GitHubApp.CallbackURL)
	if err := applyInt64Env("OPSI_CLOUD_GITHUB_APP_ID", &cfg.GitHubApp.AppID); err != nil {
		return err
	}
	applyStringEnv("OPSI_CLOUD_GITHUB_APP_PRIVATE_KEY_PATH", &cfg.GitHubApp.PrivateKeyPath)
	if err := applyStringOrFileEnv("OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET", "OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET_FILE", &cfg.GitHubApp.WebhookSecret); err != nil {
		return err
	}
	if err := applyBoolEnv("OPSI_CLOUD_GITHUB_OIDC_ENABLED", &cfg.GitHubOIDC.Enabled); err != nil {
		return err
	}
	applyStringEnv("OPSI_CLOUD_GITHUB_OIDC_ISSUER", &cfg.GitHubOIDC.Issuer)
	applyStringEnv("OPSI_CLOUD_GITHUB_OIDC_JWKS_URL", &cfg.GitHubOIDC.JWKSURL)
	applyStringEnv("OPSI_CLOUD_GITHUB_OIDC_AUDIENCE", &cfg.GitHubOIDC.Audience)
	if err := applyGitHubOIDCDurationEnv("OPSI_CLOUD_GITHUB_OIDC_CLOCK_SKEW", &cfg.GitHubOIDC.ClockSkew); err != nil {
		return err
	}
	if err := applyGitHubOIDCDurationEnv("OPSI_CLOUD_GITHUB_OIDC_HTTP_TIMEOUT", &cfg.GitHubOIDC.HTTPTimeout); err != nil {
		return err
	}
	if err := applyGitHubOIDCDurationEnv("OPSI_CLOUD_GITHUB_OIDC_CACHE_TTL", &cfg.GitHubOIDC.CacheTTL); err != nil {
		return err
	}
	if err := applyIntEnv("OPSI_CLOUD_GITHUB_OIDC_MAX_TOKEN_BYTES", &cfg.GitHubOIDC.MaxTokenBytes); err != nil {
		return err
	}
	if err := applyIntEnv("OPSI_CLOUD_GITHUB_OIDC_MAX_JWKS_BYTES", &cfg.GitHubOIDC.MaxJWKSBytes); err != nil {
		return err
	}
	if err := applyIntEnv("OPSI_CLOUD_GITHUB_OIDC_MAX_JWK_KEYS", &cfg.GitHubOIDC.MaxJWKKeys); err != nil {
		return err
	}
	return nil
}

func applyGitHubOIDCDurationEnv(name string, target *githuboidc.Duration) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid duration", name)
	}
	*target = githuboidc.Duration(parsed)
	return nil
}

func applyIntEnv(name string, target *int) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s must be a positive integer", name)
	}
	*target = parsed
	return nil
}

func applyStringEnv(name string, target *string) {
	if value, ok := os.LookupEnv(name); ok {
		*target = value
	}
}

func applyStringOrFileEnv(valueName, fileName string, target *string) error {
	value, valueSet := os.LookupEnv(valueName)
	path, fileSet := os.LookupEnv(fileName)
	if valueSet && fileSet {
		return fmt.Errorf("%s and %s are mutually exclusive", valueName, fileName)
	}
	if valueSet {
		*target = value
		return nil
	}
	if !fileSet {
		return nil
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s must name a non-empty file path", fileName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", fileName, err)
	}
	if len(data) > maxConfigFileBytes {
		return fmt.Errorf("%s exceeds %d bytes", fileName, maxConfigFileBytes)
	}
	*target = trimOneTrailingNewline(string(data))
	return nil
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

func applyInt64Env(name string, target *int64) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	if value == "" {
		*target = 0
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s must be a positive base-10 integer", name)
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
	if time.Duration(cfg.Placement.HeartbeatTTL) == 0 {
		cfg.Placement.HeartbeatTTL = Duration(2 * time.Minute)
	}
	if ttl := time.Duration(cfg.Placement.HeartbeatTTL); ttl < 30*time.Second || ttl > 30*time.Minute {
		return fmt.Errorf("placement.heartbeat_ttl must be between 30s and 30m")
	}
	if cfg.Placement.ReservedCPUMilli < 0 || cfg.Placement.ReservedCPUMilli > 1_000_000 {
		return fmt.Errorf("placement.reserved_cpu_millicores is outside bounded values")
	}
	if cfg.Placement.ReservedMemoryBytes < 0 || cfg.Placement.ReservedMemoryBytes > 1<<50 {
		return fmt.Errorf("placement.reserved_memory_bytes is outside bounded values")
	}
	for i, route := range cfg.Routes {
		if route.ProjectID == "" || route.ServiceID == "" || route.RepoFullName == "" || route.Branch == "" {
			return fmt.Errorf("routes[%d] requires project_id, service_id, repo_full_name and branch", i)
		}
		if len(route.WebhookSecret) < 32 {
			return fmt.Errorf("routes[%d].webhook_secret must contain at least 32 bytes", i)
		}
		if cfg.Production && isProductionPlaceholder(route.WebhookSecret) {
			return fmt.Errorf("production routes[%d].webhook_secret must not use a placeholder", i)
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
		if !cfg.RequireAgentSignatures {
			return fmt.Errorf("production requires require_agent_signatures=true")
		}
		publicBaseURL, err := normalizeProductionPublicBaseURL(cfg.PublicBaseURL)
		if err != nil {
			return err
		}
		cfg.PublicBaseURL = publicBaseURL
		for name, value := range map[string]string{
			"database_url":                cfg.DatabaseURL,
			"public_base_url":             cfg.PublicBaseURL,
			"bootstrap_worker_token":      cfg.BootstrapWorkerToken,
			"bootstrap_secret_key":        cfg.BootstrapSecretKey,
			"alerts.internal_token":       cfg.Alerts.InternalToken,
			"smtp.host":                   cfg.SMTP.Host,
			"smtp.username":               cfg.SMTP.Username,
			"smtp.password":               cfg.SMTP.Password,
			"smtp.from":                   cfg.SMTP.From,
			"github_app.client_id":        cfg.GitHubApp.ClientID,
			"github_app.client_secret":    cfg.GitHubApp.ClientSecret,
			"github_app.callback_url":     cfg.GitHubApp.CallbackURL,
			"github_app.webhook_secret":   cfg.GitHubApp.WebhookSecret,
			"github_app.private_key_path": cfg.GitHubApp.PrivateKeyPath,
		} {
			if isProductionPlaceholder(value) {
				return fmt.Errorf("production %s must not use a placeholder", name)
			}
		}
		if isProductionPlaceholder(cfg.GitHubOIDC.Audience) {
			return fmt.Errorf("production github_oidc.audience must not use a placeholder")
		}
		for i, workload := range cfg.GitHubOIDC.Workloads {
			for _, values := range [][]string{workload.WorkflowRefs, workload.JobWorkflowRefs, workload.Refs, workload.Events, workload.OCIRepositories} {
				for _, value := range values {
					if isProductionPlaceholder(value) {
						return fmt.Errorf("production github_oidc.workloads[%d] must not use placeholders", i)
					}
				}
			}
		}
	}
	if err := validateGitHubAppConfig(cfg); err != nil {
		return err
	}
	if cfg.Production {
		expectedAudience := cfg.PublicBaseURL + buildRecordPath
		if cfg.GitHubOIDC.Audience != expectedAudience {
			return fmt.Errorf("production github_oidc.audience must exactly match %s", expectedAudience)
		}
	}
	return cfg.GitHubOIDC.Validate(cfg.Production)
}

func normalizeProductionPublicBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("production requires an exact https public_base_url")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" {
		return "", fmt.Errorf("production public_base_url must contain only an https origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.HasSuffix(hostname, ".") {
		return "", fmt.Errorf("production public_base_url host is invalid")
	}
	port := parsed.Port()
	if port == "443" {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return "https://" + host, nil
}

func isProductionPlaceholder(value string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	return strings.Contains(normalized, "REPLACE_WITH_") ||
		strings.Contains(normalized, "CHANGE_ME") ||
		strings.Contains(normalized, "EXAMPLE_SECRET") ||
		strings.Contains(strings.ToLower(normalized), "example.invalid") ||
		(len(normalized) == 64 && strings.Trim(normalized, "0") == "")
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

	userAuthorizationEnabled := github.ClientID != ""
	if cfg.Production && !userAuthorizationEnabled {
		return fmt.Errorf("production requires github_app client_id, client_secret and callback_url")
	}
	if userAuthorizationEnabled && github.CallbackURL == "" {
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

	github.WebhookSecret = trimOneTrailingNewline(github.WebhookSecret)
	if github.AppID < 0 {
		return fmt.Errorf("github_app.app_id must be a positive integer")
	}
	installationEnabled := github.InstallationEnabled()
	if installationEnabled {
		if github.AppID <= 0 || github.PrivateKeyPath == "" || github.WebhookSecret == "" {
			return fmt.Errorf("github_app app_id, private_key_path and webhook_secret must be configured together")
		}
		if strings.TrimSpace(github.PrivateKeyPath) != github.PrivateKeyPath {
			return fmt.Errorf("github_app.private_key_path must not contain leading or trailing whitespace")
		}
		if strings.TrimSpace(github.WebhookSecret) != github.WebhookSecret {
			return fmt.Errorf("github_app.webhook_secret must not contain leading or trailing whitespace")
		}
		if strings.IndexFunc(github.WebhookSecret, unicode.IsControl) >= 0 {
			return fmt.Errorf("github_app.webhook_secret must not contain control characters")
		}
		if len([]byte(github.WebhookSecret)) < 32 {
			return fmt.Errorf("github_app.webhook_secret must contain at least 32 bytes")
		}
		if err := validateGitHubAppPrivateKeyFile(github.PrivateKeyPath); err != nil {
			return err
		}
	}
	if cfg.Production && !installationEnabled {
		return fmt.Errorf("production requires github_app app_id, private_key_path and webhook_secret")
	}
	return nil
}

func trimOneTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	return strings.TrimSuffix(value, "\n")
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
