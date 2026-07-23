package deploy

import (
	"errors"
	"testing"
)

func TestRequestRequiresImmutableCommand(t *testing.T) {
	if err := (Request{}).Validate(); !errors.Is(err, ErrLegacyDeploymentRetired) {
		t.Fatalf("error = %v", err)
	}
}

func TestRequestAcceptsCanonicalCommand(t *testing.T) {
	command := testAgentCommand(t)
	if err := (Request{Production: &command}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyFailure(t *testing.T) {
	if decision := ClassifyFailure("rollout timeout", false, 0); !decision.RollbackSafe || decision.FailType != FailTypeDeployTimeFail {
		t.Fatalf("expected deploy-time rollback safe: %+v", decision)
	}
	if decision := ClassifyFailure("OOMKilled", false, 0); decision.RollbackSafe || decision.FailType != FailTypeResourceExhaustion {
		t.Fatalf("expected resource exhaustion no rollback: %+v", decision)
	}
	if decision := ClassifyFailure("database connection refused", false, 0); decision.RollbackSafe || decision.FailType != FailTypeExternalDependency {
		t.Fatalf("expected dependency no rollback: %+v", decision)
	}
	if decision := ClassifyFailure("crashloop", true, 6); decision.RollbackSafe || decision.FailType != FailTypeRuntimeCrash {
		t.Fatalf("expected runtime no rollback: %+v", decision)
	}
}
