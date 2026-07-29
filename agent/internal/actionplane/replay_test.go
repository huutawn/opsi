package actionplane

import (
	"context"
	"sync"
	"testing"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

type replayBarrierDevices struct {
	device       Device
	mu           sync.Mutex
	calls        int
	staleLoaded  chan struct{}
	releaseStale chan struct{}
}

func (d *replayBarrierDevices) Resolve(context.Context, string, string, string) (Device, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	if call == 1 {
		close(d.staleLoaded)
		<-d.releaseStale
	}
	return d.device, nil
}

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

func TestStaleConcurrentReplayReturnsExactTerminalWithoutLockOrEvents(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	barrier := &replayBarrierDevices{device: service.device, staleLoaded: make(chan struct{}), releaseStale: make(chan struct{})}
	service.Devices = barrier
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	request := &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant}
	staleResult := make(chan *actionv1.ActionResult, 1)
	staleErr := make(chan error, 1)
	go func() {
		result, executeErr := service.Execute(context.Background(), request)
		staleResult <- result
		staleErr <- executeErr
	}()
	<-barrier.staleLoaded
	first, err := service.Execute(context.Background(), request)
	if err != nil || first.Status != actionv1.StatusSucceeded {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	close(barrier.releaseStale)
	second := <-staleResult
	if err := <-staleErr; err != nil {
		t.Fatal(err)
	}
	if second == nil || second.Status != first.Status || second.Message != first.Message || second.FinishedAt != first.FinishedAt {
		t.Fatalf("stale replay=%+v first=%+v", second, first)
	}
	store := service.Store.(*SQLiteStore)
	if got := lockCount(t, store, preflight.Plan.ID); got != 0 {
		t.Fatalf("terminal replay lock count=%d", got)
	}
	assertEventCounts(t, store, preflight.Plan.ID, map[actionv1.ActionStatus]int{
		actionv1.StatusPlanned: 1, actionv1.StatusPreflighted: 1, actionv1.StatusApproved: 1,
		actionv1.StatusExecuting: 1, actionv1.StatusSucceeded: 1,
	})
	if len(runtime.calls) != 1 {
		t.Fatalf("executor calls=%v", runtime.calls)
	}

	third, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	thirdResult, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: third.Challenge.ID, Grant: signGrant(t, third.Challenge, service)})
	if err != nil || thirdResult.Status != actionv1.StatusSucceeded || len(runtime.calls) != 2 {
		t.Fatalf("next action=%+v err=%v calls=%v", thirdResult, err, runtime.calls)
	}
}

func assertEventCounts(t *testing.T, store *SQLiteStore, actionID string, want map[actionv1.ActionStatus]int) {
	t.Helper()
	rows, err := store.db.Query(`SELECT status, COUNT(*) FROM action_plane_events WHERE action_id=? GROUP BY status`, actionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[actionv1.ActionStatus]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			t.Fatal(err)
		}
		got[actionv1.ActionStatus(status)] = count
	}
	for status, count := range want {
		if got[status] != count {
			t.Fatalf("event %s count=%d want=%d all=%v", status, got[status], count, got)
		}
	}
}
