package bootstrapworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	valid := Config{CloudURL: "https://cloud.example", BootstrapWorkerToken: strings.Repeat("x", 32), WorkerID: "bootstrap-worker.dev_01", PollInterval: time.Second, AgentInstallURL: "https://downloads.example/opsi-agent", SSHKnownHostsPath: "/etc/ssh/ssh_known_hosts", Production: true}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid daemon config failed: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"missing worker id": func(c *Config) { c.WorkerID = "" },
		"invalid worker id": func(c *Config) { c.WorkerID = "bad worker/id" },
		"small poll":        func(c *Config) { c.PollInterval = time.Millisecond },
		"large poll":        func(c *Config) { c.PollInterval = 6 * time.Minute },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid config passed")
			}
		})
	}
	if err := (Config{CloudURL: "http://cloud.example", BootstrapWorkerToken: "short", WorkerID: "worker-1", Production: true}).Validate(); err == nil {
		t.Fatal("production guardrails did not fail closed")
	}
}

func TestLoadConfigDefaultsAndRejectsLegacySessionID(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(validPath, []byte(`{"cloud_url":"https://cloud.example","bootstrap_worker_token":"secret","worker_id":"  worker-1  ","agent_install_url":"https://downloads.example/agent"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(validPath)
	if err != nil || cfg.WorkerID != "worker-1" || cfg.PollInterval != defaultPollInterval {
		t.Fatalf("loaded cfg=%+v err=%v", cfg, err)
	}
	legacyPath := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"session_id":"boot-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(legacyPath); err == nil || !strings.Contains(err.Error(), "session_id is no longer supported") {
		t.Fatalf("legacy session_id error=%v", err)
	}
	invalidPollPath := filepath.Join(dir, "invalid-poll.json")
	if err := os.WriteFile(invalidPollPath, []byte(`{"worker_id":"worker-1","poll_interval":"0s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(invalidPollPath); err == nil || !strings.Contains(err.Error(), "poll_interval must be positive") {
		t.Fatalf("invalid poll error=%v", err)
	}
}

func TestRunAutomaticallyPicksUpWorkAfterNoContent(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.emptyBefore = 1
	runUntilFinishes(t, h, 1)
	if h.leaseRequests < 2 || h.finishes[0].status != "completed" {
		t.Fatalf("lease requests=%d finishes=%+v", h.leaseRequests, h.finishes)
	}
}

func TestRunRetriesTemporaryCloudFailureButStopsOnUnauthorized(t *testing.T) {
	t.Run("temporary", func(t *testing.T) {
		h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
		h.leaseFailures = 1
		runUntilFinishes(t, h, 1)
		if h.requests() < 2 {
			t.Fatalf("temporary failure was not retried: %d requests", h.requests())
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		h := newDaemonHarness(t, nil)
		h.leaseErrorStatus = http.StatusUnauthorized
		if err := Run(context.Background(), h.config()); err == nil {
			t.Fatal("unauthorized lease did not stop daemon")
		}
	})
}

func TestRunProcessesLeasesSequentially(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1"), testLease("boot-2", "host-2")})
	h.executor.blockHost = "host-1"
	h.executor.started = make(chan struct{})
	h.executor.release = make(chan struct{})
	go func() {
		<-h.executor.started
		if got := h.requests(); got != 1 {
			t.Errorf("worker requested job 2 while job 1 was active: requests=%d", got)
		}
		close(h.executor.release)
	}()
	runUntilFinishes(t, h, 2)
	if h.maxActive != 1 || len(h.finishes) != 2 {
		t.Fatalf("max active=%d finishes=%+v", h.maxActive, h.finishes)
	}
}

func TestLongRunningJobSendsMultipleHeartbeatsAndStopsAfterCompletion(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.heartbeatNotify = make(chan struct{}, 4)
	h.executor.blockHost = "host-1"
	h.executor.started = make(chan struct{})
	h.executor.release = make(chan struct{})
	go func() {
		<-h.executor.started
		<-h.heartbeatNotify
		<-h.heartbeatNotify
		close(h.executor.release)
	}()
	runUntilFinishes(t, h, 1)
	h.mu.Lock()
	count := h.heartbeatRequests
	h.mu.Unlock()
	if count < 2 {
		t.Fatalf("heartbeat requests=%d want at least 2", count)
	}
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.heartbeatRequests != count {
		t.Fatalf("heartbeat loop continued after completion: before=%d after=%d", count, h.heartbeatRequests)
	}
}

func TestDefinitiveHeartbeatLeaseLossCancelsRemoteWorkWithoutFinish(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.heartbeatErrorStatus = http.StatusGone
	h.executor.blockHost = "host-1"
	h.executor.started = make(chan struct{})
	h.executor.release = make(chan struct{})
	h.executor.canceled = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.config()) }()
	<-h.executor.started
	select {
	case <-h.executor.canceled:
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel remote executor")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.finishes) != 0 {
		t.Fatalf("stale lease reported finish: %+v", h.finishes)
	}
}

func TestHeartbeatWorkerAuthenticationFailureStopsDaemon(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.heartbeatErrorStatus = http.StatusUnauthorized
	h.executor.blockHost = "host-1"
	h.executor.started = make(chan struct{})
	h.executor.release = make(chan struct{})
	h.executor.canceled = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), h.config()) }()
	<-h.executor.started
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("worker authentication failure did not stop daemon")
		}
	case <-time.After(time.Second):
		t.Fatal("worker authentication failure did not stop promptly")
	}
}

func TestTemporaryHeartbeatFailureRecoversBeforeExpiry(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.heartbeatFailures = 2
	h.heartbeatNotify = make(chan struct{}, 8)
	h.executor.blockHost = "host-1"
	h.executor.started = make(chan struct{})
	h.executor.release = make(chan struct{})
	go func() {
		<-h.executor.started
		<-h.heartbeatNotify
		<-h.heartbeatNotify
		<-h.heartbeatNotify
		close(h.executor.release)
	}()
	runUntilFinishes(t, h, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.heartbeatRequests < 3 || len(h.finishes) != 1 || h.finishes[0].status != "completed" {
		t.Fatalf("heartbeats=%d finishes=%+v", h.heartbeatRequests, h.finishes)
	}
}

func TestHeartbeatFailureBeyondSafetyDeadlineCancelsJob(t *testing.T) {
	lease := testLease("boot-1", "host-1")
	lease.LeaseExpiresAt = time.Now().Add(80 * time.Millisecond)
	h := newDaemonHarness(t, []Lease{lease})
	h.heartbeatErrorStatus = http.StatusServiceUnavailable
	h.executor.blockHost = "host-1"
	h.executor.started = make(chan struct{})
	h.executor.release = make(chan struct{})
	h.executor.canceled = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.config()) }()
	<-h.executor.started
	select {
	case <-h.executor.canceled:
	case <-time.After(time.Second):
		t.Fatal("heartbeat safety deadline did not cancel job")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunCancellationDuringJobReportsShutdownAndReturnsNil(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.executor.blockHost = "host-1"
	h.executor.started = make(chan struct{})
	h.executor.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.config()) }()
	<-h.executor.started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("active cancellation did not stop worker")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.finishes) != 1 || h.finishes[0].status != "failed" || h.finishes[0].failureCode != "BOOTSTRAP_WORKER_SHUTDOWN" || !h.finishes[0].retryable {
		t.Fatalf("shutdown finish=%+v", h.finishes)
	}
}

func TestRunContinuesAfterJobFailureAndUnsupportedBundle(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "preflight failure", true: "unsupported role"}[unsupported], func(t *testing.T) {
			first := testLease("boot-1", "host-1")
			if unsupported {
				first.Bundle.Role = "worker"
			}
			h := newDaemonHarness(t, []Lease{first, testLease("boot-2", "host-2")})
			if !unsupported {
				h.executor.failHost = "host-1"
			}
			runUntilFinishes(t, h, 2)
			if h.finishes[0].status != "failed" || h.finishes[1].status != "completed" {
				t.Fatalf("finishes=%+v", h.finishes)
			}
			if unsupported && (h.finishes[0].failureCode != "BOOTSTRAP_ROLE_UNSUPPORTED" || h.finishes[0].retryable) {
				t.Fatalf("unsupported classification=%+v", h.finishes[0])
			}
		})
	}
}

func TestBootstrapConnectFailureIsRetryable(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.executor.failConnectHost = "host-1"
	runUntilFinishes(t, h, 1)
	if h.finishes[0].failureCode != "BOOTSTRAP_CONNECT_FAILED" || !h.finishes[0].retryable {
		t.Fatalf("connect classification=%+v", h.finishes[0])
	}
}

func TestRunNoWorkDoesNotHotLoopAndCancelsPromptly(t *testing.T) {
	h := newDaemonHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.config()) }()
	time.Sleep(650 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle cancellation did not stop worker")
	}
	if h.requests() > 2 {
		t.Fatalf("empty polling hot-looped: %d requests", h.requests())
	}
}

func TestRunRedactsSecretsIncludingLeaseAndWorkerTokens(t *testing.T) {
	lease := testLease("boot-1", "host-1")
	lease.Bundle.SSH.PrivateKey = "private-key-secret"
	h := newDaemonHarness(t, []Lease{lease})
	h.executor.failHost = "host-1"
	h.executor.secretOutput = true
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	runUntilFinishes(t, h, 1)
	all := logs.String() + h.finishes[0].message
	for _, secret := range []string{"ssh-secret", "areg-secret", "private-key-secret", "agent-secret", "pat-secret", "kubeconfig-secret", "app-secret", "lease-secret-boot-1", "worker-secret"} {
		if strings.Contains(all, secret) {
			t.Fatalf("secret %q leaked in %q", secret, all)
		}
	}
}

func TestValidateBundleInvalidAndUnsupportedTargets(t *testing.T) {
	b := testLease("boot-1", "host-1").Bundle
	b.PublicHost = ""
	if err := ValidateBundle(b); err == nil || !strings.Contains(err.Error(), "public_host") {
		t.Fatalf("invalid target error=%v", err)
	}
	b = testLease("boot-1", "host-1").Bundle
	b.SSH.AuthMethod, b.SSH.Password, b.SSH.PrivateKey = "private_key", "", "private-key-secret"
	if err := ValidateBundle(b); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("private key error=%v", err)
	}
}

type finishRecord struct {
	sessionID, status, message, failureCode string
	retryable                               bool
}

type daemonHarness struct {
	mu                   sync.Mutex
	server               *httptest.Server
	executor             *fakeExecutor
	logger               *slog.Logger
	leases               []Lease
	emptyBefore          int
	leaseFailures        int
	leaseErrorStatus     int
	leaseRequests        int
	heartbeatRequests    int
	heartbeatFailures    int
	heartbeatErrorStatus int
	heartbeatNotify      chan struct{}
	active               int
	maxActive            int
	finishes             []finishRecord
	cancel               context.CancelFunc
}

func newDaemonHarness(t *testing.T, leases []Lease) *daemonHarness {
	t.Helper()
	h := &daemonHarness{leases: append([]Lease(nil), leases...), executor: &fakeExecutor{h: nil}}
	h.executor.h = h
	h.server = httptest.NewServer(http.HandlerFunc(h.serveHTTP))
	t.Cleanup(h.server.Close)
	return h
}

func (h *daemonHarness) config() Config {
	return Config{CloudURL: h.server.URL, BootstrapWorkerToken: "worker-secret", WorkerID: "worker-1", PollInterval: minPollInterval, AgentInstallURL: "https://downloads.example/opsi-agent", Executor: h.executor, Logger: h.logger, HeartbeatInterval: 20 * time.Millisecond, HeartbeatRetryInterval: 5 * time.Millisecond, LeaseSafetyMargin: 10 * time.Millisecond}
}

func (h *daemonHarness) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Bootstrap-Worker-Token") != "worker-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case r.URL.Path == "/internal/bootstrap/sessions/lease":
		h.leaseRequests++
		if h.leaseErrorStatus != 0 {
			w.WriteHeader(h.leaseErrorStatus)
			return
		}
		if h.leaseFailures > 0 {
			h.leaseFailures--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if h.emptyBefore > 0 {
			h.emptyBefore--
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(h.leases) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		lease := h.leases[0]
		h.leases = h.leases[1:]
		_ = json.NewEncoder(w).Encode(lease)
	case strings.HasSuffix(r.URL.Path, "/status"):
		h.requireLeaseHeaders(w, r)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "verifying"})
	case strings.HasSuffix(r.URL.Path, "/progress"):
		h.requireLeaseHeaders(w, r)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case strings.HasSuffix(r.URL.Path, "/lease-heartbeat"):
		h.requireLeaseHeaders(w, r)
		h.heartbeatRequests++
		if h.heartbeatNotify != nil {
			select {
			case h.heartbeatNotify <- struct{}{}:
			default:
			}
		}
		if h.heartbeatErrorStatus != 0 {
			w.WriteHeader(h.heartbeatErrorStatus)
			return
		}
		if h.heartbeatFailures > 0 {
			h.heartbeatFailures--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "boot-1", "lease_expires_at": time.Now().Add(time.Second)})
	case strings.HasSuffix(r.URL.Path, "/finish"):
		h.requireLeaseHeaders(w, r)
		var req struct {
			Status      string `json:"status"`
			Message     string `json:"message"`
			FailureCode string `json:"failure_code"`
			Retryable   bool   `json:"retryable"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		parts := strings.Split(r.URL.Path, "/")
		h.finishes = append(h.finishes, finishRecord{sessionID: parts[4], status: req.Status, message: req.Message, failureCode: req.FailureCode, retryable: req.Retryable})
		_ = json.NewEncoder(w).Encode(map[string]string{"status": req.Status})
		if h.cancel != nil && len(h.leases) == 0 {
			h.cancel()
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *daemonHarness) requireLeaseHeaders(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Bootstrap-Worker-ID") != "worker-1" || !strings.HasPrefix(r.Header.Get("X-Bootstrap-Lease-Token"), "lease-secret-") {
		w.WriteHeader(http.StatusForbidden)
	}
}

func (h *daemonHarness) requests() int { h.mu.Lock(); defer h.mu.Unlock(); return h.leaseRequests }

func runUntilFinishes(t *testing.T, h *daemonHarness, count int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.config()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("worker did not finish expected leases")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.finishes) != count {
		t.Fatalf("finish count=%d want=%d", len(h.finishes), count)
	}
}

func testLease(sessionID, host string) Lease {
	var b Bundle
	b.SessionID, b.ProjectID, b.NodeID = sessionID, "proj-1", "node-"+sessionID
	b.PublicHost, b.SSHPort, b.Role = host, 22, "first_server"
	b.AgentRegistrationToken = "areg-secret"
	b.SSH.AuthMethod, b.SSH.Username, b.SSH.Password = "password", "root", "ssh-secret"
	return Lease{Bundle: b, LeaseToken: "lease-secret-" + sessionID, LeaseExpiresAt: time.Now().Add(time.Hour)}
}

type fakeExecutor struct {
	h               *daemonHarness
	failHost        string
	failConnectHost string
	secretOutput    bool
	blockHost       string
	started         chan struct{}
	release         chan struct{}
	canceled        chan struct{}
	cancelOnce      sync.Once
}

func (f *fakeExecutor) Connect(_ context.Context, target RemoteTarget) (RemoteSession, error) {
	if f.failConnectHost == target.Host {
		return nil, errors.New("temporary network timeout")
	}
	f.h.mu.Lock()
	f.h.active++
	if f.h.active > f.h.maxActive {
		f.h.maxActive = f.h.active
	}
	f.h.mu.Unlock()
	return &fakeSession{executor: f, host: target.Host}, nil
}

type fakeSession struct {
	executor *fakeExecutor
	host     string
}

func (s *fakeSession) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	if s.executor.blockHost == s.host && spec.Script == preflightScript {
		close(s.executor.started)
		select {
		case <-ctx.Done():
			s.executor.cancelOnce.Do(func() {
				if s.executor.canceled != nil {
					close(s.executor.canceled)
				}
			})
			return CommandResult{ExitCode: 255}, ctx.Err()
		case <-s.executor.release:
		}
	}
	if s.executor.failHost == s.host && spec.Script == preflightScript {
		out := "preflight failed"
		if s.executor.secretOutput {
			out = "password=ssh-secret token=areg-secret private_key=private-key-secret agent_token=agent-secret pat=pat-secret kubeconfig=kubeconfig-secret app_secret=app-secret lease-secret-boot-1 worker-secret"
		}
		return CommandResult{ExitCode: 1, Stdout: out, Stderr: out}, errors.New(out)
	}
	return CommandResult{}, nil
}

func (s *fakeSession) Close() error {
	s.executor.h.mu.Lock()
	s.executor.h.active--
	s.executor.h.mu.Unlock()
	return nil
}
