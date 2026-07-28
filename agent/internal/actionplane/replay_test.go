package actionplane

import (
	"context"
	"testing"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func TestConflictingReplayDoesNotExecuteAgain(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	if _, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant}); err != nil {
		t.Fatal(err)
	}
	grant.Signature[0] ^= 1
	if _, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant}); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	if len(runtime.calls) != 1 {
		t.Fatalf("conflicting replay executed: %v", runtime.calls)
	}
}
