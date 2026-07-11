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

func TestSanitizeIncidentContextRejectsSecret(t *testing.T) {
	_, err := SanitizeIncidentContext(telemetry.IncidentRecord{
		ID:          "inc-1",
		ProjectID:   "p1",
		ContextJSON: `{"metric":{"password":"secret"}}`,
		CreatedAt:   time.Unix(10, 0),
	})
	if err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("expected sensitive data rejection, got %v", err)
	}
}

func TestSanitizeIncidentContextDropsRawLog(t *testing.T) {
	ctx, err := SanitizeIncidentContext(telemetry.IncidentRecord{
		ID:          "inc-1",
		ProjectID:   "p1",
		ContextJSON: `{"metric":{"name":"cpu"},"raw_log":"password=secret"}`,
		CreatedAt:   time.Unix(10, 0),
	})
	if err != nil || ctx.Metric["raw_log"] != nil {
		t.Fatalf("expected raw log omitted, ctx=%+v err=%v", ctx, err)
	}
}

func TestIncidentContextBuilderAddsMetricAndLogEvidence(t *testing.T) {
	store, err := telemetry.OpenSQLiteStore(t.TempDir() + "/telemetry.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := time.Unix(1000, 0).UTC()
	rec := telemetry.IncidentRecord{ID: "inc-1", ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", AnomalyType: "cpu_spike", Severity: "P1", Status: "open", CreatedAt: created}
	if err := store.InsertMetric(context.Background(), telemetry.MetricRecord{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", Name: "cpu_usage", Value: 96, Unit: "%", ObservedAt: created}); err != nil {
		t.Fatal(err)
	}
	for _, log := range []telemetry.LogRecord{
		{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", Level: "error", Message: "password=secret boom", Fingerprint: "fp-2", ObservedAt: created},
		{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", Level: "warn", Message: "another raw message", Fingerprint: "fp-1", ObservedAt: created},
	} {
		if err := store.InsertLog(context.Background(), log); err != nil {
			t.Fatal(err)
		}
	}
	builder := IncidentContextBuilder{Store: store}
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
	if first.MetricSnapshot["cpu_usage"]["value"] != float64(96) || len(first.LogPatterns) != 2 || first.LogPatterns[0]["fingerprint"] != "fp-1" {
		t.Fatalf("bad context evidence: %s", firstJSON)
	}
	if strings.Contains(string(firstJSON), "password=secret") || string(firstJSON) != string(secondJSON) {
		t.Fatalf("context must be sanitized and deterministic: first=%s second=%s", firstJSON, secondJSON)
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
	if rec, err := svc.Resolve(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "owner", Role: "Owner"}); err != nil || rec.Status != StatusResolved {
		t.Fatalf("owner resolve failed rec=%+v err=%v", rec, err)
	}
	if len(audit.records) != 1 || audit.records[0].Action != "incident.resolve" || audit.records[0].Result != "success" {
		t.Fatalf("resolve audit missing: %+v", audit.records)
	}

	store.rec.Status = "open"
	if rec, err := svc.Resolve(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-1", UserID: "developer", Role: "Developer"}); err != nil || rec.Status != StatusResolved {
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
	rec, err := svc.Resolve(context.Background(), ResolveRequest{ProjectID: "p1", IncidentID: "inc-legacy", UserID: "owner", Role: "Owner"})
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
