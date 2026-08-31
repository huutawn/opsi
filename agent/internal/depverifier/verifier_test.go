package depverifier

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

type mockRunner struct {
	responses map[string]string // key: substring in command
	errors    map[string]error
	commands  []string
}

func (m *mockRunner) Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	m.commands = append(m.commands, cmd)
	if m.errors != nil {
		for k, err := range m.errors {
			if strings.Contains(cmd, k) {
				return nil, err
			}
		}
	}
	for k, v := range m.responses {
		if strings.Contains(cmd, k) {
			return []byte(v), nil
		}
	}
	return []byte("ok"), nil
}

func TestVerifyPostgresSuccess(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]string{
			"CREATE TEMP TABLE opsi_dependency_probe": "opsi-write-read\n",
			"get pods": "true\n",
			"curl":     "200",
		},
	}
	verifier := Verifier{Runner: runner}

	lease := cloudrelay.DepVerificationLease{
		ID:                    "lease-1",
		LeaseToken:            "tok-1",
		ProviderKind:          "postgres",
		ProviderServiceName:   "pg-svc",
		ProviderNamespace:     "opsi-managed",
		ConsumerServiceKey:    "app-svc",
		ConsumerNamespace:     "opsi-apps",
		BindingUsername:       "opsi_b_testuser",
		BindingPassword:       "test-secret-password-12345",
		BindingDatabase:       "opsi",
		AssertionPath:         "/healthz",
		AssertionExpectedCode: 200,
	}

	result := verifier.Verify(context.Background(), lease)
	if result.ConnectionStatus != verificationv1.LayerStatusVerified {
		t.Fatalf("expected connection VERIFIED, got %s", result.ConnectionStatus)
	}
	if result.ConsumerHealthStatus != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected consumer health HEALTHY, got %s", result.ConsumerHealthStatus)
	}
	if result.AssertionStatus != verificationv1.LayerStatusVerified {
		t.Fatalf("expected assertion VERIFIED, got %s", result.AssertionStatus)
	}
}

func TestVerifyValkeySuccess(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]string{
			"opsi:dependency-probe:": "PONG\nOK\nopsi-write-read\n",
			"get pods":               "true\n",
			"curl":                   "200",
		},
	}
	verifier := Verifier{Runner: runner}

	lease := cloudrelay.DepVerificationLease{
		ID:                    "lease-vk-1",
		LeaseToken:            "tok-vk-1",
		ProviderKind:          "valkey",
		ProviderServiceName:   "valkey-svc",
		ProviderNamespace:     "opsi-managed",
		ConsumerServiceKey:    "app-svc",
		ConsumerNamespace:     "opsi-apps",
		BindingUsername:       "opsi",
		BindingPassword:       "valkey-pass-123",
		AssertionPath:         "/healthz",
		AssertionExpectedCode: 200,
	}

	result := verifier.Verify(context.Background(), lease)
	if result.ConnectionStatus != verificationv1.LayerStatusVerified {
		t.Fatalf("expected connection VERIFIED, got %s", result.ConnectionStatus)
	}
	if result.ConsumerHealthStatus != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected consumer health HEALTHY, got %s", result.ConsumerHealthStatus)
	}
	if result.AssertionStatus != verificationv1.LayerStatusVerified {
		t.Fatalf("expected assertion VERIFIED, got %s", result.AssertionStatus)
	}
}

func TestVerifyPostgresFailure(t *testing.T) {
	runner := &mockRunner{
		errors: map[string]error{
			"CREATE TEMP TABLE opsi_dependency_probe": errors.New("psql: error: connection refused"),
		},
		responses: map[string]string{
			"get pods": "true\n",
		},
	}
	verifier := Verifier{Runner: runner}

	lease := cloudrelay.DepVerificationLease{
		ID:                  "lease-fail",
		LeaseToken:          "tok-fail",
		ProviderKind:        "postgres",
		ProviderServiceName: "pg-svc",
		ProviderNamespace:   "opsi-managed",
		ConsumerServiceKey:  "app-svc",
		ConsumerNamespace:   "opsi-apps",
	}

	result := verifier.Verify(context.Background(), lease)
	if result.ConnectionStatus != verificationv1.LayerStatusFailed {
		t.Fatalf("expected connection FAILED, got %s", result.ConnectionStatus)
	}
	if result.ConnectionFailCode != verificationv1.FailureConnectionFailed {
		t.Fatalf("expected fail code %s, got %s", verificationv1.FailureConnectionFailed, result.ConnectionFailCode)
	}
}

func TestVerifyValkeyFailure(t *testing.T) {
	runner := &mockRunner{
		errors: map[string]error{
			"opsi:dependency-probe:": errors.New("valkey-cli: connection refused"),
		},
		responses: map[string]string{
			"get pods": "true\n",
		},
	}
	verifier := Verifier{Runner: runner}

	lease := cloudrelay.DepVerificationLease{
		ID:                  "lease-vk-fail",
		LeaseToken:          "tok-vk-fail",
		ProviderKind:        "valkey",
		ProviderServiceName: "valkey-svc",
		ProviderNamespace:   "opsi-managed",
		ConsumerServiceKey:  "app-svc",
		ConsumerNamespace:   "opsi-apps",
	}

	result := verifier.Verify(context.Background(), lease)
	if result.ConnectionStatus != verificationv1.LayerStatusFailed {
		t.Fatalf("expected connection FAILED, got %s", result.ConnectionStatus)
	}
}

func TestVerifyAssertionMismatch(t *testing.T) {
	runner := &mockRunner{
		responses: map[string]string{
			"CREATE TEMP TABLE opsi_dependency_probe": "opsi-write-read\n",
			"get pods": "true\n",
			"curl":     "500",
		},
	}
	verifier := Verifier{Runner: runner}

	lease := cloudrelay.DepVerificationLease{
		ID:                    "lease-1",
		LeaseToken:            "tok-1",
		ProviderKind:          "postgres",
		ProviderServiceName:   "pg-svc",
		ConsumerServiceKey:    "app-svc",
		AssertionPath:         "/healthz",
		AssertionExpectedCode: 200,
	}

	result := verifier.Verify(context.Background(), lease)
	if result.AssertionStatus != verificationv1.LayerStatusFailed {
		t.Fatalf("expected assertion FAILED on status 500, got %s", result.AssertionStatus)
	}
	if result.AssertionStatusCode != 500 {
		t.Fatalf("expected status code 500, got %d", result.AssertionStatusCode)
	}
}

func TestCleanupStaleProbes(t *testing.T) {
	runner := &mockRunner{}
	verifier := Verifier{Runner: runner}

	err := verifier.CleanupStaleProbes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, cmd := range runner.commands {
		if strings.Contains(cmd, "delete pod -A -l opsi.dev/probe=dep-verification") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CleanupStaleProbes did not execute expected delete command: %v", runner.commands)
	}
}
