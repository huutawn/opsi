package bootstrapworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

var ErrRuntimeUnsupported = errors.New("bootstrap target mode unsupported")

const (
	defaultPollInterval = 3 * time.Second
	minPollInterval     = 500 * time.Millisecond
	maxPollInterval     = 5 * time.Minute
	defaultJobTimeout   = 10 * time.Minute
)

type Config struct {
	CloudURL             string         `json:"cloud_url"`
	BootstrapWorkerToken string         `json:"bootstrap_worker_token"`
	WorkerID             string         `json:"worker_id"`
	PollInterval         time.Duration  `json:"-"`
	AgentInstallURL      string         `json:"agent_install_url"`
	AgentInstallSHA256   string         `json:"agent_install_sha256"`
	SSHKnownHostsPath    string         `json:"ssh_known_hosts_path"`
	Production           bool           `json:"production"`
	Timeout              time.Duration  `json:"-"`
	HTTPClient           *http.Client   `json:"-"`
	Executor             RemoteExecutor `json:"-"`
	Logger               *slog.Logger   `json:"-"`
}

type fileConfig struct {
	CloudURL             string `json:"cloud_url"`
	BootstrapWorkerToken string `json:"bootstrap_worker_token"`
	WorkerID             string `json:"worker_id"`
	PollInterval         string `json:"poll_interval"`
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

type Lease struct {
	Bundle         Bundle    `json:"bundle"`
	LeaseToken     string    `json:"lease_token"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err == nil {
		if _, ok := fields["session_id"]; ok {
			return Config{}, errors.New("session_id is no longer supported; bootstrap workers lease sessions automatically")
		}
	}
	cfg := Config{CloudURL: raw.CloudURL, BootstrapWorkerToken: raw.BootstrapWorkerToken, WorkerID: strings.TrimSpace(raw.WorkerID), AgentInstallURL: raw.AgentInstallURL, AgentInstallSHA256: raw.AgentInstallSHA256, SSHKnownHostsPath: raw.SSHKnownHostsPath, Production: raw.Production, PollInterval: defaultPollInterval}
	if raw.PollInterval != "" {
		cfg.PollInterval, err = time.ParseDuration(raw.PollInterval)
		if err != nil {
			return Config{}, fmt.Errorf("parse poll_interval: %w", err)
		}
		if cfg.PollInterval <= 0 {
			return Config{}, errors.New("poll_interval must be positive")
		}
	}
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
	if err := registry.ValidateBootstrapWorkerID(strings.TrimSpace(c.WorkerID)); err != nil {
		return err
	}
	if c.PollInterval == 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.PollInterval < minPollInterval || c.PollInterval > maxPollInterval {
		return fmt.Errorf("poll_interval must be between %s and %s", minPollInterval, maxPollInterval)
	}
	if c.Production && c.SSHKnownHostsPath == "" {
		return errors.New("production requires ssh_known_hosts_path")
	}
	if c.Timeout < 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

func Run(ctx context.Context, cfg Config) error {
	return NewWorker(cfg).Run(ctx)
}

type CloudClient interface {
	LeaseNext(context.Context) (Lease, bool, error)
	Progress(context.Context, Lease, string, string) error
	Finish(context.Context, Lease, string, string) error
	WaitForHeartbeat(context.Context, Lease) error
}

type Worker struct {
	cfg      Config
	client   CloudClient
	executor RemoteExecutor
	logger   *slog.Logger
}

func NewWorker(cfg Config) Worker {
	cfg.WorkerID = strings.TrimSpace(cfg.WorkerID)
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	executor := cfg.Executor
	if executor == nil {
		executor = SSHExecutor{KnownHostsPath: cfg.SSHKnownHostsPath}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return Worker{cfg: cfg, client: httpCloudClient{client: httpClient, cfg: cfg}, executor: executor, logger: logger}
}

func (w Worker) Run(ctx context.Context) error {
	cfg := w.cfg
	cfg.WorkerID = strings.TrimSpace(cfg.WorkerID)
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	w.cfg = cfg
	for {
		if ctx.Err() != nil {
			return nil
		}
		lease, found, err := w.client.LeaseNext(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if isFatalCloudError(err) {
				return err
			}
			w.logger.Warn("bootstrap lease poll failed", "worker_id", cfg.WorkerID, "error", redactForConfig(cfg, "", err.Error()))
			if !waitForPoll(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}
		if !found {
			if !waitForPoll(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}
		if lease.LeaseToken == "" || lease.LeaseExpiresAt.IsZero() {
			return errors.New("invalid bootstrap lease protocol response")
		}
		if err := w.processLease(ctx, lease); err != nil {
			if isFatalCloudError(err) {
				return err
			}
			w.logger.Warn("bootstrap session failed", "worker_id", cfg.WorkerID, "session_id", lease.Bundle.SessionID, "project_id", lease.Bundle.ProjectID, "error", redactForLease(cfg, lease, err.Error()))
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (w Worker) processLease(parent context.Context, lease Lease) error {
	cfg := w.cfg
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultJobTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	bundle := lease.Bundle
	defer func() {
		bundle.AgentRegistrationToken = ""
		bundle.SSH.PrivateKey = ""
		bundle.SSH.Password = ""
	}()
	if err := ValidateBundle(bundle); err != nil {
		_ = w.finishLease(ctx, lease, "failed", "INVALID_BOOTSTRAP_TARGET: "+redactForBundle(bundle, err.Error()))
		return err
	}
	plan, err := BuildBootstrapPlan(cfg, bundle)
	if err != nil {
		_ = w.finishLease(ctx, lease, "failed", redactForBundle(bundle, err.Error()))
		return err
	}

	target := RemoteTarget{Host: bundle.PublicHost, Port: bundle.SSHPort, Username: bundle.SSH.Username, Password: bundle.SSH.Password}
	if err := w.client.Progress(ctx, lease, "connecting", "connecting to target over SSH"); isFatalCloudError(err) {
		return err
	}
	session, err := w.executor.Connect(ctx, target)
	if err != nil {
		if parent.Err() != nil {
			_ = w.finishLease(ctx, lease, "failed", "BOOTSTRAP_WORKER_SHUTDOWN")
			return errors.New("BOOTSTRAP_WORKER_SHUTDOWN")
		}
		msg := "BOOTSTRAP_CONNECT_FAILED: " + redactForLease(cfg, lease, err.Error())
		_ = w.finishLease(ctx, lease, "failed", msg)
		return errors.New(msg)
	}
	defer session.Close()

	for _, step := range plan.Steps {
		if err := w.client.Progress(ctx, lease, step.Status, step.Message); isFatalCloudError(err) {
			return err
		}
		result, err := session.Run(ctx, step.Command)
		if err != nil || result.ExitCode != 0 {
			if parent.Err() != nil {
				_ = w.finishLease(ctx, lease, "failed", "BOOTSTRAP_WORKER_SHUTDOWN")
				return errors.New("BOOTSTRAP_WORKER_SHUTDOWN")
			}
			msg := redactForLease(cfg, lease, fmt.Sprintf("%s failed: %s %s %v", step.Status, result.Stdout, result.Stderr, err))
			_ = w.finishLease(ctx, lease, "failed", msg)
			return errors.New(msg)
		}
	}
	if err := w.client.Progress(ctx, lease, "verifying_agent", "waiting for healthy Agent heartbeat"); isFatalCloudError(err) {
		return err
	}
	if err := w.client.WaitForHeartbeat(ctx, lease); err != nil {
		if isFatalCloudError(err) {
			return err
		}
		if parent.Err() != nil {
			_ = w.finishLease(ctx, lease, "failed", "BOOTSTRAP_WORKER_SHUTDOWN")
			return errors.New("BOOTSTRAP_WORKER_SHUTDOWN")
		}
		msg := "BOOTSTRAP_HEARTBEAT_VERIFY_FAILED: " + redactForLease(cfg, lease, err.Error())
		_ = w.finishLease(ctx, lease, "failed", msg)
		return errors.New(msg)
	}
	return w.finishLease(ctx, lease, "completed", "bootstrap completed after verified Agent heartbeat")
}

func (w Worker) finishLease(ctx context.Context, lease Lease, status, message string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return w.client.Finish(cleanupCtx, lease, status, redactForLease(w.cfg, lease, message))
}

func waitForPoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
