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
	Auth          AuthConfig       `yaml:"auth"`
	CloudRelay    CloudRelayConfig `yaml:"cloud_relay"`
	Deployment    DeploymentConfig `yaml:"deployment"`
	Telemetry     TelemetryConfig  `yaml:"telemetry"`
	Secret        SecretConfig     `yaml:"secret"`
}

type TLSConfig struct {
	CACertPath        string `yaml:"ca_cert_path"`
	ServerCertPath    string `yaml:"server_cert_path"`
	ServerKeyPath     string `yaml:"server_key_path"`
	RequireClientCert bool   `yaml:"require_client_cert"`
}

type AuthConfig struct {
	Enabled        bool   `yaml:"enabled"`
	VerifyCacheTTL string `yaml:"verify_cache_ttl"`
}

type CloudRelayConfig struct {
	Enabled           bool   `yaml:"enabled"`
	ProjectID         string `yaml:"project_id"`
	AgentToken        string `yaml:"agent_token"`
	PollInterval      string `yaml:"poll_interval"`
	LongPollWait      string `yaml:"long_poll_wait"`
	HeartbeatInterval string `yaml:"heartbeat_interval"`
	SignRequests      bool   `yaml:"sign_requests"`
}

type DeploymentConfig struct {
	ProjectID                     string            `yaml:"project_id"`
	ServiceID                     string            `yaml:"service_id"`
	ServiceName                   string            `yaml:"service_name"`
	ServiceType                   string            `yaml:"service_type"`
	RepoURL                       string            `yaml:"repo_url"`
	Branch                        string            `yaml:"branch"`
	Namespace                     string            `yaml:"namespace"`
	BuildContext                  string            `yaml:"build_context"`
	Dockerfile                    string            `yaml:"dockerfile"`
	ManifestPath                  string            `yaml:"manifest_path"`
	PublicEndpoint                string            `yaml:"public_endpoint"`
	WatchPaths                    []string          `yaml:"watch_paths"`
	TerminationGracePeriodSeconds int               `yaml:"termination_grace_period_seconds"`
	ResourceRequests              map[string]string `yaml:"resource_requests"`
	ResourceLimits                map[string]string `yaml:"resource_limits"`
	IngressEnabled                bool              `yaml:"ingress_enabled"`
	Registry                      string            `yaml:"registry"`
	BuilderMode                   string            `yaml:"builder_mode"`
	NerdctlPath                   string            `yaml:"nerdctl_path"`
	ContainerdNS                  string            `yaml:"containerd_namespace"`
	WebhookSecret                 string            `yaml:"webhook_secret"`
	DryRun                        bool              `yaml:"dry_run"`
	BuildRoot                     string            `yaml:"build_root"`
	RolloutTimeout                string            `yaml:"rollout_timeout"`
	PollInterval                  string            `yaml:"poll_interval"`
}

type TelemetryConfig struct {
	Enabled                bool   `yaml:"enabled"`
	Interval               string `yaml:"interval"`
	KubectlPath            string `yaml:"kubectl_path"`
	CAdvisorEndpoint       string `yaml:"cadvisor_endpoint"`
	CAdvisorTimeout        string `yaml:"cadvisor_timeout"`
	MaxRecordsPerTick      int    `yaml:"max_records_per_tick"`
	PodLogTail             int    `yaml:"pod_log_tail"`
	PodLogSince            string `yaml:"pod_log_since"`
	ExternalHealthInterval string `yaml:"external_health_interval"`
}

type SecretConfig struct {
	Namespace                 string `yaml:"namespace"`
	KubectlPath               string `yaml:"kubectl_path"`
	TOTPNamespace             string `yaml:"totp_namespace"`
	RolloutRestartOnRotate    bool   `yaml:"rollout_restart_on_rotate"`
	EncryptionAtRestConfirmed bool   `yaml:"encryption_at_rest_confirmed"`
	CloudOTPTimeout           string `yaml:"cloud_otp_timeout"`
}

func Default() Config {
	return Config{
		NodeID:        "dev-agent",
		ListenAddr:    "127.0.0.1:9443",
		HealthAddr:    "127.0.0.1:9080",
		CloudEndpoint: "https://cloud.localhost",
		SQLitePath:    "./opsi-agent.sqlite",
		Auth:          AuthConfig{Enabled: false, VerifyCacheTTL: "15m"},
		CloudRelay:    CloudRelayConfig{Enabled: false, PollInterval: "2s", LongPollWait: "30s", HeartbeatInterval: "30s", SignRequests: true},
		Deployment: DeploymentConfig{
			ProjectID:                     "dev-project",
			ServiceID:                     "example-app",
			ServiceName:                   "example-app",
			ServiceType:                   "backend",
			Branch:                        "main",
			Namespace:                     "default",
			BuildContext:                  ".",
			Dockerfile:                    "Dockerfile",
			PublicEndpoint:                "",
			WatchPaths:                    []string{"**"},
			TerminationGracePeriodSeconds: 30,
			ResourceRequests:              map[string]string{"cpu": "100m", "memory": "128Mi"},
			ResourceLimits:                map[string]string{"cpu": "500m", "memory": "512Mi"},
			IngressEnabled:                true,
			BuilderMode:                   "containerd",
			NerdctlPath:                   "nerdctl",
			ContainerdNS:                  "k8s.io",
			BuildRoot:                     "/tmp/opsi-builds",
			RolloutTimeout:                "10m",
			PollInterval:                  "5s",
		},
		Telemetry: TelemetryConfig{
			Enabled:                true,
			Interval:               "15s",
			KubectlPath:            "kubectl",
			CAdvisorTimeout:        "5s",
			MaxRecordsPerTick:      1000,
			PodLogTail:             50,
			PodLogSince:            "1m",
			ExternalHealthInterval: "60s",
		},
		Secret: SecretConfig{
			Namespace:                 "default",
			KubectlPath:               "kubectl",
			TOTPNamespace:             "default",
			RolloutRestartOnRotate:    true,
			EncryptionAtRestConfirmed: false,
			CloudOTPTimeout:           "10s",
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
	if c.Auth.VerifyCacheTTL != "" {
		if _, err := time.ParseDuration(c.Auth.VerifyCacheTTL); err != nil {
			return fmt.Errorf("auth.verify_cache_ttl: %w", err)
		}
	}
	if c.CloudRelay.Enabled {
		if c.CloudEndpoint == "" {
			return errors.New("cloud_endpoint is required when cloud_relay.enabled=true")
		}
		if c.CloudRelay.ProjectID == "" && c.Deployment.ProjectID == "" {
			return errors.New("cloud_relay.project_id or deployment.project_id is required when cloud_relay.enabled=true")
		}
		if c.CloudRelay.AgentToken == "" {
			return errors.New("cloud_relay.agent_token is required when cloud_relay.enabled=true")
		}
	}
	for field, value := range map[string]string{
		"cloud_relay.poll_interval":      c.CloudRelay.PollInterval,
		"cloud_relay.long_poll_wait":     c.CloudRelay.LongPollWait,
		"cloud_relay.heartbeat_interval": c.CloudRelay.HeartbeatInterval,
	} {
		if value != "" {
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("%s: %w", field, err)
			}
		}
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
	if c.Deployment.TerminationGracePeriodSeconds < 0 {
		return errors.New("deployment.termination_grace_period_seconds must be >= 0")
	}
	switch c.Deployment.BuilderMode {
	case "", "containerd", "docker", "dry_run":
	default:
		return fmt.Errorf("deployment.builder_mode must be containerd, docker, or dry_run")
	}
	if c.Telemetry.Interval != "" {
		if _, err := time.ParseDuration(c.Telemetry.Interval); err != nil {
			return fmt.Errorf("telemetry.interval: %w", err)
		}
	}
	if c.Telemetry.CAdvisorTimeout != "" {
		if _, err := time.ParseDuration(c.Telemetry.CAdvisorTimeout); err != nil {
			return fmt.Errorf("telemetry.cadvisor_timeout: %w", err)
		}
	}
	if c.Telemetry.PodLogSince != "" {
		if _, err := time.ParseDuration(c.Telemetry.PodLogSince); err != nil {
			return fmt.Errorf("telemetry.pod_log_since: %w", err)
		}
	}
	if c.Telemetry.ExternalHealthInterval != "" {
		if _, err := time.ParseDuration(c.Telemetry.ExternalHealthInterval); err != nil {
			return fmt.Errorf("telemetry.external_health_interval: %w", err)
		}
	}
	if c.Secret.CloudOTPTimeout != "" {
		if _, err := time.ParseDuration(c.Secret.CloudOTPTimeout); err != nil {
			return fmt.Errorf("secret.cloud_otp_timeout: %w", err)
		}
	}
	return nil
}

func (c TLSConfig) Enabled() bool {
	return c.ServerCertPath != "" || c.ServerKeyPath != "" || c.RequireClientCert
}
