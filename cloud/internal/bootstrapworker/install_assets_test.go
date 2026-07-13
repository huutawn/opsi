package bootstrapworker

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestBootstrapPlanV2Contract(t *testing.T) {
	cfg := testInstallConfig()
	bundle := testLease("boot-1", "host-1").Bundle
	plan, err := BuildBootstrapPlan(cfg, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != registry.FirstServerBootstrapPlanVersionV2 {
		t.Fatalf("plan version=%q", plan.Version)
	}
	wantIDs := []string{"preflight", "install_k3s", "install_agent", "register_agent"}
	gotIDs := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		gotIDs = append(gotIDs, step.ID)
		if strings.Contains(step.Command.Script, "curl -sfL") || regexp.MustCompile(`curl[^|\n]*\|[[:space:]]*(sh|bash)`).MatchString(step.Command.Script) {
			t.Fatalf("unsafe installer pipeline remains in %s", step.ID)
		}
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("step IDs=%v want=%v", gotIDs, wantIDs)
	}
	if !strings.Contains(installK3sScript, "download_file \"$OPSI_K3S_INSTALLER_URL\" \"$installer\"") {
		t.Fatal("K3s installer is not downloaded before execution")
	}
	checksumAt := strings.Index(installK3sScript, "sha256sum --check")
	executeAt := strings.Index(installK3sScript, "sh \"$installer\"")
	if checksumAt < 0 || executeAt < 0 || checksumAt > executeAt {
		t.Fatalf("K3s checksum/execution order invalid: checksum=%d execute=%d", checksumAt, executeAt)
	}
	for _, required := range []string{
		"INSTALL_K3S_VERSION=\"$OPSI_K3S_VERSION\"",
		"INSTALL_K3S_EXEC='server --write-kubeconfig-mode 0640'",
		"systemctl is-active --quiet k3s",
		"k3s kubectl get nodes",
	} {
		if !strings.Contains(installK3sScript, required) {
			t.Fatalf("K3s script missing %q", required)
		}
	}
	if !strings.Contains(installAgentScript, "/opt/opsi/agent/releases") || !strings.Contains(installAgentScript, "$OPSI_AGENT_SHA256") {
		t.Fatal("Agent release path is not checksum-addressed")
	}
	if strings.Contains(installAgentScript, "current_link") || strings.Contains(installAgentScript, "systemctl restart opsi-agent") {
		t.Fatal("install_agent activates or restarts the Agent")
	}
	for _, required := range []string{
		"mv -Tf \"$tmp_link\" \"$link\"",
		"atomic_link \"releases/$OPSI_AGENT_SHA256\" \"$current_link\"",
		"ExecStart=/opt/opsi/agent/current/opsi-agent --config /etc/opsi/agent.yaml",
	} {
		if !strings.Contains(registerAgentScript, required) {
			t.Fatalf("register_agent missing %q", required)
		}
	}
}

func TestRemoteScriptsPassShellSyntaxCheck(t *testing.T) {
	for name, script := range map[string]string{
		"preflight":      preflightScript,
		"install_k3s":    installK3sScript,
		"install_agent":  installAgentScript,
		"register_agent": registerAgentScript,
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("sh", "-n")
			cmd.Stdin = strings.NewReader(script)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("sh -n failed: %v: %s", err, output)
			}
		})
	}
}

func TestBootstrapSystemdUnitMatchesPackagingAsset(t *testing.T) {
	path := filepath.Join("..", "..", "..", "agent", "packaging", "systemd", "opsi-agent.service")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(string(data), "\n") != strings.TrimSuffix(opsiAgentSystemdUnit, "\n") {
		t.Fatal("Bootstrap Worker systemd unit differs from packaging asset")
	}
}

func TestBootstrapPlanFingerprintInputsAndSecrets(t *testing.T) {
	cfg := testInstallConfig()
	bundle := testLease("boot-1", "host-1").Bundle
	plan, err := BuildBootstrapPlan(cfg, bundle)
	if err != nil {
		t.Fatal(err)
	}
	base := BootstrapPlanFingerprint(cfg, plan)
	for name, mutate := range map[string]func(*Config){
		"K3s version":     func(c *Config) { c.K3sVersion = "v1.32.6+k3s1" },
		"K3s URL":         func(c *Config) { c.K3sInstallerURL += "/changed" },
		"K3s checksum":    func(c *Config) { c.K3sInstallerSHA256 = strings.Repeat("c", 64) },
		"Agent URL":       func(c *Config) { c.AgentInstallURL += "/changed" },
		"Agent checksum":  func(c *Config) { c.AgentInstallSHA256 = strings.Repeat("d", 64) },
		"Agent Cloud URL": func(c *Config) { c.AgentCloudURL = "https://other-cloud.example" },
	} {
		t.Run(name, func(t *testing.T) {
			changedCfg := cfg
			mutate(&changedCfg)
			changedPlan, err := BuildBootstrapPlan(changedCfg, bundle)
			if err != nil {
				t.Fatal(err)
			}
			if BootstrapPlanFingerprint(changedCfg, changedPlan) == base {
				t.Fatal("immutable input did not change fingerprint")
			}
		})
	}
	for name, mutate := range map[string]func(*Config, *Bundle){
		"registration token": func(_ *Config, b *Bundle) { b.AgentRegistrationToken = "different-registration-token" },
		"SSH password":       func(_ *Config, b *Bundle) { b.SSH.Password = "different-password" },
		"SSH private key":    func(_ *Config, b *Bundle) { b.SSH.PrivateKey = "different-private-key" },
		"worker token":       func(c *Config, _ *Bundle) { c.BootstrapWorkerToken = "different-worker-token" },
	} {
		t.Run(name, func(t *testing.T) {
			changedCfg, changedBundle := cfg, bundle
			mutate(&changedCfg, &changedBundle)
			changedPlan, err := BuildBootstrapPlan(changedCfg, changedBundle)
			if err != nil {
				t.Fatal(err)
			}
			if BootstrapPlanFingerprint(changedCfg, changedPlan) != base {
				t.Fatal("secret changed fingerprint")
			}
		})
	}
	changedPlan := plan
	changedPlan.Steps = append([]BootstrapStep(nil), plan.Steps...)
	changedPlan.Steps[0].ID = "changed-step"
	if BootstrapPlanFingerprint(cfg, changedPlan) == base {
		t.Fatal("ordered step IDs did not change fingerprint")
	}
	changedPlan = plan
	changedPlan.Steps = append([]BootstrapStep(nil), plan.Steps...)
	changedPlan.Steps[3].Command.Script = strings.Replace(registerAgentScript, "Description=Opsi Agent", "Description=Changed", 1)
	if BootstrapPlanFingerprint(cfg, changedPlan) == base {
		t.Fatal("systemd unit/remote command content did not change fingerprint")
	}
}

func TestRemoteInstallIdempotencyContracts(t *testing.T) {
	contracts := map[string][]string{
		"K3s exact version skips reinstall": {
			"if [ \"$current_version\" != \"$OPSI_K3S_VERSION\" ]; then",
			"systemctl enable --now k3s",
		},
		"K3s mismatch verified upgrade": {
			"download_file \"$OPSI_K3S_INSTALLER_URL\" \"$installer\"",
			"sha256sum --check",
			"INSTALL_K3S_VERSION=\"$OPSI_K3S_VERSION\"",
		},
		"existing release reuse": {
			"if $SUDO test -e \"$release\"; then",
			"AGENT_RELEASE_INTEGRITY_FAILED",
			"exit 0",
		},
		"registration marker replay": {
			"if $SUDO test -e \"$marker\"; then",
			"AGENT_REGISTRATION_IDENTITY_MISMATCH",
			"AGENT_REGISTRATION_STATE_INVALID",
			"else\n\tpayload=",
		},
		"atomic state writes": {
			"config_stage=",
			"marker_stage=",
			"unit_stage=",
			"mv -Tf",
		},
		"rollback and retry activation": {
			"atomic_link \"releases/$OPSI_AGENT_SHA256\" \"$current_link\"",
			"AGENT_SERVICE_START_FAILED",
			"AGENT_ROLLBACK_FAILED",
		},
	}
	for name, required := range contracts {
		t.Run(name, func(t *testing.T) {
			script := registerAgentScript
			if strings.HasPrefix(name, "K3s") {
				script = installK3sScript
			} else if name == "existing release reuse" {
				script = installAgentScript
			}
			for _, value := range required {
				if !strings.Contains(script, value) {
					t.Fatalf("script missing %q", value)
				}
			}
		})
	}
}

func TestFirstServerV1CheckpointFailsClosedAgainstV2Plan(t *testing.T) {
	cfg := testInstallConfig()
	plan, err := BuildBootstrapPlan(cfg, testLease("boot-1", "host-1").Bundle)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := registry.BootstrapCheckpoint{
		SchemaVersion:   registry.BootstrapCheckpointSchemaVersion,
		PlanVersion:     registry.FirstServerBootstrapPlanVersion,
		PlanFingerprint: strings.Repeat("a", 64),
	}
	if err := ValidateBootstrapCheckpoint(plan, BootstrapPlanFingerprint(cfg, plan), checkpoint); !errors.Is(err, ErrBootstrapPlanMismatch) {
		t.Fatalf("v1 checkpoint error=%v", err)
	}
}

func TestStepFailureClassification(t *testing.T) {
	for _, testCase := range []struct {
		code      string
		retryable bool
	}{
		{"K3S_INSTALLER_CHECKSUM_MISMATCH", false},
		{"K3S_VERSION_VERIFICATION_FAILED", true},
		{"AGENT_INSTALL_CHECKSUM_MISMATCH", false},
		{"AGENT_RELEASE_INTEGRITY_FAILED", false},
		{"AGENT_REGISTRATION_IDENTITY_MISMATCH", false},
		{"AGENT_REGISTRATION_STATE_INVALID", false},
		{"AGENT_SERVICE_START_FAILED", true},
		{"AGENT_ROLLBACK_FAILED", false},
	} {
		failure := classifyStepFailure("registering_agent", "remote step failed: "+testCase.code)
		if failure.Code != testCase.code || failure.Retryable != testCase.retryable {
			t.Fatalf("classification=%+v want code=%s retryable=%v", failure, testCase.code, testCase.retryable)
		}
	}
}

func testInstallConfig() Config {
	return Config{
		CloudURL:             "https://cloud-internal.example",
		AgentCloudURL:        "https://cloud.example",
		BootstrapWorkerToken: "worker-token",
		WorkerID:             "worker-1",
		K3sVersion:           "v1.32.5+k3s1",
		K3sInstallerURL:      "https://get.k3s.io",
		K3sInstallerSHA256:   strings.Repeat("b", 64),
		AgentInstallURL:      "https://downloads.example/opsi-agent",
		AgentInstallSHA256:   strings.Repeat("a", 64),
	}
}
