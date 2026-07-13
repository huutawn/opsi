package webhookrelay

import (
	"encoding/json"
	"fmt"
	"os"
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
	AgentTokens            []string    `json:"agent_tokens"`
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
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read cloud config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse cloud config: %w", err)
	}
	if time.Duration(cfg.TTL) <= 0 {
		cfg.TTL = Duration(24 * time.Hour)
	}
	if time.Duration(cfg.TTL) > 24*time.Hour {
		return Config{}, fmt.Errorf("ttl must be <= 24h")
	}
	for i, route := range cfg.Routes {
		if route.ProjectID == "" || route.ServiceID == "" || route.RepoFullName == "" || route.Branch == "" {
			return Config{}, fmt.Errorf("routes[%d] requires project_id, service_id, repo_full_name and branch", i)
		}
		if len(route.WebhookSecret) < 32 {
			return Config{}, fmt.Errorf("routes[%d].webhook_secret must contain at least 32 bytes", i)
		}
	}
	if cfg.Production {
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("production requires database_url")
		}
		if len(cfg.BootstrapWorkerToken) < 32 {
			return Config{}, fmt.Errorf("production requires bootstrap_worker_token with at least 32 bytes")
		}
		if len(cfg.BootstrapSecretKey) < 32 {
			return Config{}, fmt.Errorf("production requires bootstrap_secret_key with at least 32 bytes")
		}
		if len(cfg.Alerts.InternalToken) < 32 {
			return Config{}, fmt.Errorf("production requires alerts.internal_token with at least 32 bytes")
		}
		if cfg.OTP.DevEcho {
			return Config{}, fmt.Errorf("production forbids otp.dev_echo")
		}
		if cfg.OTP.OutboxPath != "" {
			return Config{}, fmt.Errorf("production forbids otp.outbox_path")
		}
		if cfg.SMTP.Host == "" || cfg.SMTP.Port == "" || cfg.SMTP.From == "" {
			return Config{}, fmt.Errorf("production requires smtp host, port and from")
		}
		if cfg.EnableDebugUI {
			return Config{}, fmt.Errorf("production forbids enable_debug_ui")
		}
		if cfg.Auth.Provider == "" || cfg.Auth.ClientID == "" || cfg.Auth.ClientSecret == "" || cfg.Auth.AuthURL == "" || cfg.Auth.TokenURL == "" || cfg.Auth.UserInfoURL == "" || cfg.Auth.RedirectURL == "" {
			return Config{}, fmt.Errorf("production requires auth OAuth config")
		}
		if cfg.PublicBaseURL == "" || !strings.HasPrefix(cfg.PublicBaseURL, "https://") {
			return Config{}, fmt.Errorf("production requires an https public_base_url")
		}
		cfg.RequireAgentSignatures = true
	}
	return cfg, nil
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
