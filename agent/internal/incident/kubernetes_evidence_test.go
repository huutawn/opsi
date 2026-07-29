package incident

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	ownedLabels = `"app.kubernetes.io/managed-by":"opsi","opsi.dev/project":"p1","opsi.dev/environment":"prod","opsi.dev/runtime":"runtime-1","opsi.dev/service":"svc"`
	digestA     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB     = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestKubernetesEvidenceUsesApplicationDigestAndOwnershipGraph(t *testing.T) {
	podA := ownedPod("pod-a", digestA, digestB)
	podB := ownedPod("pod-b", digestA, "")
	events := `{"items":[` +
		ownedEvent("default", "Pod", "pod-a", "pod") + `,` +
		ownedEvent("default", "ReplicaSet", "api-rs", "rs") + `,` +
		ownedEvent("default", "Deployment", "api", "deployment") + `,` +
		ownedEvent("default", "Service", "api", "service") + `,` +
		ownedEvent("other", "Deployment", "api", "wrong-namespace") + `,` +
		ownedEvent("default", "Pod", "other-pod", "wrong-service") + `]}`
	runner := &fakeKubernetesRunner{responses: ownedKubernetesResponses(podA+","+podB, events)}
	runner.responses["get pods -A -o json"] = kubectlResult{stdout: runner.responses["get pods -A -o json"].stdout, stderr: []byte("warning: local fake")}
	result, err := (KubernetesEvidenceSource{Runner: runner}).Read(context.Background(), "p1", "svc", "node-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservedDigest != digestA || result.CoverageStatus != "" || len(result.Pods) != 2 || result.Pods[0].ObservedDigest != digestA {
		t.Fatalf("digest result=%+v", result)
	}
	if len(result.Events) != 4 {
		t.Fatalf("events=%+v", result.Events)
	}
	for _, event := range result.Events {
		if event.Namespace != "default" || strings.Contains(event.Message, "wrong") {
			t.Fatalf("foreign event accepted: %+v", event)
		}
	}
	want := "kubectl get pods -A -o json|kubectl get deployments -A -o json|kubectl get replicasets -A -o json|kubectl get services -A -o json|kubectl get events -A -o json"
	if strings.Join(runner.calls, "|") != want {
		t.Fatalf("commands=%v", runner.calls)
	}
}

func TestKubernetesEvidenceMixedAndIncompleteApplicationDigestsArePartial(t *testing.T) {
	tests := []struct {
		name   string
		pods   string
		reason string
	}{
		{name: "mixed", pods: ownedPod("pod-a", digestA, "") + "," + ownedPod("pod-b", digestB, ""), reason: "MIXED_APPLICATION_DIGESTS"},
		{name: "missing application", pods: ownedPod("pod-a", "", digestA), reason: "APPLICATION_DIGEST_INCOMPLETE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := (KubernetesEvidenceSource{Runner: &fakeKubernetesRunner{responses: ownedKubernetesResponses(test.pods, `{"items":[]}`)}}).Read(context.Background(), "p1", "svc", "node-1", "")
			if err != nil || result.ObservedDigest != "" || result.CoverageStatus != "partial" || result.ReasonCode != test.reason {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestKubernetesEvidenceIsStableAcrossReorderedInput(t *testing.T) {
	read := func(pods string) KubernetesEvidenceResult {
		result, err := (KubernetesEvidenceSource{Runner: &fakeKubernetesRunner{responses: ownedKubernetesResponses(pods, `{"items":[]}`)}}).Read(context.Background(), "p1", "svc", "node-1", "")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	podA, podB := ownedPod("pod-a", digestA, ""), ownedPod("pod-b", digestB, "")
	first, second := read(podA+","+podB), read(podB+","+podA)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) || first.ObservedDigest != "" || first.ReasonCode != "MIXED_APPLICATION_DIGESTS" {
		t.Fatalf("reordered Kubernetes input changed evidence:\n%s\n%s", firstJSON, secondJSON)
	}
}

func TestKubernetesEvidenceResourceFailureIsPartialAndInvalidJSONFailsClosed(t *testing.T) {
	responses := ownedKubernetesResponses(ownedPod("pod-a", digestA, ""), `{"items":[]}`)
	responses["get replicasets -A -o json"] = kubectlResult{err: errors.New("unavailable")}
	result, err := (KubernetesEvidenceSource{Runner: &fakeKubernetesRunner{responses: responses}}).Read(context.Background(), "p1", "svc", "node-1", "")
	if err != nil || result.CoverageStatus != "partial" || result.ReasonCode != "KUBERNETES_RESOURCE_QUERY_PARTIAL" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for name, data := range map[string][]byte{
		"malformed": []byte(`{"items":`),
		"trailing":  []byte(`{"items":[]} {}`),
		"oversized": make([]byte, maxKubernetesCommandOutput+1),
	} {
		t.Run(name, func(t *testing.T) {
			invalid := ownedKubernetesResponses("", `{"items":[]}`)
			invalid["get pods -A -o json"] = kubectlResult{stdout: data}
			if _, err := (KubernetesEvidenceSource{Runner: &fakeKubernetesRunner{responses: invalid}}).Read(context.Background(), "p1", "svc", "", ""); err == nil {
				t.Fatal("invalid Kubernetes JSON accepted")
			}
		})
	}
}

func TestKubernetesEvidenceCommandDeadlineIsBounded(t *testing.T) {
	runner := &fakeKubernetesRunner{deadlineOnly: true}
	started := time.Now()
	_, err := (KubernetesEvidenceSource{Runner: runner}).Read(context.Background(), "p1", "svc", "", "")
	if err == nil {
		t.Fatal("deadline runner unexpectedly succeeded")
	}
	if runner.deadline <= 0 || runner.deadline > MaxKubernetesCommandDuration || time.Since(started) > time.Second {
		t.Fatalf("deadline=%s elapsed=%s", runner.deadline, time.Since(started))
	}
}

func ownedKubernetesResponses(pods, events string) map[string]kubectlResult {
	return map[string]kubectlResult{
		"get pods -A -o json":        {stdout: []byte(`{"items":[` + pods + `]}`)},
		"get deployments -A -o json": {stdout: []byte(`{"items":[{"metadata":{"name":"api","namespace":"default","labels":{` + ownedLabels + `}}}]}`)},
		"get replicasets -A -o json": {stdout: []byte(`{"items":[{"metadata":{"name":"api-rs","namespace":"default","labels":{` + ownedLabels + `},"ownerReferences":[{"kind":"Deployment","name":"api"}]}}]}`)},
		"get services -A -o json":    {stdout: []byte(`{"items":[{"metadata":{"name":"api","namespace":"default","labels":{` + ownedLabels + `}}}]}`)},
		"get events -A -o json":      {stdout: []byte(events)},
	}
}

func ownedPod(name, applicationDigest, sidecarDigest string) string {
	containers := ""
	if applicationDigest != "" {
		containers = `{"name":"app","ready":true,"restartCount":1,"imageID":"repo@` + applicationDigest + `"}`
	}
	if sidecarDigest != "" {
		if containers != "" {
			containers += ","
		}
		containers += `{"name":"sidecar","ready":true,"imageID":"sidecar@` + sidecarDigest + `"}`
	}
	return `{"metadata":{"name":"` + name + `","namespace":"default","labels":{` + ownedLabels + `},"ownerReferences":[{"kind":"ReplicaSet","name":"api-rs"}]},"spec":{"nodeName":"node-1"},"status":{"containerStatuses":[` + containers + `]}}`
}

func ownedEvent(namespace, kind, name, message string) string {
	return `{"metadata":{"namespace":"` + namespace + `","creationTimestamp":"2026-07-27T01:00:00Z"},"involvedObject":{"kind":"` + kind + `","name":"` + name + `"},"type":"Warning","reason":"Failed","message":"` + message + `"}`
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
		return nil, nil, errors.New("bounded")
	}
	result, ok := f.responses[strings.Join(args, " ")]
	if !ok {
		return nil, nil, errors.New("unexpected kubectl command")
	}
	return result.stdout, result.stderr, result.err
}
