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

var ErrRuntimeUnsupported = errors.New("bootstrap target mode unsupported")

type Config struct {
	CloudURL             string         `json:"cloud_url"`
	BootstrapWorkerToken string         `json:"bootstrap_worker_token"`
	SessionID            string         `json:"session_id"`
	AgentInstallURL      string         `json:"agent_install_url"`
	AgentInstallSHA256   string         `json:"agent_install_sha256"`
	SSHKnownHostsPath    string         `json:"ssh_known_hosts_path"`
	Production           bool           `json:"production"`
	Timeout              time.Duration  `json:"-"`
	HTTPClient           *http.Client   `json:"-"`
	Executor             RemoteExecutor `json:"-"`
}

type fileConfig struct {
	CloudURL             string `json:"cloud_url"`
	BootstrapWorkerToken string `json:"bootstrap_worker_token"`
	SessionID            string `json:"session_id"`
	AgentInstallURL      string `json:"agent_install_url"`
	AgentInstallSHA256   string `json:"agent_install_sha256"`
	SSHKnownHostsPath    string `json:"ssh_known_hosts_path"`
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
	cfg := Config{CloudURL: raw.CloudURL, BootstrapWorkerToken: raw.BootstrapWorkerToken, SessionID: raw.SessionID, AgentInstallURL: raw.AgentInstallURL, AgentInstallSHA256: raw.AgentInstallSHA256, SSHKnownHostsPath: raw.SSHKnownHostsPath, Production: raw.Production}
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
	if c.Production && c.SSHKnownHostsPath == "" {
		return errors.New("production requires ssh_known_hosts_path")
	}
	if c.Timeout < 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

func RunOnce(ctx context.Context, cfg Config) error {
	return NewInstaller(cfg).RunOnce(ctx)
}

type CloudClient interface {
	Take(context.Context) (Bundle, error)
	Progress(context.Context, string, string, string) error
	Finish(context.Context, string, string, string) error
	WaitForHeartbeat(context.Context, Bundle) error
}

type Installer struct {
	cfg      Config
	client   CloudClient
	executor RemoteExecutor
}

func NewInstaller(cfg Config) Installer {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	executor := cfg.Executor
	if executor == nil {
		executor = SSHExecutor{KnownHostsPath: cfg.SSHKnownHostsPath}
	}
	return Installer{cfg: cfg, client: httpCloudClient{client: httpClient, cfg: cfg}, executor: executor}
}

func (i Installer) RunOnce(ctx context.Context) error {
	cfg := i.cfg
	if err := cfg.Validate(); err != nil {
		return err
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	bundle, err := i.client.Take(ctx)
	if err != nil {
		return err
	}
	defer func() {
		bundle.AgentRegistrationToken = ""
		bundle.SSH.PrivateKey = ""
		bundle.SSH.Password = ""
	}()
	if err := ValidateBundle(bundle); err != nil {
		_ = i.client.Finish(context.WithoutCancel(ctx), bundle.ProjectID, "failed", "INVALID_BOOTSTRAP_TARGET: "+redactForBundle(bundle, err.Error()))
		return err
	}
	plan, err := BuildBootstrapPlan(cfg, bundle)
	if err != nil {
		_ = i.client.Finish(context.WithoutCancel(ctx), bundle.ProjectID, "failed", redactForBundle(bundle, err.Error()))
		return err
	}

	target := RemoteTarget{Host: bundle.PublicHost, Port: bundle.SSHPort, Username: bundle.SSH.Username, Password: bundle.SSH.Password}
	_ = i.client.Progress(ctx, bundle.ProjectID, "connecting", "connecting to target over SSH")
	session, err := i.executor.Connect(ctx, target)
	if err != nil {
		msg := "BOOTSTRAP_CONNECT_FAILED: " + redactForBundle(bundle, err.Error())
		_ = i.client.Finish(context.WithoutCancel(ctx), bundle.ProjectID, "failed", msg)
		return errors.New(msg)
	}
	defer session.Close()

	for _, step := range plan.Steps {
		_ = i.client.Progress(ctx, bundle.ProjectID, step.Status, step.Message)
		result, err := session.Run(ctx, step.Command)
		if err != nil || result.ExitCode != 0 {
			msg := redactForBundle(bundle, fmt.Sprintf("%s failed: %s %s %v", step.Status, result.Stdout, result.Stderr, err))
			_ = i.client.Finish(context.WithoutCancel(ctx), bundle.ProjectID, "failed", msg)
			return errors.New(msg)
		}
	}
	_ = i.client.Progress(ctx, bundle.ProjectID, "verifying_agent", "waiting for healthy Agent heartbeat")
	if err := i.client.WaitForHeartbeat(ctx, bundle); err != nil {
		msg := "BOOTSTRAP_HEARTBEAT_VERIFY_FAILED: " + redactForBundle(bundle, err.Error())
		_ = i.client.Finish(context.WithoutCancel(ctx), bundle.ProjectID, "failed", msg)
		return errors.New(msg)
	}
	return i.client.Finish(context.WithoutCancel(ctx), bundle.ProjectID, "completed", "bootstrap completed after verified Agent heartbeat")
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
		return fmt.Errorf("%w: ssh private key bootstrap is not supported by Cloud credential handoff", ErrRuntimeUnsupported)
	default:
		return errors.New("ssh auth_method is invalid")
	}
	if bundle.AgentRegistrationToken == "" {
		return errors.New("agent registration token is missing")
	}
	return nil
}

type BootstrapPlan struct {
	Steps []BootstrapStep
}

type BootstrapStep struct {
	Status  string
	Message string
	Command CommandSpec
}

func BuildBootstrapPlan(cfg Config, bundle Bundle) (BootstrapPlan, error) {
	if bundle.Role != "" && bundle.Role != "first_server" {
		return BootstrapPlan{}, fmt.Errorf("%w: only first_server bootstrap is implemented", ErrRuntimeUnsupported)
	}
	if cfg.AgentInstallURL == "" {
		return BootstrapPlan{}, errors.New("agent_install_url is required for bootstrap runtime install")
	}
	u, err := url.Parse(cfg.AgentInstallURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return BootstrapPlan{}, errors.New("agent_install_url must be an absolute URL")
	}
	if cfg.Production && u.Scheme != "https" {
		return BootstrapPlan{}, errors.New("production requires https agent_install_url")
	}
	env := map[string]string{
		"OPSI_CLOUD_URL":       strings.TrimRight(cfg.CloudURL, "/"),
		"OPSI_NODE_ID":         bundle.NodeID,
		"OPSI_PROJECT_ID":      bundle.ProjectID,
		"OPSI_AGENT_URL":       cfg.AgentInstallURL,
		"OPSI_AGENT_SHA256":    cfg.AgentInstallSHA256,
		"OPSI_REMOTE_USERNAME": bundle.SSH.Username,
	}
	secretEnv := map[string]string{"OPSI_AGENT_REGISTRATION_TOKEN": bundle.AgentRegistrationToken}
	return BootstrapPlan{Steps: []BootstrapStep{
		{Status: "preflight", Message: "checking Ubuntu target prerequisites", Command: CommandSpec{Script: preflightScript, Env: env}},
		{Status: "installing_k3s", Message: "installing K3s server", Command: CommandSpec{Script: installK3sScript, Env: env}},
		{Status: "installing_agent", Message: "installing Opsi Agent binary", Command: CommandSpec{Script: installAgentScript, Env: env}},
		{Status: "registering_agent", Message: "registering and starting Opsi Agent", Command: CommandSpec{Script: registerAgentScript, Env: env, SensitiveEnv: secretEnv}},
	}}, nil
}

const preflightScript = `
set -eu
. /etc/os-release
test "${ID:-}" = ubuntu
command -v curl >/dev/null
command -v systemctl >/dev/null
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then command -v sudo >/dev/null && sudo -n true; fi
`

const installK3sScript = `
set -eu
SUDO=""
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then SUDO="sudo -n"; fi
if ! command -v k3s >/dev/null 2>&1; then
	if [ "${OPSI_REMOTE_USERNAME}" = root ]; then
		curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='server --write-kubeconfig-mode 0640' sh -
	else
		curl -sfL https://get.k3s.io | sudo -n env INSTALL_K3S_EXEC='server --write-kubeconfig-mode 0640' sh -
	fi
fi
$SUDO systemctl enable --now k3s
$SUDO k3s kubectl get nodes
`

const installAgentScript = `
set -eu
SUDO=""
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then SUDO="sudo -n"; fi
$SUDO install -d -m 0755 /opt/opsi/bin
tmp="$(mktemp)"
curl -fsSL "${OPSI_AGENT_URL}" -o "$tmp"
if [ -n "${OPSI_AGENT_SHA256}" ]; then echo "${OPSI_AGENT_SHA256}  $tmp" | sha256sum -c -; fi
$SUDO install -m 0755 "$tmp" /opt/opsi/bin/opsi-agent
rm -f "$tmp"
`

const registerAgentScript = `
set -eu
SUDO=""
if [ "${OPSI_REMOTE_USERNAME}" != root ]; then SUDO="sudo -n"; fi
fingerprint="bootstrap-${OPSI_NODE_ID}"
payload=$(printf '{"registration_token":"%s","public_key_fingerprint":"%s","version":"bootstrap","capabilities":{"deploy":true,"node_lifecycle":true}}' "$OPSI_AGENT_REGISTRATION_TOKEN" "$fingerprint")
response=$(curl -fsS -X POST "${OPSI_CLOUD_URL}/v1/agents/register" -H 'content-type: application/json' --data "$payload")
agent_token=$(printf '%s' "$response" | sed -n 's/.*"agent_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
test -n "$agent_token"
$SUDO install -d -m 0750 /etc/opsi /var/lib/opsi
$SUDO tee /etc/opsi/agent.yaml >/dev/null <<EOF
node_id: ${OPSI_NODE_ID}
mode: dev
listen_addr: 127.0.0.1:9443
health_addr: 127.0.0.1:9080
cloud_endpoint: ${OPSI_CLOUD_URL}
sqlite_path: /var/lib/opsi/opsi-agent.sqlite
auth:
  enabled: false
cloud_relay:
  enabled: true
  project_id: ${OPSI_PROJECT_ID}
  agent_token: ${agent_token}
  poll_interval: 2s
  long_poll_wait: 30s
  heartbeat_interval: 10s
  sign_requests: true
deployment:
  project_id: ${OPSI_PROJECT_ID}
  builder_mode: containerd
  build_root: /tmp/opsi-builds
telemetry:
  enabled: true
secret:
  namespace: default
  kubectl_path: kubectl
  totp_namespace: default
  encryption_at_rest_confirmed: false
EOF
$SUDO chmod 0600 /etc/opsi/agent.yaml
$SUDO tee /etc/systemd/system/opsi-agent.service >/dev/null <<EOF
[Unit]
Description=Opsi Agent
After=network-online.target k3s.service
Wants=network-online.target

[Service]
ExecStart=/opt/opsi/bin/opsi-agent --config /etc/opsi/agent.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
$SUDO systemctl daemon-reload
$SUDO systemctl enable --now opsi-agent
`

type httpCloudClient struct {
	client *http.Client
	cfg    Config
}

func (h httpCloudClient) Take(ctx context.Context) (Bundle, error) {
	return take(ctx, h.client, h.cfg)
}

func (h httpCloudClient) Progress(ctx context.Context, projectID, status, message string) error {
	return progress(ctx, h.client, h.cfg, projectID, status, message)
}

func (h httpCloudClient) Finish(ctx context.Context, projectID, status, message string) error {
	return finish(ctx, h.client, h.cfg, projectID, status, message)
}

func (h httpCloudClient) WaitForHeartbeat(ctx context.Context, bundle Bundle) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		status, err := bootstrapStatus(ctx, h.client, h.cfg, bundle.ProjectID)
		if err != nil {
			return err
		}
		switch status {
		case "verifying":
			return nil
		case "failed", "cancelled", "expired":
			return fmt.Errorf("bootstrap session became %s before heartbeat verification", status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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

func progress(ctx context.Context, client *http.Client, cfg Config, projectID, status, message string) error {
	return postWorkerState(ctx, client, cfg, "progress", projectID, status, message)
}

func finish(ctx context.Context, client *http.Client, cfg Config, projectID, status, message string) error {
	return postWorkerState(ctx, client, cfg, "finish", projectID, status, message)
}

func postWorkerState(ctx context.Context, client *http.Client, cfg Config, action, projectID, status, message string) error {
	body, _ := json.Marshal(map[string]string{"project_id": projectID, "status": status, "message": registry.RedactString(message)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.CloudURL, "/")+"/internal/bootstrap/sessions/"+url.PathEscape(cfg.SessionID)+"/"+action, bytes.NewReader(body))
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
		return fmt.Errorf("%s bootstrap session: %s", action, safeBody(resp.Body))
	}
	return nil
}

func bootstrapStatus(ctx context.Context, client *http.Client, cfg Config, projectID string) (string, error) {
	endpoint := strings.TrimRight(cfg.CloudURL, "/") + "/internal/bootstrap/sessions/" + url.PathEscape(cfg.SessionID) + "/status?project_id=" + url.QueryEscape(projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Bootstrap-Worker-Token", cfg.BootstrapWorkerToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get bootstrap session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get bootstrap session: %s", safeBody(resp.Body))
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Status, nil
}

func safeBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4096))
	return registry.RedactString(string(data))
}

func redactForBundle(bundle Bundle, value string) string {
	value = registry.RedactString(value)
	for _, secret := range []string{bundle.SSH.Password, bundle.SSH.PrivateKey, bundle.AgentRegistrationToken} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
