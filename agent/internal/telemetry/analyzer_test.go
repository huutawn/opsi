package telemetry

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestAnalyzerCreatesIncidentAfterConsecutiveSpike(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	analyzer := &Analyzer{Store: store, ConsecutiveNeeded: 3, ZThreshold: 1.0, Now: func() time.Time { return now }}
	values := []float64{10, 11, 9, 10, 11, 99, 98, 97}
	var incident *IncidentRecord
	for i, value := range values {
		now = now.Add(time.Second)
		incident, err = analyzer.ObserveMetric(context.Background(), MetricRecord{ProjectID: "proj", NodeID: "node", ServiceID: "svc", PodID: "pod", Name: "cpu_pct", Value: value, Unit: "pct", ObservedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		if i < len(values)-1 && incident != nil {
			t.Fatalf("incident too early at %d: %+v", i, incident)
		}
	}
	if incident == nil || incident.AnomalyType != AnomalyCPUSpike || incident.Severity != "P1" || incident.Status != IncidentStatusDetecting {
		t.Fatalf("unexpected incident: %+v", incident)
	}
	var ctx IncidentContext
	if err := json.Unmarshal([]byte(incident.ContextJSON), &ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.ProjectID != "proj" || len(ctx.AffectedServices) != 1 || ctx.AffectedServices[0] != "svc" || ctx.AnomalyType != AnomalyCPUSpike {
		t.Fatalf("unexpected context: %+v", ctx)
	}
}

func TestAnalyzerDedupesOpenIncident(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	if err := store.InsertIncident(context.Background(), IncidentRecord{ID: "inc-existing", ProjectID: "proj", ServiceID: "svc", AnomalyType: AnomalyCPUSpike, Severity: "P2", Status: "detecting", ContextJSON: `{}`, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	analyzer := &Analyzer{Store: store, ConsecutiveNeeded: 1, ZThreshold: 0.1, Now: func() time.Time { return now.Add(time.Minute) }}
	for _, value := range []float64{1, 2, 99, 100, 101} {
		incident, err := analyzer.ObserveMetric(context.Background(), MetricRecord{ProjectID: "proj", ServiceID: "svc", Name: "cpu_pct", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		if incident != nil && incident.ID != "inc-existing" {
			t.Fatalf("expected dedupe to existing incident, got %+v", incident)
		}
	}
}
