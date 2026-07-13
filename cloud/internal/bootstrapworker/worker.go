package bootstrapworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	K3sVersion             string           `json:"k3s_version"`
	K3sInstallerURL        string           `json:"k3s_installer_url"`
	K3sInstallerSHA256     string           `json:"k3s_installer_sha256"`
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
	K3sVersion           string `json:"k3s_version"`
	K3sInstallerURL      string `json:"k3s_installer_url"`
	K3sInstallerSHA256   string `json:"k3s_installer_sha256"`
	AgentInstallURL      string `json:"agent_install_url"`
	AgentInstallSHA256   string `json:"agent_install_sha256"`
	SSHKnownHostsPath    string `json:"ssh_known_hosts_path"`
	Production           bool   `json:"production"`
	Timeout              string `json:"timeout"`
}

type Bundle struct {
	SessionID                string                       `json:"session_id"`
	ProjectID                string                       `json:"project_id"`
	NodeID                   string                       `json:"node_id"`
	PublicHost               string                       `json:"public_host"`
	SSHPort                  int                          `json:"ssh_port"`
	Role                     string                       `json:"role"`
	AgentRegistrationToken   string                       `json:"agent_registration_token"`
	AgentRegistrationExpires time.Time                    `json:"agent_registration_expires"`
	Checkpoint               registry.BootstrapCheckpoint `json:"checkpoint"`
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
	cfg := Config{CloudURL: raw.CloudURL, AgentCloudURL: raw.AgentCloudURL, BootstrapWorkerToken: raw.BootstrapWorkerToken, WorkerID: strings.TrimSpace(raw.WorkerID), K3sVersion: strings.TrimSpace(raw.K3sVersion), K3sInstallerURL: raw.K3sInstallerURL, K3sInstallerSHA256: raw.K3sInstallerSHA256, AgentInstallURL: raw.AgentInstallURL, AgentInstallSHA256: raw.AgentInstallSHA256, SSHKnownHostsPath: raw.SSHKnownHostsPath, Production: raw.Production, PollInterval: defaultPollInterval}
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
	if c.Production && (u.Hostname() == "example.invalid" || isPlaceholderValue(c.CloudURL)) {
		return errors.New("production cloud_url must not use a placeholder")
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
	if c.Production && (agentURL.Hostname() == "example.invalid" || isPlaceholderValue(agentCloudURL)) {
		return errors.New("production agent_cloud_url must not use a placeholder")
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
	if c.Production && strings.TrimSpace(c.SSHKnownHostsPath) == "" {
		return errors.New("production requires ssh_known_hosts_path")
	}
	if c.SSHKnownHostsPath != "" {
		if err := validateKnownHostsFile(c.SSHKnownHostsPath, c.Production); err != nil {
			return fmt.Errorf("ssh_known_hosts_path: %w", err)
		}
	}
	if err := validateK3sVersion(c.K3sVersion); err != nil {
		return fmt.Errorf("k3s_version: %w", err)
	}
	for name, raw := range map[string]string{
		"k3s_installer_url": c.K3sInstallerURL,
		"agent_install_url": c.AgentInstallURL,
	} {
		u, err := parseHTTPURL(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if c.Production && u.Scheme != "https" {
			return fmt.Errorf("production requires https %s", name)
		}
		if c.Production && (u.Hostname() == "example.invalid" || isPlaceholderValue(raw)) {
			return fmt.Errorf("production %s must not use a placeholder", name)
		}
	}
	for name, digest := range map[string]string{
		"k3s_installer_sha256": c.K3sInstallerSHA256,
		"agent_install_sha256": c.AgentInstallSHA256,
	} {
		if err := validateSHA256(digest); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if c.Production && (digest == strings.Repeat("0", 64) || isPlaceholderValue(digest)) {
			return fmt.Errorf("production %s must not use a placeholder", name)
		}
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
	Checkpoint(context.Context, Lease, registry.BootstrapCheckpoint) (registry.BootstrapCheckpoint, error)
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
	fingerprint := BootstrapPlanFingerprint(cfg, plan)
	checkpoint := bundle.Checkpoint
	if !checkpointInitialized(checkpoint) {
		checkpoint, err = w.client.Checkpoint(ctx, lease, registry.BootstrapCheckpoint{
			SchemaVersion:   registry.BootstrapCheckpointSchemaVersion,
			PlanVersion:     plan.Version,
			PlanFingerprint: fingerprint,
			NextStepIndex:   0,
		})
		if err != nil {
			stopHeartbeatAndWait()
			heartbeatStopped = true
			if isFatalCloudError(err) || isLeaseLossError(err) {
				return err
			}
			return w.reportFailure(ctx, lease, classifyCheckpointFailure(err))
		}
	}
	if err := ValidateBootstrapCheckpoint(plan, fingerprint, checkpoint); err != nil {
		stopHeartbeatAndWait()
		heartbeatStopped = true
		return w.reportFailure(ctx, lease, classifyCheckpointFailure(err))
	}

	if checkpoint.NextStepIndex < len(plan.Steps) {
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
			failure := classifyConnectFailure(err)
			failure.Message = redactForLease(cfg, lease, failure.Message)
			return w.reportFailure(ctx, lease, failure)
		}
		defer session.Close()

		for index := checkpoint.NextStepIndex; index < len(plan.Steps); index++ {
			step := plan.Steps[index]
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
			requested := checkpoint
			requested.NextStepIndex = index + 1
			requested.LastCompletedStep = step.ID
			requested.UpdatedAt = nil
			persisted, err := w.client.Checkpoint(ctx, lease, requested)
			if err != nil {
				stopHeartbeatAndWait()
				heartbeatStopped = true
				if isFatalCloudError(err) || isLeaseLossError(err) {
					return err
				}
				return w.reportFailure(ctx, lease, classifyCheckpointFailure(err))
			}
			if err := ValidateBootstrapCheckpoint(plan, fingerprint, persisted); err != nil || persisted.NextStepIndex != index+1 {
				stopHeartbeatAndWait()
				heartbeatStopped = true
				if err == nil {
					err = fmt.Errorf("%w: Cloud returned an unexpected checkpoint index", ErrBootstrapCheckpointInvalid)
				}
				return w.reportFailure(ctx, lease, classifyCheckpointFailure(err))
			}
			checkpoint = persisted
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

func checkpointInitialized(checkpoint registry.BootstrapCheckpoint) bool {
	return checkpoint.SchemaVersion != 0
}
