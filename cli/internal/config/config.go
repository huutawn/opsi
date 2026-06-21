package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	AgentAddr     string    `yaml:"agent_addr"`
	SyncStatePath string    `yaml:"sync_state_path"`
	TLS           TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
	CACertPath             string `yaml:"ca_cert_path"`
	ClientCertPath         string `yaml:"client_cert_path"`
	ClientKeyPath          string `yaml:"client_key_path"`
	PinnedServerCertSHA256 string `yaml:"pinned_server_cert_sha256"`
	ServerName             string `yaml:"server_name"`
}

func Default() Config {
	return Config{AgentAddr: "127.0.0.1:9443"}
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
	if c.AgentAddr == "" {
		return errors.New("agent_addr is required")
	}
	if (c.TLS.ClientCertPath == "") != (c.TLS.ClientKeyPath == "") {
		return errors.New("tls.client_cert_path and tls.client_key_path must be configured together")
	}
	return nil
}

func (c TLSConfig) Enabled() bool {
	return c.CACertPath != "" || c.ClientCertPath != "" || c.ClientKeyPath != "" || c.PinnedServerCertSHA256 != ""
}
