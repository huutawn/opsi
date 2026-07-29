package actionplane

import (
	"context"
	"sync"
	"testing"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func TestConcurrentExecuteHasAtMostOneExecutorAndLocksTarget(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	requests := make([]*actionv1.ExecuteRequest, 2)
	for i := range requests {
		requests[i] = &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant}
	}
	var wg sync.WaitGroup
	results := make(chan *actionv1.ActionResult, 2)
	for _, request := range requests {
		wg.Add(1)
		go func(req *actionv1.ExecuteRequest) {
			defer wg.Done()
			result, _ := service.Execute(context.Background(), req)
			results <- result
		}(request)
	}
	wg.Wait()
	close(results)
	if len(runtime.calls) > 1 {
		t.Fatalf("executor called %d times", len(runtime.calls))
	}
}

type blockingRuntime struct {
	*fakeRuntime
	entered chan struct{}
	release chan struct{}
}

func (r *blockingRuntime) RestartWorkload(context.Context, actionv1.TargetIdentity) error {
	r.mu.Lock()
	r.calls = append(r.calls, actionv1.ActionRestartWorkload)
	r.mu.Unlock()
	close(r.entered)
	<-r.release
	return nil
}

func TestDifferentActionOnLockedTargetIsRejected(t *testing.T) {
	runtime := &blockingRuntime{fakeRuntime: &fakeRuntime{state: fixtureState()}, entered: make(chan struct{}), release: make(chan struct{})}
	service := newTestService(t, runtime.fakeRuntime, false)
	service.Runtime = runtime
	first, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionScaleWorkload))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, executeErr := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: first.Challenge.ID, Grant: signGrant(t, first.Challenge, service)})
		done <- executeErr
	}()
	select {
	case <-runtime.entered:
	case <-time.After(time.Second):
		t.Fatal("first action did not enter executor")
	}
	result, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: second.Challenge.ID, Grant: signGrant(t, second.Challenge, service)})
	if err != nil || result.FailureCode != actionv1.FailureTargetLocked {
		t.Fatalf("locked result=%#v err=%v", result, err)
	}
	close(runtime.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.calls) != 1 {
		t.Fatalf("executor calls=%v", runtime.calls)
	}
}
