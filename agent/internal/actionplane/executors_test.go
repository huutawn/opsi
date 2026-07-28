package actionplane

import (
	"context"
	"testing"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func TestTypedExecutorDispatchHasNoCommandStringPath(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	for _, request := range []*actionv1.PreflightRequest{fixtureRequest(actionv1.ActionRestartWorkload), fixtureRequest(actionv1.ActionScaleWorkload), fixtureRequest(actionv1.ActionGatewayReconcile), fixtureRequest(actionv1.ActionIncidentResolve)} {
		plan := actionv1.ActionPlan{Kind: request.Kind, Target: request.Target, Parameters: request.Parameters}
		if err := executeTyped(context.Background(), runtime, plan); err != nil {
			t.Fatal(err)
		}
	}
	if len(runtime.calls) != 4 {
		t.Fatalf("typed executor calls=%v", runtime.calls)
	}
}
