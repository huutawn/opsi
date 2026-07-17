package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	AgentAddr     string    `yaml:"agent_addr"`
	CloudURL      string    `yaml:"cloud_url"`
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
	return Config{AgentAddr: "127.0.0.1:9443", CloudURL: "http://127.0.0.1:9800"}
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
	if c.CloudURL == "" {
		return errors.New("cloud_url is required")
	}
	if (c.TLS.ClientCertPath == "") != (c.TLS.ClientKeyPath == "") {
		return errors.New("tls.client_cert_path and tls.client_key_path must be configured together")
	}
	if !isLoopbackAddress(c.AgentAddr) {
		if c.TLS.PinnedServerCertSHA256 == "" {
			return errors.New("non-loopback agent_addr requires tls.pinned_server_cert_sha256")
		}
		if c.TLS.ServerName == "" {
			return errors.New("non-loopback agent_addr requires tls.server_name")
		}
	}
	return nil
}

func (c TLSConfig) Enabled() bool {
	return c.CACertPath != "" || c.ClientCertPath != "" || c.ClientKeyPath != "" || c.PinnedServerCertSHA256 != ""
}

// Save atomically persists only non-secret endpoint metadata resolved from Cloud.
func Save(path string, cfg Config) error {
	if path == "" {
		return errors.New("config path is required to save Agent connection metadata")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".opsi-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
