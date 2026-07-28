package actionplane

import (
	"context"
	"strings"
	"testing"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

type fakeKubectl struct {
	calls  [][]string
	output []byte
	err    error
	wait   bool
}

func (f *fakeKubectl) Run(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.output, f.err
}

func TestKubernetesRuntimeUsesFixedTypedOperationsAndBoundedTimeout(t *testing.T) {
	runner := &fakeKubectl{}
	runtime := KubernetesRuntime{Runner: runner, KubectlPath: "kubectl", Timeout: time.Second}
	target := fixtureState().Target
	if err := runtime.RestartWorkload(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ScaleWorkload(context.Background(), target, 2); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.calls[0], " ") + "\n" + strings.Join(runner.calls[1], " ")
	if strings.Contains(joined, "sh -c") || strings.Contains(joined, "bash -c") || !strings.Contains(joined, "patch deployment") || !strings.Contains(joined, "--replicas=2") {
		t.Fatalf("unsafe kubectl args: %s", joined)
	}
	runner.wait = true
	runtime.Timeout = 10 * time.Millisecond
	if err := runtime.ScaleWorkload(context.Background(), target, 1); err == nil {
		t.Fatal("unbounded kubectl call succeeded")
	}
}

func TestKubernetesRuntimeRejectsInvalidScaleAndMalformedOutput(t *testing.T) {
	runner := &fakeKubectl{output: []byte(`{"metadata":`)}
	runtime := KubernetesRuntime{Runner: runner, Timeout: time.Second}
	if err := runtime.ScaleWorkload(context.Background(), fixtureState().Target, actionv1.MaxReplicas+1); err == nil {
		t.Fatal("invalid replica count accepted")
	}
	if _, err := runtime.CurrentState(context.Background(), fixtureState().Target, actionv1.ActionRestartWorkload, actionv1.ActionParameters{RestartWorkload: &actionv1.RestartWorkloadParameters{}}); err == nil {
		t.Fatal("malformed Kubernetes JSON accepted")
	}
}
