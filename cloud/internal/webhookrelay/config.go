package webhookrelay

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Config struct {
	TTL                    Duration   `json:"ttl"`
	DatabaseURL            string     `json:"database_url"`
	Production             bool       `json:"production"`
	OTP                    OTPConfig  `json:"otp"`
	SMTP                   SMTPConfig `json:"smtp"`
	Routes                 []Route    `json:"routes"`
	AgentTokens            []string   `json:"agent_tokens"`
	BootstrapWorkerToken   string     `json:"bootstrap_worker_token"`
	BootstrapSecretKey     string     `json:"bootstrap_secret_key"`
	RequireAgentSignatures bool       `json:"require_agent_signatures"`
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

type Route struct {
	ProjectID    string `json:"project_id"`
	ServiceID    string `json:"service_id"`
	ServiceName  string `json:"service_name"`
	ServiceType  string `json:"service_type"`
	RepoURL      string `json:"repo_url"`
	RepoFullName string `json:"repo_full_name"`
	Branch       string `json:"branch"`
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
