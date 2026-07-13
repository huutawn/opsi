package webhookrelay

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TTL                    Duration    `json:"ttl"`
	DatabaseURL            string      `json:"database_url"`
	PublicBaseURL          string      `json:"public_base_url"`
	Production             bool        `json:"production"`
	EnableDebugUI          bool        `json:"enable_debug_ui"`
	OTP                    OTPConfig   `json:"otp"`
	SMTP                   SMTPConfig  `json:"smtp"`
	Alerts                 AlertConfig `json:"alerts"`
	Routes                 []Route     `json:"routes"`
	BootstrapWorkerToken   string      `json:"bootstrap_worker_token"`
	BootstrapSecretKey     string      `json:"bootstrap_secret_key"`
	RequireAgentSignatures bool        `json:"require_agent_signatures"`
	Auth                   AuthConfig  `json:"auth"`
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

type AuthConfig struct {
	Provider     string   `json:"provider"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	UserInfoURL  string   `json:"user_info_url"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes"`
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
	cfg := Config{TTL: Duration(24 * time.Hour)}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read cloud config: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse cloud config: %w", err)
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
	applyStringEnv("OPSI_CLOUD_AUTH_PROVIDER", &cfg.Auth.Provider)
	applyStringEnv("OPSI_CLOUD_AUTH_CLIENT_ID", &cfg.Auth.ClientID)
	applyStringEnv("OPSI_CLOUD_AUTH_CLIENT_SECRET", &cfg.Auth.ClientSecret)
	applyStringEnv("OPSI_CLOUD_AUTH_AUTH_URL", &cfg.Auth.AuthURL)
	applyStringEnv("OPSI_CLOUD_AUTH_TOKEN_URL", &cfg.Auth.TokenURL)
	applyStringEnv("OPSI_CLOUD_AUTH_USERINFO_URL", &cfg.Auth.UserInfoURL)
	applyStringEnv("OPSI_CLOUD_AUTH_REDIRECT_URL", &cfg.Auth.RedirectURL)
	applyCSVEnv("OPSI_CLOUD_AUTH_SCOPES", &cfg.Auth.Scopes)
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

func applyCSVEnv(name string, target *[]string) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	items := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	*target = items
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
		if cfg.Auth.Provider == "" || cfg.Auth.ClientID == "" || cfg.Auth.ClientSecret == "" || cfg.Auth.AuthURL == "" || cfg.Auth.TokenURL == "" || cfg.Auth.UserInfoURL == "" || cfg.Auth.RedirectURL == "" {
			return fmt.Errorf("production requires auth OAuth config")
		}
		if cfg.PublicBaseURL == "" || !strings.HasPrefix(cfg.PublicBaseURL, "https://") {
			return fmt.Errorf("production requires an https public_base_url")
		}
		cfg.RequireAgentSignatures = true
	}
	return nil
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
