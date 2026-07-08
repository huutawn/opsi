package bootstrapworker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	if err := (Config{CloudURL: "https://cloud.example", BootstrapWorkerToken: strings.Repeat("x", 32), SessionID: "boot-1", AgentInstallURL: "https://downloads.example/opsi-agent", SSHKnownHostsPath: "/etc/ssh/ssh_known_hosts", Production: true}).Validate(); err != nil {
		t.Fatalf("valid production config failed: %v", err)
	}
	if err := (Config{CloudURL: "http://cloud.example", BootstrapWorkerToken: "short", SessionID: "boot-1", Production: true}).Validate(); err == nil {
		t.Fatal("missing production config did not fail closed")
	}
}

func TestRunOnceSupportedCompletesAfterHeartbeatVerification(t *testing.T) {
	env := newWorkerHarness(t, supportedBundle())
	err := RunOnce(context.Background(), Config{CloudURL: env.server.URL, BootstrapWorkerToken: "worker-secret", SessionID: "boot-1", AgentInstallURL: "https://downloads.example/opsi-agent", Executor: env.executor})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	wantStages := []string{"connecting", "preflight", "installing_k3s", "installing_agent", "registering_agent", "verifying_agent", "completed"}
	if !reflect.DeepEqual(env.stages, wantStages) {
		t.Fatalf("stages=%v want=%v", env.stages, wantStages)
	}
	if !env.statusPolled {
		t.Fatal("worker completed without polling heartbeat/session status")
	}
	if env.completedBeforeVerify {
		t.Fatal("worker completed before heartbeat verification")
	}
	if errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatal("supported bundle returned ErrRuntimeUnsupported")
	}
}

func TestRunOnceFailureModesFinishFailed(t *testing.T) {
	tests := []struct {
		name      string
		failStage string
		connect   bool
		want      string
	}{
		{name: "connect", connect: true, want: "BOOTSTRAP_CONNECT_FAILED"},
		{name: "preflight", failStage: "preflight", want: "preflight failed"},
		{name: "k3s", failStage: "installing_k3s", want: "installing_k3s failed"},
		{name: "agent install", failStage: "installing_agent", want: "installing_agent failed"},
		{name: "agent register", failStage: "registering_agent", want: "registering_agent failed"},
		{name: "heartbeat", failStage: "heartbeat", want: "BOOTSTRAP_HEARTBEAT_VERIFY_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newWorkerHarness(t, supportedBundle())
			env.executor.connectErr = tt.connect
			env.executor.failStage = tt.failStage
			env.heartbeatFails = tt.failStage == "heartbeat"
			cfg := Config{CloudURL: env.server.URL, BootstrapWorkerToken: "worker-secret", SessionID: "boot-1", AgentInstallURL: "https://downloads.example/opsi-agent", Executor: env.executor}
			if env.heartbeatFails {
				cfg.Timeout = 30 * time.Millisecond
			}
			err := RunOnce(context.Background(), cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if env.finishStatus != "failed" || !strings.Contains(env.finishMessage, tt.want) {
				t.Fatalf("finish=%q %q want failed containing %q", env.finishStatus, env.finishMessage, tt.want)
			}
		})
	}
}

func TestRunOnceRedactsSecretsInProgressFinishAndErrors(t *testing.T) {
	bundle := supportedBundle()
	env := newWorkerHarness(t, bundle)
	env.executor.failStage = "preflight"
	env.executor.secretOutput = true
	err := RunOnce(context.Background(), Config{CloudURL: env.server.URL, BootstrapWorkerToken: "worker-secret", SessionID: "boot-1", AgentInstallURL: "https://downloads.example/opsi-agent", Executor: env.executor})
	if err == nil {
		t.Fatal("expected error")
	}
	all := err.Error() + " " + env.finishMessage + " " + strings.Join(env.progressMessages, " ")
	for _, secret := range []string{"ssh-secret", "areg-secret", "private-key-secret", "agent-secret", "pat-secret", "kubeconfig-secret", "app-secret"} {
		if strings.Contains(all, secret) {
			t.Fatalf("secret leaked in worker output: %s", all)
		}
	}
}

func TestUnsupportedAndMissingConfigFailClosed(t *testing.T) {
	privateKey := supportedBundle()
	privateKey.SSH.AuthMethod = "private_key"
	privateKey.SSH.Password = ""
	privateKey.SSH.PrivateKey = "private-key-secret"
	if err := ValidateBundle(privateKey); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("private-key mode should be typed unsupported, got %v", err)
	}
	worker := supportedBundle()
	worker.Role = "worker"
	if _, err := BuildBootstrapPlan(Config{AgentInstallURL: "https://downloads.example/opsi-agent"}, worker); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("worker role should be typed unsupported, got %v", err)
	}
	if _, err := BuildBootstrapPlan(Config{}, supportedBundle()); err == nil || !strings.Contains(err.Error(), "agent_install_url") {
		t.Fatalf("missing agent install config should fail closed, got %v", err)
	}
}

func TestValidateBundleInvalidTargetFailsClosed(t *testing.T) {
	b := supportedBundle()
	b.PublicHost = ""
	if err := ValidateBundle(b); err == nil || !strings.Contains(err.Error(), "public_host") {
		t.Fatalf("expected invalid target error, got %v", err)
	}
}

type workerHarness struct {
	server                *httptest.Server
	executor              *fakeExecutor
	stages                []string
	progressMessages      []string
	finishStatus          string
	finishMessage         string
	statusPolled          bool
	heartbeatFails        bool
	completedBeforeVerify bool
}

func newWorkerHarness(t *testing.T, bundle Bundle) *workerHarness {
	t.Helper()
	env := &workerHarness{executor: &fakeExecutor{}}
	env.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Bootstrap-Worker-Token") != "worker-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/take"):
			_ = json.NewEncoder(w).Encode(bundle)
		case strings.HasSuffix(r.URL.Path, "/progress"):
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			env.stages = append(env.stages, req["status"])
			env.progressMessages = append(env.progressMessages, req["message"])
			_ = json.NewEncoder(w).Encode(map[string]string{"status": req["status"]})
		case strings.HasSuffix(r.URL.Path, "/status"):
			env.statusPolled = true
			status := "verifying"
			if env.heartbeatFails {
				status = "waiting_agent"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
		case strings.HasSuffix(r.URL.Path, "/finish"):
			if !env.statusPolled && env.finishStatus == "" {
				env.completedBeforeVerify = true
			}
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			env.finishStatus, env.finishMessage = req["status"], req["message"]
			env.stages = append(env.stages, req["status"])
			_ = json.NewEncoder(w).Encode(map[string]string{"status": req["status"]})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(env.server.Close)
	return env
}

func supportedBundle() Bundle {
	var b Bundle
	b.SessionID = "boot-1"
	b.ProjectID = "proj-1"
	b.NodeID = "node-1"
	b.PublicHost = "203.0.113.10"
	b.SSHPort = 22
	b.Role = "first_server"
	b.AgentRegistrationToken = "areg-secret"
	b.SSH.AuthMethod = "password"
	b.SSH.Username = "root"
	b.SSH.Password = "ssh-secret"
	return b
}

type fakeExecutor struct {
	connectErr   bool
	failStage    string
	secretOutput bool
}

func (f *fakeExecutor) Connect(context.Context, RemoteTarget) (RemoteSession, error) {
	if f.connectErr {
		return nil, errors.New("dial tcp: password=ssh-secret")
	}
	return &fakeSession{executor: f}, nil
}

type fakeSession struct {
	executor *fakeExecutor
}

func (s *fakeSession) Run(_ context.Context, spec CommandSpec) (CommandResult, error) {
	stage := stageForScript(spec.Script)
	if s.executor.failStage == stage {
		out := "failed"
		if s.executor.secretOutput {
			out = "password=ssh-secret token=areg-secret private_key=private-key-secret agent_token=agent-secret pat=pat-secret kubeconfig=kubeconfig-secret app_secret=app-secret"
		}
		return CommandResult{ExitCode: 1, Stdout: out, Stderr: out}, errors.New(out)
	}
	return CommandResult{}, nil
}

func (s *fakeSession) Close() error { return nil }

func stageForScript(script string) string {
	switch script {
	case preflightScript:
		return "preflight"
	case installK3sScript:
		return "installing_k3s"
	case installAgentScript:
		return "installing_agent"
	case registerAgentScript:
		return "registering_agent"
	default:
		return "unknown"
	}
}
