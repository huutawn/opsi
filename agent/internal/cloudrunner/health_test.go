package cloudrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestKubernetesHealthProbeReady(t *testing.T) {
	runner := &scriptedHealthRunner{responses: []healthCommandResponse{
		{output: []byte("ok\n")},
		{output: []byte(`{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`)},
	}}
	health := ProbeRuntime(context.Background(), KubernetesHealthProbe{KubectlPath: "/usr/local/bin/kubectl", Runner: runner})
	if !health.NodeReady || health.K3SStatus != K3SStatusReady {
		t.Fatalf("health = %+v", health)
	}
	want := [][]string{
		{"/usr/local/bin/kubectl", "--request-timeout=4s", "get", "--raw=/readyz"},
		{"/usr/local/bin/kubectl", "--request-timeout=4s", "get", "nodes", "-o", "json"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestKubernetesHealthProbeFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		responses []healthCommandResponse
		want      string
	}{
		{name: "k3s unavailable", responses: []healthCommandResponse{{err: errors.New("unavailable")}}, want: K3SStatusUnavailable},
		{name: "node not ready", responses: []healthCommandResponse{{output: []byte("ok")}, {output: []byte(`{"items":[{"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`)}}, want: K3SStatusNotReady},
		{name: "malformed output", responses: []healthCommandResponse{{output: []byte("ok")}, {output: []byte("not-json")}}, want: K3SStatusUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := ProbeRuntime(context.Background(), KubernetesHealthProbe{KubectlPath: "kubectl", Runner: &scriptedHealthRunner{responses: tt.responses}})
			if health.NodeReady || health.K3SStatus != tt.want {
				t.Fatalf("health = %+v", health)
			}
		})
	}
}

func TestProbeRuntimeTimeoutAndMissingProbeFailClosed(t *testing.T) {
	for name, probe := range map[string]HealthProbe{
		"missing": nil,
		"timeout": blockingHealthProbe{},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			health := ProbeRuntime(ctx, probe)
			if health.NodeReady || health.K3SStatus != K3SStatusUnavailable {
				t.Fatalf("health = %+v", health)
			}
		})
	}
}

func TestExecHealthCommandRunnerAllowsExactOutputBound(t *testing.T) {
	output, err := (ExecHealthCommandRunner{}).Run(context.Background(), os.Args[0], "-test.run=TestHealthCommandHelper", "--", "stdout", fmt.Sprint(MaxHealthCommandOutputLen))
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != MaxHealthCommandOutputLen {
		t.Fatalf("output length = %d", len(output))
	}
}

func TestExecHealthCommandRunnerFailsClosedAboveOutputBound(t *testing.T) {
	output, err := (ExecHealthCommandRunner{}).Run(context.Background(), os.Args[0], "-test.run=TestHealthCommandHelper", "--", "stdout", fmt.Sprint(MaxHealthCommandOutputLen+1))
	if !errors.Is(err, errHealthCommandOutputExceeded) || output != nil {
		t.Fatalf("output=%d err=%v", len(output), err)
	}
}

func TestExecHealthCommandRunnerBoundsLargeStderr(t *testing.T) {
	output, err := (ExecHealthCommandRunner{}).Run(context.Background(), os.Args[0], "-test.run=TestHealthCommandHelper", "--", "stderr", fmt.Sprint(MaxHealthCommandOutputLen*4))
	if !errors.Is(err, errHealthCommandOutputExceeded) || output != nil {
		t.Fatalf("output=%d err=%v", len(output), err)
	}
}

func TestExecHealthCommandRunnerTimeoutFailsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	output, err := (ExecHealthCommandRunner{}).Run(ctx, os.Args[0], "-test.run=TestHealthCommandHelper", "--", "block", "0")
	if err == nil || output != nil {
		t.Fatalf("output=%d err=%v", len(output), err)
	}
}

func TestTruncatedNodeJSONCannotBecomeReady(t *testing.T) {
	runner := &scriptedHealthRunner{responses: []healthCommandResponse{
		{output: []byte("ok")},
		{output: []byte(`{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)},
	}}
	health := ProbeRuntime(context.Background(), KubernetesHealthProbe{KubectlPath: "kubectl", Runner: runner})
	if health.NodeReady || health.K3SStatus != K3SStatusUnavailable {
		t.Fatalf("health = %+v", health)
	}
}

func TestHealthCommandHelper(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) < separator+3 {
		return
	}
	mode := os.Args[separator+1]
	var size int
	_, _ = fmt.Sscan(os.Args[separator+2], &size)
	switch mode {
	case "stdout":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", size))
	case "stderr":
		_, _ = os.Stderr.WriteString(strings.Repeat("x", size))
	case "block":
		select {}
	}
	os.Exit(0)
}

type healthCommandResponse struct {
	output []byte
	err    error
}

type scriptedHealthRunner struct {
	responses []healthCommandResponse
	calls     [][]string
}

func (r *scriptedHealthRunner) Run(_ context.Context, executable string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{executable}, args...))
	if len(r.responses) == 0 {
		return nil, errors.New("unexpected command")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.output, response.err
}

type blockingHealthProbe struct{}

func (blockingHealthProbe) Probe(ctx context.Context) (RuntimeHealth, error) {
	<-ctx.Done()
	return RuntimeHealth{}, ctx.Err()
}
