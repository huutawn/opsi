package depverifier

import (
	"context"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

type mockRunner struct {
	responses map[string]string // key: substring in command
}

func (m mockRunner) Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	for k, v := range m.responses {
		if strings.Contains(cmd, k) {
			return []byte(v), nil
		}
	}
	return []byte("ok"), nil
}

func TestVerifyPostgresSuccess(t *testing.T) {
	runner := mockRunner{
		responses: map[string]string{
			"SELECT 1": "1\n",
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

func TestVerifyAssertionMismatch(t *testing.T) {
	runner := mockRunner{
		responses: map[string]string{
			"SELECT 1": "1\n",
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
