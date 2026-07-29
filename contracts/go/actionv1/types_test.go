package actionv1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPreflightRequestRejectsActorSpoofAndUnknownFields(t *testing.T) {
	for _, field := range []string{"origin", "requested_by", "approved_by", "command", "kubectl", "sql"} {
		body := `{"schema_version":"action.v1","project_id":"p1","node_id":"n1","service_id":"s1","target":{"project_id":"p1","node_id":"n1","service_id":"s1"},"kind":"restart_workload","parameters":{"restart_workload":{}},"` + field + `":"spoof"}`
		var request PreflightRequest
		if err := DecodeStrict([]byte(body), &request); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("field %q was accepted: %v", field, err)
		}
	}
}

func TestTypedParametersAndCatalogRejectProhibitedActions(t *testing.T) {
	valid := []ActionParameters{
		{RestartWorkload: &RestartWorkloadParameters{}},
		{ScaleWorkload: &ScaleWorkloadParameters{Replicas: 2}},
		{GatewayReconcile: &GatewayReconcileParameters{}},
		{IncidentResolve: &IncidentResolveParameters{IncidentID: "inc-1"}},
	}
	for _, parameters := range valid {
		if err := parameters.Validate(); err != nil {
			t.Fatalf("valid typed parameters rejected: %v", err)
		}
	}
	for _, kind := range []ActionKind{"deploy", "rollback", "retry_deployment", "apply_manifest", "build_source", "shell", "kubectl", "sql"} {
		if kind.Valid() {
			t.Fatalf("prohibited kind %q is valid", kind)
		}
	}
	if err := (ActionParameters{RestartWorkload: &RestartWorkloadParameters{}, ScaleWorkload: &ScaleWorkloadParameters{Replicas: 1}}).Validate(); err == nil {
		t.Fatal("multiple typed payloads were accepted")
	}
}

func TestValidateTTLAndTerminalFailureCode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	challenge := ApprovalChallenge{SchemaVersion: SchemaVersion, ID: "ch-1", ActionID: "a-1", ProjectID: "p1", PlanHash: strings.Repeat("a", 64), StateHash: strings.Repeat("b", 64), Nonce: "nonce", IssuedAt: now, ExpiresAt: now.Add(MaxChallengeTTL)}
	if err := challenge.Validate(now); err != nil {
		t.Fatal(err)
	}
	challenge.ExpiresAt = now.Add(MaxChallengeTTL + time.Second)
	if err := challenge.Validate(now); err == nil {
		t.Fatal("overlong challenge TTL accepted")
	}
	result := ActionResult{SchemaVersion: SchemaVersion, ActionID: "a-1", Status: StatusFailed}
	if err := result.Validate(); err == nil {
		t.Fatal("failed result without typed failure code accepted")
	}
}

func TestActionTypesSurviveJSONRoundTrip(t *testing.T) {
	plan := testPlan()
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ActionPlan
	if err := DecodeStrict(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Target.Key() != plan.Target.Key() || decoded.Parameters.ScaleWorkload.Replicas != 3 {
		t.Fatalf("round trip changed typed action: %#v", decoded)
	}
}

func TestRuntimeActionsRequireFullIdentityButIncidentResolveDoesNot(t *testing.T) {
	for _, kind := range []ActionKind{ActionRestartWorkload, ActionScaleWorkload, ActionGatewayReconcile} {
		request := PreflightRequest{SchemaVersion: SchemaVersion, ProjectID: "p1", NodeID: "n1", ServiceID: "s1", Target: TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1"}, Kind: kind}
		switch kind {
		case ActionRestartWorkload:
			request.Parameters.RestartWorkload = &RestartWorkloadParameters{}
		case ActionScaleWorkload:
			request.Parameters.ScaleWorkload = &ScaleWorkloadParameters{Replicas: 1}
		case ActionGatewayReconcile:
			request.Parameters.GatewayReconcile = &GatewayReconcileParameters{}
		}
		if err := request.Validate(); err == nil {
			t.Fatalf("%s accepted missing environment/runtime identity", kind)
		}
	}

	incident := PreflightRequest{SchemaVersion: SchemaVersion, ProjectID: "p1", NodeID: "n1", ServiceID: "s1", Target: TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1"}, Kind: ActionIncidentResolve, Parameters: ActionParameters{IncidentResolve: &IncidentResolveParameters{IncidentID: "inc-1"}}}
	if err := incident.Validate(); err != nil {
		t.Fatalf("incident resolve incorrectly requires Kubernetes runtime identity: %v", err)
	}
}

func testPlan() ActionPlan {
	now := time.Unix(1_800_000_000, 0).UTC()
	return ActionPlan{
		SchemaVersion:    SchemaVersion,
		ID:               "action-1",
		ProjectID:        "project-1",
		NodeID:           "node-1",
		ServiceID:        "service-1",
		Target:           TargetIdentity{ProjectID: "project-1", NodeID: "node-1", ServiceID: "service-1", EnvironmentID: "env-1", RuntimeID: "runtime-1"},
		Kind:             ActionScaleWorkload,
		Parameters:       ActionParameters{ScaleWorkload: &ScaleWorkloadParameters{Replicas: 3}},
		Origin:           OriginManualCLI,
		RequestedBy:      "user-1",
		Risk:             RiskR2,
		Preconditions:    []Condition{{Code: "OWNED_WORKLOAD", Summary: "owned"}},
		Postconditions:   []Condition{{Code: "READY_REPLICAS", Summary: "ready"}},
		CurrentStateHash: strings.Repeat("b", 64),
		IssuedAt:         now,
		ExpiresAt:        now.Add(MaxPlanTTL),
	}
}
