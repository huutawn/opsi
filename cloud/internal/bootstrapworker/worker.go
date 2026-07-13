package bootstrapworker

import (
	"context"
	"encoding/hex"
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
	defaultPollInterval           = 3 * time.Second
	minPollInterval               = 500 * time.Millisecond
	maxPollInterval               = 5 * time.Minute
	defaultJobTimeout             = 10 * time.Minute
	defaultHeartbeatInterval      = 25 * time.Second
	defaultHeartbeatRetryInterval = 2 * time.Second
	defaultLeaseSafetyMargin      = 10 * time.Second
	defaultDaemonBackoff          = time.Second
	maximumDaemonBackoff          = 30 * time.Second
)

type Config struct {
	CloudURL               string           `json:"cloud_url"`
	AgentCloudURL          string           `json:"agent_cloud_url"`
	BootstrapWorkerToken   string           `json:"bootstrap_worker_token"`
	WorkerID               string           `json:"worker_id"`
	PollInterval           time.Duration    `json:"-"`
	AgentInstallURL        string           `json:"agent_install_url"`
	AgentInstallSHA256     string           `json:"agent_install_sha256"`
	SSHKnownHostsPath      string           `json:"ssh_known_hosts_path"`
	Production             bool             `json:"production"`
	Timeout                time.Duration    `json:"-"`
	HTTPClient             *http.Client     `json:"-"`
	Executor               RemoteExecutor   `json:"-"`
	Logger                 *slog.Logger     `json:"-"`
	HeartbeatInterval      time.Duration    `json:"-"`
	HeartbeatRetryInterval time.Duration    `json:"-"`
	LeaseSafetyMargin      time.Duration    `json:"-"`
	Now                    func() time.Time `json:"-"`
}

type fileConfig struct {
	CloudURL             string `json:"cloud_url"`
	AgentCloudURL        string `json:"agent_cloud_url"`
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

type JobFailure struct {
	Code      string
	Message   string
	Retryable bool
}

type FinishResult struct {
	Status  string
	Message string
	Failure *JobFailure
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
	cfg := Config{CloudURL: raw.CloudURL, AgentCloudURL: raw.AgentCloudURL, BootstrapWorkerToken: raw.BootstrapWorkerToken, WorkerID: strings.TrimSpace(raw.WorkerID), AgentInstallURL: raw.AgentInstallURL, AgentInstallSHA256: raw.AgentInstallSHA256, SSHKnownHostsPath: raw.SSHKnownHostsPath, Production: raw.Production, PollInterval: defaultPollInterval}
	if cfg.AgentCloudURL == "" {
		cfg.AgentCloudURL = cfg.CloudURL
	}
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
	u, err := parseHTTPURL(c.CloudURL)
	if err != nil {
		return fmt.Errorf("cloud_url: %w", err)
	}
	if c.Production && u.Scheme != "https" {
		return errors.New("production requires https cloud_url")
	}
	agentCloudURL := c.AgentCloudURL
	if agentCloudURL == "" {
		agentCloudURL = c.CloudURL
	}
	agentURL, err := parseHTTPURL(agentCloudURL)
	if err != nil {
		return fmt.Errorf("agent_cloud_url: %w", err)
	}
	if c.Production && agentURL.Scheme != "https" {
		return errors.New("production requires https agent_cloud_url")
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
	if c.SSHKnownHostsPath != "" {
		info, err := os.Stat(c.SSHKnownHostsPath)
		if err != nil {
			return fmt.Errorf("ssh_known_hosts_path: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("ssh_known_hosts_path must be a regular file")
		}
	}
	installURL, err := parseHTTPURL(c.AgentInstallURL)
	if err != nil {
		return fmt.Errorf("agent_install_url: %w", err)
	}
	if c.Production && installURL.Scheme != "https" {
		return errors.New("production requires https agent_install_url")
	}
	if err := validateSHA256(c.AgentInstallSHA256); err != nil {
		return fmt.Errorf("agent_install_sha256: %w", err)
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
	HeartbeatLease(context.Context, Lease) (time.Time, error)
	Progress(context.Context, Lease, string, string) error
	Finish(context.Context, Lease, FinishResult) error
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
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}
	if cfg.HeartbeatRetryInterval == 0 {
		cfg.HeartbeatRetryInterval = defaultHeartbeatRetryInterval
	}
	if cfg.LeaseSafetyMargin == 0 {
		cfg.LeaseSafetyMargin = defaultLeaseSafetyMargin
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
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
	backoff := defaultDaemonBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		lease, found, err := w.client.LeaseNext(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if isDaemonFatalCloudError(err) {
				return err
			}
			w.logger.Warn("bootstrap lease poll failed", "worker_id", cfg.WorkerID, "error", redactForConfig(cfg, "", err.Error()))
			if !waitForPoll(ctx, backoff) {
				return nil
			}
			backoff *= 2
			if backoff > maximumDaemonBackoff {
				backoff = maximumDaemonBackoff
			}
			continue
		}
		backoff = defaultDaemonBackoff
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
	timeoutCtx, timeoutCancel := context.WithTimeout(parent, timeout)
	defer timeoutCancel()
	ctx, cancel := context.WithCancelCause(timeoutCtx)
	defer cancel(nil)
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.heartbeatLoop(heartbeatCtx, lease, cancel)
	}()
	stopHeartbeatAndWait := func() {
		stopHeartbeat()
		<-heartbeatDone
	}
	heartbeatStopped := false
	defer func() {
		if !heartbeatStopped {
			stopHeartbeatAndWait()
		}
	}()
	bundle := lease.Bundle
	defer func() {
		bundle.AgentRegistrationToken = ""
		bundle.SSH.PrivateKey = ""
		bundle.SSH.Password = ""
	}()
	if err := ValidateBundle(bundle); err != nil {
		stopHeartbeatAndWait()
		heartbeatStopped = true
		failure := classifyValidationFailure(err)
		return w.reportFailure(ctx, lease, failure)
	}
	plan, err := BuildBootstrapPlan(cfg, bundle)
	if err != nil {
		stopHeartbeatAndWait()
		heartbeatStopped = true
		failure := classifyPlanFailure(err)
		return w.reportFailure(ctx, lease, failure)
	}

	target := RemoteTarget{Host: bundle.PublicHost, Port: bundle.SSHPort, Username: bundle.SSH.Username, Password: bundle.SSH.Password, PrivateKey: bundle.SSH.PrivateKey}
	if err := w.client.Progress(ctx, lease, "connecting", "connecting to target over SSH"); err != nil {
		if isFatalCloudError(err) || isLeaseLossError(err) {
			return err
		}
	}
	session, err := w.executor.Connect(ctx, target)
	if err != nil {
		stopHeartbeatAndWait()
		heartbeatStopped = true
		if parent.Err() != nil {
			failure := JobFailure{Code: "BOOTSTRAP_WORKER_SHUTDOWN", Message: "bootstrap worker shut down", Retryable: true}
			return w.reportFailure(ctx, lease, failure)
		}
		if cause := context.Cause(ctx); cause != nil && cause != context.DeadlineExceeded {
			return cause
		}
		failure := classifyConnectFailure(redactForLease(cfg, lease, err.Error()))
		return w.reportFailure(ctx, lease, failure)
	}
	defer session.Close()

	for _, step := range plan.Steps {
		if err := w.client.Progress(ctx, lease, step.Status, step.Message); err != nil {
			if isFatalCloudError(err) || isLeaseLossError(err) {
				return err
			}
		}
		result, err := session.Run(ctx, step.Command)
		if err != nil || result.ExitCode != 0 {
			stopHeartbeatAndWait()
			heartbeatStopped = true
			if parent.Err() != nil {
				failure := JobFailure{Code: "BOOTSTRAP_WORKER_SHUTDOWN", Message: "bootstrap worker shut down", Retryable: true}
				return w.reportFailure(ctx, lease, failure)
			}
			if cause := context.Cause(ctx); cause != nil && cause != context.DeadlineExceeded {
				return cause
			}
			failure := classifyStepFailure(step.Status, redactForLease(cfg, lease, fmt.Sprintf("%s failed: %s %s %v", step.Status, result.Stdout, result.Stderr, err)))
			return w.reportFailure(ctx, lease, failure)
		}
	}
	if err := w.client.Progress(ctx, lease, "verifying_agent", "waiting for healthy Agent heartbeat"); err != nil {
		if isFatalCloudError(err) || isLeaseLossError(err) {
			return err
		}
	}
	if err := w.client.WaitForHeartbeat(ctx, lease); err != nil {
		if isFatalCloudError(err) || isLeaseLossError(err) {
			return err
		}
		stopHeartbeatAndWait()
		heartbeatStopped = true
		if parent.Err() != nil {
			failure := JobFailure{Code: "BOOTSTRAP_WORKER_SHUTDOWN", Message: "bootstrap worker shut down", Retryable: true}
			return w.reportFailure(ctx, lease, failure)
		}
		if cause := context.Cause(ctx); cause != nil && cause != context.DeadlineExceeded {
			return cause
		}
		failure := JobFailure{Code: "BOOTSTRAP_CLOUD_TEMPORARY", Message: boundedFailureMessage(redactForLease(cfg, lease, err.Error())), Retryable: true}
		return w.reportFailure(ctx, lease, failure)
	}
	stopHeartbeatAndWait()
	heartbeatStopped = true
	return w.finishLease(ctx, lease, FinishResult{Status: "completed", Message: "bootstrap completed after verified Agent heartbeat"})
}

func (w Worker) finishLease(ctx context.Context, lease Lease, result FinishResult) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	result.Message = redactForLease(w.cfg, lease, result.Message)
	if result.Failure != nil {
		result.Failure.Message = boundedFailureMessage(redactForLease(w.cfg, lease, result.Failure.Message))
	}
	return w.client.Finish(cleanupCtx, lease, result)
}

func (w Worker) reportFailure(ctx context.Context, lease Lease, failure JobFailure) error {
	if err := w.finishLease(ctx, lease, FinishResult{Status: "failed", Failure: &failure}); err != nil {
		return err
	}
	return errors.New(failure.Code)
}

func (w Worker) heartbeatLoop(ctx context.Context, lease Lease, cancel context.CancelCauseFunc) {
	expiresAt := lease.LeaseExpiresAt
	delay := w.cfg.HeartbeatInterval
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		renewedUntil, err := w.client.HeartbeatLease(ctx, lease)
		if err == nil {
			expiresAt = renewedUntil
			delay = w.cfg.HeartbeatInterval
			continue
		}
		if isFatalCloudError(err) {
			cancel(err)
			return
		}
		if isLeaseLossError(err) {
			cancel(fmt.Errorf("BOOTSTRAP_LEASE_LOST: %w", err))
			return
		}
		if !w.now().Before(expiresAt.Add(-w.cfg.LeaseSafetyMargin)) {
			cancel(errors.New("BOOTSTRAP_LEASE_LOST: heartbeat renewal deadline exceeded"))
			return
		}
		delay = w.cfg.HeartbeatRetryInterval
	}
}

func (w Worker) now() time.Time { return w.cfg.Now().UTC() }

func classifyValidationFailure(err error) JobFailure {
	code := "INVALID_BOOTSTRAP_TARGET"
	if errors.Is(err, ErrRuntimeUnsupported) && strings.Contains(err.Error(), "private key") {
		code = "SSH_AUTH_METHOD_UNSUPPORTED"
	}
	return JobFailure{Code: code, Message: boundedFailureMessage(err.Error())}
}

func classifyPlanFailure(err error) JobFailure {
	code := "AGENT_INSTALL_URL_INVALID"
	if errors.Is(err, ErrRuntimeUnsupported) {
		code = "BOOTSTRAP_ROLE_UNSUPPORTED"
	}
	return JobFailure{Code: code, Message: boundedFailureMessage(err.Error())}
}

func classifyConnectFailure(message string) JobFailure {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "parse ssh private key"):
		return JobFailure{Code: "SSH_PRIVATE_KEY_INVALID", Message: boundedFailureMessage(message), Retryable: false}
	case strings.Contains(lower, "knownhosts: key mismatch") || strings.Contains(lower, "knownhosts: key is unknown"):
		return JobFailure{Code: "SSH_HOST_KEY_VERIFICATION_FAILED", Message: boundedFailureMessage(message), Retryable: false}
	default:
		return JobFailure{Code: "BOOTSTRAP_CONNECT_FAILED", Message: boundedFailureMessage(message), Retryable: true}
	}
}

func classifyStepFailure(status, message string) JobFailure {
	code, retryable := "BOOTSTRAP_CLOUD_TEMPORARY", true
	if status == "preflight" {
		code, retryable = "TARGET_OS_UNSUPPORTED", false
	} else if status == "installing_agent" && strings.Contains(strings.ToLower(message), "checksum") {
		code, retryable = "AGENT_INSTALL_CHECKSUM_MISMATCH", false
	} else if status == "registering_agent" && strings.Contains(message, "409") {
		code, retryable = "AGENT_ALREADY_REGISTERED", false
	}
	return JobFailure{Code: code, Message: boundedFailureMessage(message), Retryable: retryable}
}

func boundedFailureMessage(message string) string {
	message = strings.TrimSpace(registry.RedactString(message))
	if len(message) > 512 {
		message = message[:512]
	}
	return message
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
		if bundle.SSH.Password == "" || bundle.SSH.PrivateKey != "" {
			return errors.New("ssh password credential is missing")
		}
	case "private_key":
		if bundle.SSH.PrivateKey == "" || bundle.SSH.Password != "" {
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
	u, err := parseHTTPURL(cfg.AgentInstallURL)
	if err != nil {
		return BootstrapPlan{}, fmt.Errorf("agent_install_url: %w", err)
	}
	if cfg.Production && u.Scheme != "https" {
		return BootstrapPlan{}, errors.New("production requires https agent_install_url")
	}
	if err := validateSHA256(cfg.AgentInstallSHA256); err != nil {
		return BootstrapPlan{}, fmt.Errorf("agent_install_sha256: %w", err)
	}
	agentCloudURL := cfg.AgentCloudURL
	if agentCloudURL == "" {
		agentCloudURL = cfg.CloudURL
	}
	env := map[string]string{
		"OPSI_CLOUD_URL":       strings.TrimRight(agentCloudURL, "/"),
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

func parseHTTPURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("must be an absolute HTTP(S) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("must use http or https")
	}
	if u.User != nil || u.Fragment != "" {
		return nil, errors.New("must not contain user info or a fragment")
	}
	return u, nil
}

func validateSHA256(raw string) error {
	if len(raw) != 64 {
		return errors.New("must contain exactly 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return errors.New("must be hexadecimal")
	}
	return nil
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
  enabled: true
  verify_cache_ttl: 15m
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
