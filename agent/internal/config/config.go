package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	NodeID        string           `yaml:"node_id"`
	ListenAddr    string           `yaml:"listen_addr"`
	HealthAddr    string           `yaml:"health_addr"`
	CloudEndpoint string           `yaml:"cloud_endpoint"`
	SQLitePath    string           `yaml:"sqlite_path"`
	TLS           TLSConfig        `yaml:"tls"`
	Deployment    DeploymentConfig `yaml:"deployment"`
	Telemetry     TelemetryConfig  `yaml:"telemetry"`
}

type TLSConfig struct {
	CACertPath        string `yaml:"ca_cert_path"`
	ServerCertPath    string `yaml:"server_cert_path"`
	ServerKeyPath     string `yaml:"server_key_path"`
	RequireClientCert bool   `yaml:"require_client_cert"`
}

type DeploymentConfig struct {
	ProjectID      string `yaml:"project_id"`
	ServiceID      string `yaml:"service_id"`
	ServiceName    string `yaml:"service_name"`
	ServiceType    string `yaml:"service_type"`
	RepoURL        string `yaml:"repo_url"`
	Branch         string `yaml:"branch"`
	Namespace      string `yaml:"namespace"`
	BuildContext   string `yaml:"build_context"`
	Dockerfile     string `yaml:"dockerfile"`
	ManifestPath   string `yaml:"manifest_path"`
	Registry       string `yaml:"registry"`
	WebhookSecret  string `yaml:"webhook_secret"`
	DryRun         bool   `yaml:"dry_run"`
	BuildRoot      string `yaml:"build_root"`
	RolloutTimeout string `yaml:"rollout_timeout"`
	PollInterval   string `yaml:"poll_interval"`
}

type TelemetryConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Interval string `yaml:"interval"`
}

func Default() Config {
	return Config{
		NodeID:        "dev-agent",
		ListenAddr:    "127.0.0.1:9443",
		HealthAddr:    "127.0.0.1:9080",
		CloudEndpoint: "https://cloud.localhost",
		SQLitePath:    "./opsi-agent.sqlite",
		Deployment: DeploymentConfig{
			ProjectID:      "dev-project",
			ServiceID:      "example-app",
			ServiceName:    "example-app",
			ServiceType:    "backend",
			Branch:         "main",
			Namespace:      "default",
			BuildContext:   ".",
			Dockerfile:     "Dockerfile",
			BuildRoot:      "/tmp/opsi-builds",
			RolloutTimeout: "10m",
			PollInterval:   "5s",
		},
		Telemetry: TelemetryConfig{
			Enabled:  true,
			Interval: "15s",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.NodeID == "" {
		return errors.New("node_id is required")
	}
	if c.ListenAddr == "" {
		return errors.New("listen_addr is required")
	}
	if c.HealthAddr == "" {
		return errors.New("health_addr is required")
	}
	if c.TLS.RequireClientCert && c.TLS.CACertPath == "" {
		return errors.New("tls.ca_cert_path is required when client certificates are required")
	}
	if (c.TLS.ServerCertPath == "") != (c.TLS.ServerKeyPath == "") {
		return errors.New("tls.server_cert_path and tls.server_key_path must be configured together")
	}
	if c.Deployment.PollInterval != "" {
		if _, err := time.ParseDuration(c.Deployment.PollInterval); err != nil {
			return fmt.Errorf("deployment.poll_interval: %w", err)
		}
	}
	if c.Deployment.RolloutTimeout != "" {
		if _, err := time.ParseDuration(c.Deployment.RolloutTimeout); err != nil {
			return fmt.Errorf("deployment.rollout_timeout: %w", err)
		}
	}
	if c.Telemetry.Interval != "" {
		if _, err := time.ParseDuration(c.Telemetry.Interval); err != nil {
			return fmt.Errorf("telemetry.interval: %w", err)
		}
	}
	return nil
}

func (c TLSConfig) Enabled() bool {
	return c.ServerCertPath != "" || c.ServerKeyPath != "" || c.RequireClientCert
}
