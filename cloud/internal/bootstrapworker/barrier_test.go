package bootstrapworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func testBarrierConfig(t *testing.T, dir, session, run string) StagingCrashBarrierConfig {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return StagingCrashBarrierConfig{Enabled: true, Environment: "e2e", SessionID: session, RunID: run, Step: crashBarrierStep, Boundary: crashBarrierBoundary, StateDir: dir}
}

func armTestBarrier(t *testing.T, cfg StagingCrashBarrierConfig) stagingCrashBarrier {
	t.Helper()
	barrier := newStagingCrashBarrier(cfg)
	if err := writeCrashBarrierState(barrier.statePath(), crashBarrierStateForConfig(cfg, crashBarrierArmed, "")); err != nil {
		t.Fatal(err)
	}
	return barrier
}

func waitForBarrierState(t *testing.T, path, want string) crashBarrierState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, exists, err := readCrashBarrierState(path)
		if err != nil {
			t.Fatal(err)
		}
		if exists && state.State == want {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("barrier state did not reach %q", want)
	return crashBarrierState{}
}

func TestStagingCrashBarrierConfigDefaultsOffAndRejectsProduction(t *testing.T) {
	if err := (StagingCrashBarrierConfig{}).validate(false); err != nil {
		t.Fatalf("disabled barrier validation failed: %v", err)
	}
	valid := validProductionWorkerConfig(t)
	valid.StagingCrashBarrier = StagingCrashBarrierConfig{Enabled: true, Environment: "e2e", SessionID: "boot-1", RunID: "run-1", Step: crashBarrierStep, Boundary: crashBarrierBoundary, StateDir: t.TempDir()}
	if err := valid.Validate(); err == nil || !strings.Contains(err.Error(), "not allowed in production") {
		t.Fatalf("production barrier was accepted: %v", err)
	}
}

func TestLoadConfigParsesStagingCrashBarrier(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "worker.json")
	data := `{"cloud_url":"http://cloud:9800","bootstrap_worker_token":"token","worker_id":"worker-1","k3s_version":"v1.32.5+k3s1","k3s_installer_url":"https://get.k3s.io","k3s_installer_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","agent_install_url":"https://downloads.example/agent","agent_install_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","staging_crash_barrier":{"enabled":true,"environment":"e2e","session_id":"boot-1","run_id":"run-1","step":"install_k3s","boundary":"after_execute_before_checkpoint","state_dir":"` + dir + `"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.StagingCrashBarrier.SessionID != "boot-1" || cfg.StagingCrashBarrier.RunID != "run-1" {
		t.Fatalf("barrier config=%+v err=%v", cfg.StagingCrashBarrier, err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("parsed barrier config failed validation: %v", err)
	}
}

func TestLoadConfigProductionRejectsExplicitDisabledBarrier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.json")
	if err := os.WriteFile(path, []byte(`{"production":true,"staging_crash_barrier":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "not allowed in production") {
		t.Fatalf("explicit disabled production barrier was accepted: %v", err)
	}
}

func TestGeneratedBarrierConfigPassesWorkerValidator(t *testing.T) {
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(mustCallerFile(t)))))
	fixture := t.TempDir()
	tokenPath := filepath.Join(fixture, "worker-token")
	knownHostsPath := filepath.Join(fixture, "known_hosts")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat("t", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHostsPath, []byte("example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEexample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(fixture, "bootstrap-worker.json")
	output := filepath.Join(fixture, "bootstrap-worker.e2e.json")
	sourceData := []byte(fmt.Sprintf(`{"cloud_url":"http://cloud:9800","allow_insecure_internal_cloud_url":true,"bootstrap_worker_token_file":%q,"worker_id":"worker-1","poll_interval":"1s","k3s_version":"v1.32.5+k3s1","k3s_installer_url":"https://get.k3s.io","k3s_installer_sha256":"%s","agent_install_url":"https://downloads.example/agent","agent_install_sha256":"%s","ssh_known_hosts_path":%q,"production":true,"timeout":"10m"}`, tokenPath, strings.Repeat("b", 64), strings.Repeat("a", 64), knownHostsPath))
	if err := os.WriteFile(source, sourceData, 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "scripts/e2e/bootstrap-worker-barrier.sh"), "configure", "--source-config", source, "--output-config", output, "--session-id", "boot-validator", "--run-id", "run-validator")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("barrier configure failed: %v: %s", err, output)
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, sourceData) {
		t.Fatalf("source config changed: %v", err)
	}
	cfg, err := LoadConfig(output)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowInsecureInternalCloudURL || cfg.Production || cfg.CloudURL != "http://cloud:9800" {
		t.Fatalf("generated config fields are wrong: %+v", cfg)
	}
	// The container mount is unavailable in the host test; validate the generated
	// config with the real validator after pointing only state_dir at a private fixture.
	cfg.StagingCrashBarrier.StateDir = stateDir
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated barrier config failed Worker validation: %v", err)
	}
}

func mustCallerFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	return file
}

func TestStagingCrashBarrierInsertionPointAndCancellation(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.barrier = testBarrierConfig(t, t.TempDir(), "boot-1", "run-1")
	armTestBarrier(t, h.barrier)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewWorker(h.config()).Run(ctx) }()
	waitForBarrierState(t, (newStagingCrashBarrier(h.barrier)).statePath(), crashBarrierReached)
	h.mu.Lock()
	events := append([]string(nil), h.events...)
	h.mu.Unlock()
	wantPrefix := []string{"checkpoint:", "run:preflight", "checkpoint:preflight", "run:install_k3s"}
	if len(events) < len(wantPrefix) || !equalStrings(events[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("events=%v want prefix=%v", events, wantPrefix)
	}
	for _, event := range events {
		if event == "checkpoint:install_k3s" || event == "run:install_agent" {
			t.Fatalf("barrier crossed checkpoint boundary: events=%v", events)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.checkpointRequests) != 2 || h.checkpointRequests[1].LastCompletedStep != "preflight" {
		t.Fatalf("checkpoint advanced while barrier waited: %+v", h.checkpointRequests)
	}
	if len(h.finishes) != 1 || h.finishes[0].failureCode != "BOOTSTRAP_WORKER_SHUTDOWN" {
		t.Fatalf("shutdown result=%+v", h.finishes)
	}
}

func TestStagingCrashBarrierReplayAfterRestartAndCompletion(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.barrier = testBarrierConfig(t, t.TempDir(), "boot-1", "run-1")
	h.leases[0].Bundle.Checkpoint = checkpointForHarness(t, h, h.leases[0], 1)
	armTestBarrier(t, h.barrier)
	barrier := newStagingCrashBarrier(h.barrier)
	if err := writeCrashBarrierState(barrier.statePath(), crashBarrierStateForConfig(h.barrier, crashBarrierReached, "old-worker-process")); err != nil {
		t.Fatal(err)
	}
	runUntilFinishes(t, h, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	var ran []string
	for _, script := range h.executor.runScripts {
		ran = append(ran, stepIDForScript(script))
	}
	if !equalStrings(ran, []string{"install_k3s", "install_agent", "register_agent"}) {
		t.Fatalf("replay skipped remote step or later steps: %v", ran)
	}
	if len(h.checkpointRequests) != 3 || h.checkpointRequests[0].NextStepIndex != 2 || h.checkpointRequests[2].NextStepIndex != 4 {
		t.Fatalf("replay checkpoint sequence=%+v", h.checkpointRequests)
	}
	state, exists, err := readCrashBarrierState((newStagingCrashBarrier(h.barrier)).statePath())
	if err != nil || !exists || state.State != crashBarrierCompleted {
		t.Fatalf("barrier completion state=%+v exists=%v err=%v", state, exists, err)
	}
}

func TestStagingCrashBarrierCompletionFailureDoesNotContradictPersistedCheckpoint(t *testing.T) {
	first := testLease("boot-1", "host-1")
	second := testLease("boot-1", "host-1")
	h := newDaemonHarness(t, []Lease{first, second})
	h.barrier = testBarrierConfig(t, t.TempDir(), "boot-1", "run-1")
	h.leases[0].Bundle.Checkpoint = checkpointForHarness(t, h, h.leases[0], 1)
	h.leases[1].Bundle.Checkpoint = checkpointForHarness(t, h, h.leases[1], 2)
	barrier := armTestBarrier(t, h.barrier)
	if err := writeCrashBarrierState(barrier.statePath(), crashBarrierStateForConfig(h.barrier, crashBarrierReached, "old-worker-process")); err != nil {
		t.Fatal(err)
	}
	var evidenceFailure bool
	h.checkpointPersisted = func(checkpoint registry.BootstrapCheckpoint) {
		if checkpoint.NextStepIndex == 2 && !evidenceFailure {
			evidenceFailure = true
			if err := os.Chmod(barrier.statePath(), 0o644); err != nil {
				t.Error(err)
			}
		}
	}
	var logs strings.Builder
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	runUntilFinishes(t, h, 1)

	h.mu.Lock()
	defer h.mu.Unlock()
	if !evidenceFailure || len(h.checkpointRequests) != 3 || h.checkpointRequests[0].NextStepIndex != 2 {
		t.Fatalf("checkpoint evidence failure=%v requests=%+v", evidenceFailure, h.checkpointRequests)
	}
	if len(h.finishes) != 1 || h.finishes[0].status != "completed" || h.finishes[0].failureCode != "" {
		t.Fatalf("post-checkpoint evidence failure produced product failure: %+v", h.finishes)
	}
	var ran []string
	for _, script := range h.executor.runScripts {
		ran = append(ran, stepIDForScript(script))
	}
	if !equalStrings(ran, []string{"install_k3s", "install_agent", "register_agent"}) {
		t.Fatalf("resume reran persisted step or skipped later work: %v", ran)
	}
	data, err := os.ReadFile(barrier.statePath())
	if err != nil {
		t.Fatal(err)
	}
	var state crashBarrierState
	if err := json.Unmarshal(data, &state); err != nil || state.State != crashBarrierConsumed {
		t.Fatalf("marker was faked completed: state=%+v err=%v", state, err)
	}
	if !strings.Contains(logs.String(), "after persisted checkpoint") {
		t.Fatalf("evidence failure was not surfaced at daemon boundary: %s", logs.String())
	}
}

func TestStagingCrashBarrierReplayFailureKeepsCheckpoint(t *testing.T) {
	h := newDaemonHarness(t, []Lease{testLease("boot-1", "host-1")})
	h.barrier = testBarrierConfig(t, t.TempDir(), "boot-1", "run-1")
	h.leases[0].Bundle.Checkpoint = checkpointForHarness(t, h, h.leases[0], 1)
	barrier := armTestBarrier(t, h.barrier)
	if err := writeCrashBarrierState(barrier.statePath(), crashBarrierStateForConfig(h.barrier, crashBarrierReached, "old-worker-process")); err != nil {
		t.Fatal(err)
	}
	h.executor.failScript = installK3sScript
	runUntilFinishes(t, h, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.executor.runScripts) != 1 || stepIDForScript(h.executor.runScripts[0]) != "install_k3s" {
		t.Fatalf("replay failure ran=%v", h.executor.runScripts)
	}
	if len(h.checkpointRequests) != 0 || h.finishes[0].status != "failed" {
		t.Fatalf("checkpoint/final state after replay failure requests=%+v finishes=%+v", h.checkpointRequests, h.finishes)
	}
	state, _, err := readCrashBarrierState(barrier.statePath())
	if err != nil || state.State != crashBarrierReached {
		t.Fatalf("replay failure barrier state=%+v err=%v", state, err)
	}
}

func TestStagingCrashBarrierTargetAndInvalidStateIsolation(t *testing.T) {
	dir := t.TempDir()
	cfgA := testBarrierConfig(t, dir, "boot-a", "run-a")
	cfgB := testBarrierConfig(t, dir, "boot-b", "run-b")
	barrierA := armTestBarrier(t, cfgA)
	leaseA := testLease("boot-a", "host-a")
	step := BootstrapStep{ID: crashBarrierStep}
	state, _, err := readCrashBarrierState(barrierA.statePath())
	if err != nil || state.State != crashBarrierArmed {
		t.Fatal(err)
	}
	if err := newStagingCrashBarrier(cfgB).beforeCheckpoint(context.Background(), leaseA, step); err != nil {
		t.Fatalf("wrong target blocked or failed: %v", err)
	}
	if err := newStagingCrashBarrier(cfgA).beforeCheckpoint(context.Background(), leaseA, BootstrapStep{ID: "install_agent"}); err != nil {
		t.Fatalf("wrong step blocked or failed: %v", err)
	}
	cfgWrongRun := testBarrierConfig(t, dir, "boot-a", "run-other")
	if err := newStagingCrashBarrier(cfgWrongRun).beforeCheckpoint(context.Background(), leaseA, step); err != nil {
		t.Fatalf("wrong run blocked or failed: %v", err)
	}
	if err := writeCrashBarrierState(barrierA.statePath(), crashBarrierState{Version: crashBarrierStateVersion, State: crashBarrierArmed, Environment: cfgA.Environment, SessionID: cfgA.SessionID, RunID: "stale-run", Step: crashBarrierStep, Boundary: crashBarrierBoundary}); err != nil {
		t.Fatal(err)
	}
	if err := newStagingCrashBarrier(cfgA).beforeCheckpoint(context.Background(), leaseA, step); err == nil {
		t.Fatal("stale marker was accepted")
	}
	if err := os.WriteFile(barrierA.statePath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newStagingCrashBarrier(cfgA).beforeCheckpoint(context.Background(), leaseA, step); err == nil {
		t.Fatal("malformed marker was accepted")
	}
}

func TestStagingCrashBarrierMarkerPermissionsAndSecretFreeState(t *testing.T) {
	cfg := testBarrierConfig(t, t.TempDir(), "boot-1", "run-1")
	barrier := armTestBarrier(t, cfg)
	data, err := os.ReadFile(barrier.statePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ssh-private-key", "registration-token", "pat", "kubeconfig"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("marker contains secret %q", secret)
		}
	}
	if mode := mustFileMode(t, barrier.statePath()); mode != 0o600 {
		t.Fatalf("marker mode=%o", mode)
	}
	if err := os.Chmod(barrier.statePath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCrashBarrierState(barrier.statePath()); err == nil {
		t.Fatal("world-readable marker accepted")
	}
	var state crashBarrierState
	if err := json.Unmarshal(data, &state); err != nil || state.State != crashBarrierArmed {
		t.Fatalf("marker=%s err=%v", data, err)
	}
}

func TestStagingCrashBarrierRejectsSharedStateDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := testBarrierConfig(t, dir, "boot-1", "run-1")
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validate(false); err == nil {
		t.Fatal("shared state directory was accepted")
	}
}

func TestStagingCrashBarrierContextCancellationDoesNotLeakWait(t *testing.T) {
	cfg := testBarrierConfig(t, t.TempDir(), "boot-1", "run-1")
	barrier := armTestBarrier(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- barrier.beforeCheckpoint(ctx, testLease("boot-1", "host-1"), BootstrapStep{ID: crashBarrierStep})
	}()
	waitForBarrierState(t, barrier.statePath(), crashBarrierReached)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait cancellation error=%v", err)
	}
}

func mustFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
