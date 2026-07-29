package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
)

func TestIncidentEvidenceConstants(t *testing.T) {
	if MaxIncidentEvidenceBytes != 256<<10 || MaxTimelineEntries != 128 || MaxKubernetesEventEntries != 64 || MaxLogFingerprintGroups != 64 || MaxRedactedExcerptBytes != 512 || MaxAuditReferences != 64 || MaxEvidenceSourceWindow != 30*time.Minute || MaxKubernetesCommandDuration != 5*time.Second || MaxEvidenceOperationDuration != 30*time.Second {
		t.Fatal("incident evidence bounds changed")
	}
}

func TestIncidentEvidenceIsCanonicalAcrossReorderedSources(t *testing.T) {
	created := time.Unix(1000, 0).UTC()
	record := telemetry.IncidentRecord{ID: "inc-1", ProjectID: "p1", ServiceID: "svc", NodeID: "node", PodID: "pod", Status: "open", CreatedAt: created}
	records := []telemetry.SyncRecord{
		{Log: &telemetry.LogRecord{ProjectID: "p1", ServiceID: "svc", PodID: "pod", Fingerprint: "fp-b", Level: "error", Message: "second", ObservedAt: created.Add(time.Second)}},
		{Log: &telemetry.LogRecord{ProjectID: "p1", ServiceID: "svc", PodID: "pod", Fingerprint: "fp-a", Level: "warn", Message: "first", ObservedAt: created}},
	}
	audits := []telemetry.EvidenceAuditRecord{{ID: "audit-b", Action: "deploy", ResourceID: "svc", CreatedAt: created.Add(time.Second)}, {ID: "audit-a", Action: "incident", ResourceID: "inc-1", CreatedAt: created}}
	rollout := &fakeRolloutEvidence{projection: &deploy.EvidenceProjection{RolloutID: "rollout-1", State: "failed", FailureCode: "RUNTIME_READINESS_FAILED", DesiredDigest: "sha256:" + strings.Repeat("a", 64), PreviousDigest: "sha256:" + strings.Repeat("b", 64), TotalEvents: 2, Events: []deploy.EvidenceEvent{{Version: 2, State: "failed", StateHash: strings.Repeat("d", 64), CreatedAt: created.Add(time.Second)}, {Version: 1, State: "waiting", StateHash: strings.Repeat("c", 64), CreatedAt: created}}}}
	kube := fakeKubernetesEvidence{result: KubernetesEvidenceResult{Pods: []agentv1.IncidentPodEvidence{{PodID: "pod", ReadyContainers: 0, TotalContainers: 1, RestartCount: 3, ObservedDigest: "sha256:" + strings.Repeat("e", 64)}}, ObservedDigest: "sha256:" + strings.Repeat("e", 64)}}
	build := func(records []telemetry.SyncRecord, audits []telemetry.EvidenceAuditRecord) *agentv1.IncidentEvidence {
		source := &fakeEvidenceSource{records: records, audits: audits}
		evidence, err := (IncidentContextBuilder{Store: source, Rollouts: rollout, Kubernetes: kube, Now: func() time.Time { return time.Unix(2000, 0).UTC() }}).Build(context.Background(), record)
		if err != nil {
			t.Fatal(err)
		}
		return evidence
	}
	first := build(records, audits)
	second := build([]telemetry.SyncRecord{records[1], records[0]}, []telemetry.EvidenceAuditRecord{audits[1], audits[0]})
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) || first.ContentSHA256 != second.ContentSHA256 {
		t.Fatalf("reordered sources changed evidence:\n%s\n%s", firstJSON, secondJSON)
	}
	if first.Deployment.DesiredDigest == "" || first.Deployment.ObservedDigest == "" || len(first.Rollout.EventCorrelation) != 2 || len(first.AuditReferences) != 2 {
		t.Fatalf("factual rollout/audit evidence missing: %+v", first)
	}
}

func TestIncidentEvidenceBoundsUTF8SecretsAndUntrustedContent(t *testing.T) {
	created := time.Unix(1000, 0).UTC()
	privateKeyCanary := "-----BEGIN PRIVATE" + " KEY-----\nprivate-canary\n-----END PRIVATE" + " KEY-----"
	canaries := []string{"Authorization: Bearer bearer-canary", "password=password-canary", "token=token-canary", "otp=otp-canary", privateKeyCanary, "kubeconfig", "registry credential"}
	var records []telemetry.SyncRecord
	var events []agentv1.IncidentKubernetesEvent
	var audits []telemetry.EvidenceAuditRecord
	for index := 0; index < 70; index++ {
		message := strings.Repeat("界", 400) + " " + canaries[index%len(canaries)]
		records = append(records, telemetry.SyncRecord{Log: &telemetry.LogRecord{Fingerprint: fmt.Sprintf("fp-%03d", index), Level: "error", Message: message, ObservedAt: created.Add(time.Duration(index) * time.Second)}})
		events = append(events, agentv1.IncidentKubernetesEvent{ObservedAtUnix: created.Add(time.Duration(index) * time.Second).Unix(), ObjectName: "pod", Reason: "Failed", Message: message, UntrustedContent: true})
		audits = append(audits, telemetry.EvidenceAuditRecord{ID: "audit-" + string(rune('a'+index%26)), Action: "deploy", ResourceID: "svc", CreatedAt: created.Add(time.Duration(index) * time.Second)})
	}
	source := &fakeEvidenceSource{records: records, audits: audits}
	kube := fakeKubernetesEvidence{result: KubernetesEvidenceResult{Events: events}}
	evidence, err := (IncidentContextBuilder{Store: source, Kubernetes: kube, Now: func() time.Time { return time.Unix(2000, 0).UTC() }, Window: MaxEvidenceSourceWindow}).Build(context.Background(), telemetry.IncidentRecord{ID: "inc", ProjectID: "p1", ServiceID: "svc", Status: "open", CreatedAt: created})
	if err != nil {
		t.Fatal(err)
	}
	body, err := EncodeIncidentEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > MaxIncidentEvidenceBytes || len(evidence.LogFingerprints) > MaxLogFingerprintGroups || len(evidence.KubernetesEvents) > MaxKubernetesEventEntries || len(evidence.Timeline) > MaxTimelineEntries || len(evidence.AuditReferences) > MaxAuditReferences || len(evidence.Truncations) == 0 {
		t.Fatalf("bounds not enforced: bytes=%d logs=%d events=%d timeline=%d audits=%d truncations=%+v", len(body), len(evidence.LogFingerprints), len(evidence.KubernetesEvents), len(evidence.Timeline), len(evidence.AuditReferences), evidence.Truncations)
	}
	for _, log := range evidence.LogFingerprints {
		if len(log.Excerpt) > MaxRedactedExcerptBytes || !utf8.ValidString(log.Excerpt) || !log.UntrustedContent {
			t.Fatalf("invalid bounded excerpt: %+v", log)
		}
	}
	for _, event := range evidence.KubernetesEvents {
		if !event.UntrustedContent {
			t.Fatalf("workload event is not marked untrusted: %+v", event)
		}
	}
	for _, canary := range []string{"bearer-canary", "password-canary", "token-canary", "otp-canary", "private-canary", "kubeconfig", "registry credential"} {
		if strings.Contains(strings.ToLower(string(body)), strings.ToLower(canary)) {
			t.Fatalf("secret canary leaked: %s", canary)
		}
	}
	if strings.Contains(string(body), "rca_result") || strings.Contains(string(body), "mitigation_actions_json") {
		t.Fatalf("legacy analysis/action fields leaked: %s", body)
	}
}

func TestIncidentEvidencePersistsAcrossConcurrentReadsAndRestart(t *testing.T) {
	path := t.TempDir() + "/telemetry.db"
	store, err := telemetry.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1000, 0).UTC()
	if err := store.InsertIncident(context.Background(), telemetry.IncidentRecord{ID: "inc-1", ProjectID: "p1", ServiceID: "svc", Status: "open", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: store, EvidenceBuilder: IncidentContextBuilder{Store: store, Now: func() time.Time { return time.Unix(2000, 0).UTC() }}}
	const readers = 12
	results := make([]*agentv1.IncidentEvidence, readers)
	errorsSeen := make([]error, readers)
	var wait sync.WaitGroup
	for index := range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], errorsSeen[index] = service.GetEvidence(context.Background(), IncidentRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "viewer", Role: "Viewer"})
		}()
	}
	wait.Wait()
	for index := range readers {
		if errorsSeen[index] != nil || results[index].ContentSHA256 != results[0].ContentSHA256 {
			t.Fatalf("concurrent read %d evidence=%+v err=%v", index, results[index], errorsSeen[index])
		}
	}
	record, err := store.GetIncident(context.Background(), "p1", "inc-1")
	if err != nil || record.Status != "open" || record.EvidenceJSON == "" || record.RCAResult != "" || record.MitigationActions != "[]" {
		t.Fatalf("unexpected persisted incident: %+v err=%v", record, err)
	}
	storedBody := record.EvidenceJSON
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := telemetry.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, err := (&Service{Store: reopened}).GetEvidence(context.Background(), IncidentRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "viewer", Role: "Viewer"})
	if err != nil || replayed.ContentSHA256 != results[0].ContentSHA256 {
		t.Fatalf("restart replay failed evidence=%+v err=%v", replayed, err)
	}
	reopenedRecord, _ := reopened.GetIncident(context.Background(), "p1", "inc-1")
	if reopenedRecord.EvidenceJSON != storedBody {
		t.Fatal("restart changed persisted evidence bytes")
	}
}

func TestIncidentEvidenceUnavailableAndCorruptStoredPairFailClosed(t *testing.T) {
	built, err := (IncidentContextBuilder{Now: func() time.Time { return time.Unix(20, 0).UTC() }}).Build(context.Background(), telemetry.IncidentRecord{ID: "inc", ProjectID: "p1", Status: "open", CreatedAt: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"audit", "kubernetes", "rollout", "telemetry"} {
		found := false
		for _, coverage := range built.Coverage {
			found = found || coverage.Source == source && coverage.Status == "unavailable" && coverage.ReasonCode != ""
		}
		if !found {
			t.Fatalf("missing unavailable coverage for %s: %+v", source, built.Coverage)
		}
	}
	store := &fakeStore{rec: telemetry.IncidentRecord{ID: "inc", ProjectID: "p1", Status: "open", EvidenceJSON: `{"schema_version":"opsi.incident_evidence.v1"}`, EvidenceSHA256: strings.Repeat("0", 64), EvidenceGeneratedAt: time.Unix(20, 0).UTC()}}
	_, err = (&Service{Store: store}).GetEvidence(context.Background(), IncidentRequest{ProjectID: "p1", IncidentID: "inc", UserID: "viewer", Role: "Viewer"})
	if !errors.Is(err, ErrEvidenceCorrupt) {
		t.Fatalf("corrupt evidence did not fail closed: %v", err)
	}
}

type fakeEvidenceSource struct {
	records []telemetry.SyncRecord
	audits  []telemetry.EvidenceAuditRecord
}

func (f *fakeEvidenceSource) IncidentEvidenceTelemetry(context.Context, string, string, string, string, time.Time, time.Time, int) (telemetry.IncidentEvidenceTelemetry, error) {
	logs := map[string]*telemetry.EvidenceLogGroup{}
	pods := map[string]*telemetry.EvidencePodRecord{}
	for _, record := range f.records {
		if record.Log != nil {
			group := logs[record.Log.Fingerprint]
			if group == nil {
				group = &telemetry.EvidenceLogGroup{Fingerprint: record.Log.Fingerprint, Level: record.Log.Level, FirstAt: record.Log.ObservedAt, LastAt: record.Log.ObservedAt, Excerpt: record.Log.Message}
				logs[record.Log.Fingerprint] = group
			}
			group.Count++
			if record.Log.ObservedAt.Before(group.FirstAt) {
				group.FirstAt, group.Excerpt = record.Log.ObservedAt, record.Log.Message
			}
			if record.Log.ObservedAt.After(group.LastAt) {
				group.LastAt = record.Log.ObservedAt
			}
		}
		if record.Metric != nil && record.Metric.PodID != "" {
			pod := pods[record.Metric.PodID]
			if pod == nil {
				pod = &telemetry.EvidencePodRecord{PodID: record.Metric.PodID, NodeID: record.Metric.NodeID}
				pods[record.Metric.PodID] = pod
			}
			if record.Metric.Name == "pod.ready" {
				pod.Ready = record.Metric.Value > 0
			}
			if record.Metric.Name == "pod.restart_count" && int32(record.Metric.Value) > pod.RestartCount {
				pod.RestartCount = int32(record.Metric.Value)
			}
		}
	}
	projection := telemetry.IncidentEvidenceTelemetry{TotalLogGroups: len(logs), TotalPods: len(pods)}
	for _, group := range logs {
		projection.LogGroups = append(projection.LogGroups, *group)
	}
	for _, pod := range pods {
		projection.Pods = append(projection.Pods, *pod)
	}
	sort.Slice(projection.LogGroups, func(i, j int) bool { return projection.LogGroups[i].Fingerprint < projection.LogGroups[j].Fingerprint })
	sort.Slice(projection.Pods, func(i, j int) bool { return projection.Pods[i].PodID < projection.Pods[j].PodID })
	return projection, nil
}

func (f *fakeEvidenceSource) EvidenceAuditRecords(context.Context, string, string, string, time.Time, time.Time, int) ([]telemetry.EvidenceAuditRecord, int, error) {
	return append([]telemetry.EvidenceAuditRecord(nil), f.audits...), len(f.audits), nil
}

type fakeRolloutEvidence struct{ projection *deploy.EvidenceProjection }

func (f *fakeRolloutEvidence) ReadIncidentEvidence(context.Context, string, string, time.Time, time.Time) (*deploy.EvidenceProjection, error) {
	return f.projection, nil
}

type fakeKubernetesEvidence struct {
	result KubernetesEvidenceResult
	err    error
}

func (f fakeKubernetesEvidence) Read(context.Context, string, string, string, string) (KubernetesEvidenceResult, error) {
	return f.result, f.err
}
