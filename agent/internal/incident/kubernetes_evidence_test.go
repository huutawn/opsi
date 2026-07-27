package incident

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestKubernetesEvidenceSeparatesWarningsAndUsesOnlyApprovedGets(t *testing.T) {
	runner := &fakeKubernetesRunner{responses: map[string]kubectlResult{
		"get pods -A -o json":   {stdout: []byte(`{"items":[{"metadata":{"name":"pod-1","namespace":"default","labels":{"opsi.dev/project-id":"p1","opsi.dev/service-id":"svc"}},"spec":{"nodeName":"node-1"},"status":{"containerStatuses":[{"ready":true,"restartCount":2,"imageID":"repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}]}`), stderr: []byte("warning: local fake")},
		"get events -A -o json": {stdout: []byte(`{"items":[{"metadata":{"namespace":"default","creationTimestamp":"2026-07-27T01:00:00Z"},"involvedObject":{"kind":"Pod","name":"pod-1"},"type":"Warning","reason":"Failed","message":"Authorization: Bearer canary password=canary at 10.0.0.1"}]}`), stderr: []byte("warning: local fake")},
	}}
	result, err := (KubernetesEvidenceSource{Runner: runner}).Read(context.Background(), "p1", "svc", "node-1", "pod-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pods) != 1 || result.Pods[0].ReadyContainers != 1 || result.Pods[0].RestartCount != 2 || result.ObservedDigest == "" {
		t.Fatalf("unexpected pods: %+v", result)
	}
	if len(result.Events) != 1 || !result.Events[0].UntrustedContent || strings.Contains(result.Events[0].Message, "canary") || strings.Contains(result.Events[0].Message, "10.0.0.1") {
		t.Fatalf("unsafe event: %+v", result.Events)
	}
	want := []string{"kubectl get pods -A -o json", "kubectl get events -A -o json"}
	if strings.Join(runner.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("commands=%v", runner.calls)
	}
}

func TestKubernetesEvidenceIsStableAcrossReorderedInput(t *testing.T) {
	podA := `{"metadata":{"name":"pod-a","namespace":"default","labels":{"opsi.dev/project-id":"p1","opsi.dev/service-id":"svc"}},"spec":{"nodeName":"node-1"},"status":{"containerStatuses":[{"ready":true,"imageID":"repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}`
	podB := `{"metadata":{"name":"pod-b","namespace":"default","labels":{"opsi.dev/project-id":"p1","opsi.dev/service-id":"svc"}},"spec":{"nodeName":"node-1"},"status":{"containerStatuses":[{"ready":true,"imageID":"repo@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}}`
	eventA := `{"metadata":{"namespace":"default","creationTimestamp":"2026-07-27T01:00:00Z"},"involvedObject":{"kind":"Pod","name":"pod-a"},"type":"Warning","reason":"Failed","message":"a"}`
	eventB := `{"metadata":{"namespace":"default","creationTimestamp":"2026-07-27T01:00:00Z"},"involvedObject":{"kind":"Pod","name":"pod-b"},"type":"Warning","reason":"Failed","message":"b"}`
	read := func(pods, events string) KubernetesEvidenceResult {
		runner := &fakeKubernetesRunner{responses: map[string]kubectlResult{
			"get pods -A -o json":   {stdout: []byte(`{"items":[` + pods + `]}`)},
			"get events -A -o json": {stdout: []byte(`{"items":[` + events + `]}`)},
		}}
		result, err := (KubernetesEvidenceSource{Runner: runner}).Read(context.Background(), "p1", "svc", "node-1", "")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, second := read(podA+","+podB, eventA+","+eventB), read(podB+","+podA, eventB+","+eventA)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) || first.ObservedDigest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("reordered Kubernetes input changed evidence:\n%s\n%s", firstJSON, secondJSON)
	}
}

func TestKubernetesEvidenceFailsClosedOnMalformedOrOversizedOutput(t *testing.T) {
	validPods := []byte(`{"items":[]}`)
	for name, runner := range map[string]*fakeKubernetesRunner{
		"malformed": {responses: map[string]kubectlResult{"get pods -A -o json": {stdout: []byte(`{"items":`)}}},
		"stdout":    {responses: map[string]kubectlResult{"get pods -A -o json": {stdout: make([]byte, maxKubernetesCommandOutput+1)}}},
		"stderr":    {responses: map[string]kubectlResult{"get pods -A -o json": {stdout: validPods, stderr: make([]byte, maxKubernetesCommandOutput+1)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (KubernetesEvidenceSource{Runner: runner}).Read(context.Background(), "p1", "svc", "", ""); err == nil {
				t.Fatal("expected bounded parse failure")
			}
		})
	}
}

func TestKubernetesEvidenceCommandDeadlineIsBounded(t *testing.T) {
	runner := &fakeKubernetesRunner{deadlineOnly: true}
	started := time.Now()
	if _, err := (KubernetesEvidenceSource{Runner: runner}).Read(context.Background(), "p1", "svc", "", ""); err == nil {
		t.Fatal("expected fake timeout")
	}
	if runner.deadline <= 0 || runner.deadline > MaxKubernetesCommandDuration || time.Since(started) > time.Second {
		t.Fatalf("deadline=%s elapsed=%s", runner.deadline, time.Since(started))
	}
}

type kubectlResult struct {
	stdout []byte
	stderr []byte
	err    error
}

type fakeKubernetesRunner struct {
	responses    map[string]kubectlResult
	calls        []string
	deadlineOnly bool
	deadline     time.Duration
}

func (f *fakeKubernetesRunner) RunKubectlGet(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if deadline, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(deadline)
	}
	if f.deadlineOnly {
		return nil, nil, context.DeadlineExceeded
	}
	result, ok := f.responses[strings.Join(args, " ")]
	if !ok {
		return nil, nil, errors.New("unexpected kubectl command")
	}
	return result.stdout, result.stderr, result.err
}
