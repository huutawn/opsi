package nodelifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls [][]string
	fail  map[string]error
	out   map[string]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	key := strings.Join(args, " ")
	if err := f.fail[key]; err != nil {
		return []byte("password=secret-token"), err
	}
	if out := f.out[key]; out != "" {
		return []byte(out), nil
	}
	return []byte(`{"metadata":{"name":"node-a"},"spec":{"unschedulable":true}}`), nil
}

func TestDrainExecutesTypedKubectlAndVerifies(t *testing.T) {
	runner := &fakeRunner{fail: map[string]error{}, out: map[string]string{}}
	result := (Service{Runner: runner}).Execute(context.Background(), Request{Action: ActionDrain, TargetNodeID: "node-1", TargetNodeName: "node-a"})
	if result.Status != StatusCompleted || !result.Verified {
		t.Fatalf("want verified complete, got %+v", result)
	}
	got := strings.Join(runner.calls[1], " ")
	if got != "kubectl cordon node-a" {
		t.Fatalf("unexpected command: %s", got)
	}
	if strings.Contains(strings.Join(runner.calls[2], " "), "sh -c") {
		t.Fatal("arbitrary shell path used")
	}
}

func TestInvalidTargetFailsBeforeKubectl(t *testing.T) {
	runner := &fakeRunner{fail: map[string]error{}, out: map[string]string{}}
	result := (Service{Runner: runner}).Execute(context.Background(), Request{Action: ActionDrain, TargetNodeID: "node-1", TargetNodeName: "../bad"})
	if result.Status != StatusFailed || result.FailureCode != "INVALID_NODE_TARGET" {
		t.Fatalf("want invalid target failure, got %+v", result)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("kubectl called for invalid target: %+v", runner.calls)
	}
}

func TestUnsupportedCannotReturnSuccess(t *testing.T) {
	result := (Service{}).Execute(context.Background(), Request{Action: "shell", TargetNodeID: "node-1", TargetNodeName: "node-a"})
	if result.Status != StatusUnsupported || result.Verified {
		t.Fatalf("want unsupported unverified, got %+v", result)
	}
}

func TestK3sFailureIsFailedAndRedacted(t *testing.T) {
	runner := &fakeRunner{fail: map[string]error{"drain node-a --ignore-daemonsets --delete-emptydir-data --timeout 10m0s": errors.New("boom")}, out: map[string]string{}}
	result := (Service{Runner: runner}).Execute(context.Background(), Request{Action: ActionDrain, TargetNodeID: "node-1", TargetNodeName: "node-a"})
	if result.Status != StatusFailed || result.FailureCode != "K3S_DRAIN_FAILED" {
		t.Fatalf("want drain failure, got %+v", result)
	}
	if strings.Contains(result.FailureMessageRedacted, "secret-token") || strings.Contains(result.FailureMessageRedacted, "password") {
		t.Fatalf("sensitive failure leaked: %q", result.FailureMessageRedacted)
	}
}

func TestRemoveRequiresExplicitIntent(t *testing.T) {
	result := (Service{}).Execute(context.Background(), Request{Action: ActionRemove, TargetNodeID: "node-1", TargetNodeName: "node-a"})
	if result.Status != StatusFailed || result.FailureCode != "REMOVE_INTENT_REQUIRED" {
		t.Fatalf("want explicit intent failure, got %+v", result)
	}
}
