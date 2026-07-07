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
	if err := store.InsertLog(context.Background(), telemetry.LogRecord{ProjectID: "p1", NodeID: "node-1", ServiceID: "svc", Level: "error", Message: "password=secret boom", Fingerprint: "fp-1", ObservedAt: created}); err != nil {
		t.Fatal(err)
	}
	ctx, err := (IncidentContextBuilder{Store: store}).Build(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(ctx)
	if ctx.MetricSnapshot["cpu_usage"]["value"] != float64(96) || len(ctx.LogPatterns) != 1 || strings.Contains(string(data), "password=secret") {
		t.Fatalf("bad context evidence: %s", data)
	}
}

func TestValidateRCABlocksUnsafeRollback(t *testing.T) {
	ctx := IncidentContext{SchemaVersion: "opsi.incident_context.v1", IncidentID: "inc-1", ProjectID: "p1", ServiceID: "svc"}
	err := ValidateRCA(ctx, RCA{SchemaVersion: "opsi.rca.v1", IncidentID: "inc-1", RootCause: "bad deploy", Confidence: 0.9, Metadata: metaForTest(ctx), RecommendedActions: []Action{{ID: "a1", Type: "shell", Params: map[string]string{"service_id": "svc"}}}})
	if err == nil {
		t.Fatal("expected invalid action")
	}
}

func TestValidateRCARejectsMetadataHashMismatch(t *testing.T) {
	ctx := IncidentContext{SchemaVersion: "opsi.incident_context.v1", IncidentID: "inc-1", ProjectID: "p1", ServiceID: "svc"}
	rca := fallbackRCA(ctx)
	rca.Metadata.InputContextHash = "sha256:bad"
	if err := ValidateRCA(ctx, rca); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("expected metadata rejection, got %v", err)
	}
}

func TestFallbackRCAIsExplicitLocalFallback(t *testing.T) {
	ctx := IncidentContext{SchemaVersion: "opsi.incident_context.v1", IncidentID: "inc-1", ProjectID: "p1", ServiceID: "svc", AnomalyType: "cpu"}
	rca := fallbackRCA(ctx)
	if err := ValidateRCA(ctx, rca); err != nil {
		t.Fatal(err)
	}
	if rca.Metadata.Provider != "local" || rca.Metadata.Model != "fallback" || !rca.Metadata.FallbackUsed || rca.Metadata.InputContextHash != HashIncidentContext(ctx) || !strings.Contains(rca.RootCause, "fallback") {
		t.Fatalf("fallback metadata not explicit: %+v", rca.Metadata)
	}
}

func TestApproveExecutesAllowlistedAction(t *testing.T) {
	base := incidentRecordWithRCA()
	store := &fakeStore{rec: base}
	var calls int
	svc := Service{Store: store, DryRun: false, Exec: func(context.Context, string, ...string) error { calls++; return nil }}
	rec, _, err := svc.Approve(context.Background(), ActionRequest{ProjectID: "p1", IncidentID: "inc-1", ActionID: "scale", UserID: "u1", Role: "Developer"})
	if err != nil || calls != 2 || rec.Status != StatusResolving {
		t.Fatalf("approve failed rec=%+v calls=%v err=%v", rec, calls, err)
	}
}

func TestApproveRejectsStaleActionHash(t *testing.T) {
	store := &fakeStore{rec: incidentRecordWithRCA()}
	svc := Service{Store: store, DryRun: true}
	_, _, err := svc.Approve(context.Background(), ActionRequest{ProjectID: "p1", IncidentID: "inc-1", ActionID: "scale", ActionHash: "sha256:stale", UserID: "u1", Role: "Developer"})
	if err == nil || !strings.Contains(err.Error(), "stale action") {
		t.Fatalf("expected stale action error, got %v", err)
	}
}

func TestApproveAuditsApprovalAndExecution(t *testing.T) {
	audit := &fakeAudit{}
	svc := Service{Store: &fakeStore{rec: incidentRecordWithRCA()}, Audit: audit, DryRun: true}
	if _, _, err := svc.Approve(context.Background(), ActionRequest{ProjectID: "p1", IncidentID: "inc-1", ActionID: "scale", UserID: "u1", Role: "Developer"}); err != nil {
		t.Fatal(err)
	}
	if len(audit.records) != 2 || audit.records[0].Action != "incident.action.approve" || audit.records[1].Action != "incident.action.execute" {
		t.Fatalf("missing approval/execute audit: %+v", audit.records)
	}
}

func incidentRecordWithRCA() telemetry.IncidentRecord {
	rec := telemetry.IncidentRecord{ID: "inc-1", ProjectID: "p1", ServiceID: "svc", MitigationActions: "[]", CreatedAt: time.Unix(10, 0)}
	ctx, err := SanitizeIncidentContext(rec)
	if err != nil {
		panic(err)
	}
	rca := RCA{SchemaVersion: "opsi.rca.v1", IncidentID: "inc-1", RootCause: "cpu", Confidence: 0.7, Metadata: metaForTest(ctx), RecommendedActions: []Action{{ID: "scale", Type: "scale_replicas", Params: map[string]string{"service_id": "svc", "replicas": "3"}}}}
	data, _ := json.Marshal(rca)
	rec.RCAResult = string(data)
	return rec
}

func metaForTest(ctx IncidentContext) RCAMeta {
	return RCAMeta{Provider: "fixture", ConfiguredProvider: "fixture", Model: "fixture", InputContextHash: HashIncidentContext(ctx), CreatedAt: time.Unix(10, 0).UTC().Format(time.RFC3339)}
}

type fakeStore struct{ rec telemetry.IncidentRecord }

func (f *fakeStore) ListIncidents(context.Context, string, string, int) ([]telemetry.IncidentRecord, error) {
	return []telemetry.IncidentRecord{f.rec}, nil
}
func (f *fakeStore) GetIncident(context.Context, string, string) (*telemetry.IncidentRecord, error) {
	return &f.rec, nil
}
func (f *fakeStore) UpdateIncidentRCA(context.Context, string, string, string, string, time.Time) (*telemetry.IncidentRecord, error) {
	return &f.rec, nil
}
func (f *fakeStore) AppendIncidentAction(_ context.Context, _, _, status, actions string, _ time.Time) (*telemetry.IncidentRecord, error) {
	f.rec.Status = status
	f.rec.MitigationActions = actions
	return &f.rec, nil
}
func (f *fakeStore) ResolveIncident(context.Context, string, string, time.Time) (*telemetry.IncidentRecord, error) {
	f.rec.Status = StatusResolved
	return &f.rec, nil
}

type fakeAudit struct{ records []secret.AuditRecord }

func (f *fakeAudit) InsertAudit(_ context.Context, record secret.AuditRecord) error {
	f.records = append(f.records, record)
	return nil
}
