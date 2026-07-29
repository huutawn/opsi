package incident

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	"github.com/opsi-dev/opsi/agent/internal/telemetry"
)

func TestIncidentEvidenceIgnoresSecretLegacyContext(t *testing.T) {
	evidence, err := (IncidentContextBuilder{Now: func() time.Time { return time.Unix(20, 0).UTC() }}).Build(context.Background(), telemetry.IncidentRecord{
		ID:          "inc-1",
		ProjectID:   "p1",
		ContextJSON: `{"metric":{"password":"secret"}}`,
		CreatedAt:   time.Unix(10, 0),
	})
	body, _ := json.Marshal(evidence)
	if err != nil || strings.Contains(string(body), "password") || strings.Contains(string(body), "secret") {
		t.Fatalf("legacy context leaked into evidence: %s err=%v", body, err)
	}
}

func TestIncidentEvidenceDropsLegacyRawLog(t *testing.T) {
	evidence, err := (IncidentContextBuilder{Now: func() time.Time { return time.Unix(20, 0).UTC() }}).Build(context.Background(), telemetry.IncidentRecord{
		ID:          "inc-1",
		ProjectID:   "p1",
		ContextJSON: `{"metric":{"name":"cpu"},"raw_log":"password=secret"}`,
		CreatedAt:   time.Unix(10, 0),
	})
	body, _ := json.Marshal(evidence)
	if err != nil || strings.Contains(string(body), "raw_log") || strings.Contains(string(body), "password=secret") {
		t.Fatalf("expected legacy raw log omitted, evidence=%s err=%v", body, err)
	}
}

func TestIncidentContextBuilderProducesDeterministicPodAndLogEvidence(t *testing.T) {
	store, err := telemetry.OpenSQLiteStore(t.TempDir() + "/telemetry.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := time.Unix(1000, 0).UTC()
	rec := telemetry.IncidentRecord{ID: "inc-1", ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", AnomalyType: "cpu_spike", Severity: "P1", Status: "open", CreatedAt: created}
	for _, metric := range []telemetry.MetricRecord{
		{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", PodID: "pod-1", Name: "pod.ready", Value: 1, Unit: "bool", ObservedAt: created},
		{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", PodID: "pod-1", Name: "pod.restart_count", Value: 2, Unit: "count", ObservedAt: created},
	} {
		if err := store.InsertMetric(context.Background(), metric); err != nil {
			t.Fatal(err)
		}
	}
	for _, log := range []telemetry.LogRecord{
		{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", Level: "error", Message: "password=secret boom", Fingerprint: "fp-2", ObservedAt: created},
		{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", Level: "warn", Message: "another raw message", Fingerprint: "fp-1", ObservedAt: created},
	} {
		if err := store.InsertLog(context.Background(), log); err != nil {
			t.Fatal(err)
		}
	}
	builder := IncidentContextBuilder{Store: store, Now: func() time.Time { return time.Unix(2000, 0).UTC() }}
	first, err := builder.Build(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if len(first.Pods) != 1 || first.Pods[0].ReadyContainers != 1 || first.Pods[0].RestartCount != 2 || len(first.LogFingerprints) != 2 || first.LogFingerprints[0].Fingerprint != "fp-1" {
		t.Fatalf("bad incident evidence: %s", firstJSON)
	}
	if strings.Contains(string(firstJSON), "password=secret") || string(firstJSON) != string(secondJSON) {
		t.Fatalf("evidence must be sanitized and deterministic: first=%s second=%s", firstJSON, secondJSON)
	}
}

func TestListGetResolveAuthorizationIsPreserved(t *testing.T) {
	store := &fakeStore{rec: telemetry.IncidentRecord{ID: "inc-1", ProjectID: "p1", Status: "open"}}
	audit := &fakeAudit{}
	svc := Service{Store: store, Audit: audit, Now: func() time.Time { return time.Unix(100, 0) }}

	if records, err := svc.List(context.Background(), ListRequest{ProjectID: "p1", UserID: "viewer", Role: "Viewer"}); err != nil || len(records) != 1 {
		t.Fatalf("viewer list failed records=%+v err=%v", records, err)
	}
	if rec, err := svc.Get(context.Background(), IncidentRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "viewer", Role: "Viewer"}); err != nil || rec == nil {
		t.Fatalf("viewer get failed rec=%+v err=%v", rec, err)
	}
	if _, err := svc.Resolve(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "viewer", Role: "Viewer"}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected viewer resolve denial, got %v", err)
	}
	if _, err := svc.Resolve(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "owner", Role: "Owner"}); err != ErrApprovalRequired || store.resolveCalls != 0 {
		t.Fatalf("direct owner resolve bypassed ActionPlane: calls=%d err=%v", store.resolveCalls, err)
	}
	if rec, err := svc.ResolveApproved(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "owner", Role: "Owner"}); err != nil || rec.Status != StatusResolved {
		t.Fatalf("approved owner resolve failed rec=%+v err=%v", rec, err)
	}
	if len(audit.records) != 1 || audit.records[0].Action != "incident.resolve.approved" || audit.records[0].Result != "success" {
		t.Fatalf("resolve audit missing: %+v", audit.records)
	}

	store.rec.Status = "open"
	if rec, err := svc.ResolveApproved(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "developer", Role: "Developer"}); err != nil || rec.Status != StatusResolved {
		t.Fatalf("developer resolve failed rec=%+v err=%v", rec, err)
	}
}

func TestResolveIncidentDoesNotDependOnLegacyRCA(t *testing.T) {
	store := &fakeStore{rec: telemetry.IncidentRecord{
		ID:                "inc-legacy",
		ProjectID:         "p1",
		Status:            "open",
		RCAResult:         `{"malformed":`,
		MitigationActions: `[{"type":"rollback","status":"success"}]`,
	}}
	svc := Service{Store: store}
	rec, err := svc.ResolveApproved(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-legacy", UserID: "owner", Role: "Owner"})
	if err != nil || rec == nil || rec.Status != StatusResolved {
		t.Fatalf("resolve must ignore legacy RCA data, rec=%+v err=%v", rec, err)
	}
}

type fakeStore struct {
	rec          telemetry.IncidentRecord
	resolveCalls int
}

func (f *fakeStore) ListIncidents(context.Context, string, string, int) ([]telemetry.IncidentRecord, error) {
	return []telemetry.IncidentRecord{f.rec}, nil
}

func (f *fakeStore) GetIncident(context.Context, string, string) (*telemetry.IncidentRecord, error) {
	return &f.rec, nil
}

func (f *fakeStore) ResolveIncident(context.Context, string, string, time.Time) (*telemetry.IncidentRecord, error) {
	f.resolveCalls++
	f.rec.Status = StatusResolved
	return &f.rec, nil
}

type fakeAudit struct{ records []secret.AuditRecord }

func (f *fakeAudit) InsertAudit(_ context.Context, record secret.AuditRecord) error {
	f.records = append(f.records, record)
	return nil
}
