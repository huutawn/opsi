package actionplane

import (
	"testing"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func TestPolicyCatalogAndDeterministicRisk(t *testing.T) {
	if RiskFor(actionv1.ActionIncidentResolve, actionv1.ActionParameters{IncidentResolve: &actionv1.IncidentResolveParameters{IncidentID: "i"}}, actionv1.CurrentState{}) != actionv1.RiskR1 {
		t.Fatal("incident resolve risk is not R1")
	}
	if RiskFor(actionv1.ActionScaleWorkload, actionv1.ActionParameters{ScaleWorkload: &actionv1.ScaleWorkloadParameters{Replicas: 0}}, actionv1.CurrentState{}) != actionv1.RiskR3 {
		t.Fatal("scale-to-zero is not R3")
	}
	if RiskFor(actionv1.ActionRestartWorkload, actionv1.ActionParameters{RestartWorkload: &actionv1.RestartWorkloadParameters{}}, actionv1.CurrentState{}) != actionv1.RiskR2 {
		t.Fatal("restart risk is not R2")
	}
	if err := ValidateRisk(actionv1.RiskR4); err == nil {
		t.Fatal("R4 was not rejected")
	}
	if len(Catalog()) != 4 {
		t.Fatalf("catalog size = %d", len(Catalog()))
	}
}

func TestPolicyRejectsUnknownOrProhibitedAction(t *testing.T) {
	if err := ValidateKind(actionv1.ActionKind("deploy")); err == nil {
		t.Fatal("deploy entered ActionPlane catalog")
	}
	if err := ValidateKind(actionv1.ActionKind("shell")); err == nil {
		t.Fatal("shell entered ActionPlane catalog")
	}
}
