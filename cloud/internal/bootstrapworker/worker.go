package bootstrapworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

var ErrRuntimeUnsupported = errors.New("bootstrap runtime installer unsupported")

type Config struct {
	CloudURL             string        `json:"cloud_url"`
	BootstrapWorkerToken string        `json:"bootstrap_worker_token"`
	SessionID            string        `json:"session_id"`
	Production           bool          `json:"production"`
	Timeout              time.Duration `json:"-"`
	HTTPClient           *http.Client  `json:"-"`
}

type fileConfig struct {
	CloudURL             string `json:"cloud_url"`
	BootstrapWorkerToken string `json:"bootstrap_worker_token"`
	SessionID            string `json:"session_id"`
	Production           bool   `json:"production"`
	Timeout              string `json:"timeout"`
}

type Bundle struct {
	SessionID                string    `json:"session_id"`
	ProjectID                string    `json:"project_id"`
	NodeID                   string    `json:"node_id"`
	PublicHost               string    `json:"public_host"`
	SSHPort                  int       `json:"ssh_port"`
	Role                     string    `json:"role"`
	AgentRegistrationToken   string    `json:"agent_registration_token"`
	AgentRegistrationExpires time.Time `json:"agent_registration_expires"`
	SSH                      struct {
		AuthMethod string `json:"auth_method"`
		Username   string `json:"username"`
		PrivateKey string `json:"private_key"`
		Password   string `json:"password"`
	} `json:"ssh"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read bootstrap worker config: %w", err)
	}
	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse bootstrap worker config: %w", err)
	}
	cfg := Config{CloudURL: raw.CloudURL, BootstrapWorkerToken: raw.BootstrapWorkerToken, SessionID: raw.SessionID, Production: raw.Production}
	if raw.Timeout != "" {
		cfg.Timeout, err = time.ParseDuration(raw.Timeout)
		if err != nil {
			return Config{}, fmt.Errorf("parse timeout: %w", err)
		}
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.CloudURL == "" {
		return errors.New("cloud_url is required")
	}
	u, err := url.Parse(c.CloudURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("cloud_url must be an absolute URL")
	}
	if c.Production && u.Scheme != "https" {
		return errors.New("production requires https cloud_url")
	}
	if c.BootstrapWorkerToken == "" {
		return errors.New("bootstrap_worker_token is required")
	}
	if c.Production && len(c.BootstrapWorkerToken) < 32 {
		return errors.New("production requires bootstrap_worker_token with at least 32 bytes")
	}
	if c.SessionID == "" {
		return errors.New("session_id is required")
	}
	if c.Timeout < 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

func RunOnce(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	bundle, err := take(ctx, client, cfg)
	if err != nil {
		return err
	}
	defer func() {
		bundle.AgentRegistrationToken = ""
		bundle.SSH.PrivateKey = ""
		bundle.SSH.Password = ""
	}()
	if err := ValidateBundle(bundle); err != nil {
		_ = finish(ctx, client, cfg, bundle.ProjectID, "failed", "INVALID_BOOTSTRAP_TARGET: "+err.Error())
		return err
	}
	msg := "BOOTSTRAP_RUNTIME_UNSUPPORTED: SSH/K3s/Agent installer is not implemented; no target changes were made"
	if err := finish(ctx, client, cfg, bundle.ProjectID, "failed", msg); err != nil {
		return err
	}
	return ErrRuntimeUnsupported
}

func ValidateBundle(bundle Bundle) error {
	if bundle.SessionID == "" || bundle.ProjectID == "" || bundle.NodeID == "" {
		return errors.New("bootstrap bundle is missing required ids")
	}
	if bundle.PublicHost == "" {
		return errors.New("public_host is required")
	}
	if bundle.SSHPort < 0 || bundle.SSHPort > 65535 {
		return errors.New("ssh_port is invalid")
	}
	if bundle.SSH.Username == "" {
		return errors.New("ssh username is required")
	}
	switch bundle.SSH.AuthMethod {
	case "password":
		if bundle.SSH.Password == "" {
			return errors.New("ssh password credential is missing")
		}
	case "private_key":
		if bundle.SSH.PrivateKey == "" {
			return errors.New("ssh private key credential is missing")
		}
	default:
		return errors.New("ssh auth_method is invalid")
	}
	if bundle.AgentRegistrationToken == "" {
		return errors.New("agent registration token is missing")
	}
	return nil
}

func take(ctx context.Context, client *http.Client, cfg Config) (Bundle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.CloudURL, "/")+"/internal/bootstrap/sessions/"+url.PathEscape(cfg.SessionID)+"/take", nil)
	if err != nil {
		return Bundle{}, err
	}
	req.Header.Set("X-Bootstrap-Worker-Token", cfg.BootstrapWorkerToken)
	resp, err := client.Do(req)
	if err != nil {
		return Bundle{}, fmt.Errorf("take bootstrap session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Bundle{}, fmt.Errorf("take bootstrap session: %s", safeBody(resp.Body))
	}
	var bundle Bundle
	if err := json.NewDecoder(resp.Body).Decode(&bundle); err != nil {
		return Bundle{}, fmt.Errorf("decode bootstrap bundle: %w", err)
	}
	return bundle, nil
}

func finish(ctx context.Context, client *http.Client, cfg Config, projectID, status, message string) error {
	body, _ := json.Marshal(map[string]string{"project_id": projectID, "status": status, "message": registry.RedactString(message)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.CloudURL, "/")+"/internal/bootstrap/sessions/"+url.PathEscape(cfg.SessionID)+"/finish", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Worker-Token", cfg.BootstrapWorkerToken)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("finish bootstrap session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("finish bootstrap session: %s", safeBody(resp.Body))
	}
	return nil
}

func safeBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4096))
	return registry.RedactString(string(data))
}
