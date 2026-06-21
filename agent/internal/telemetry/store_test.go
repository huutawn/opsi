package telemetry

import (
	"context"
	"testing"
	"time"
)

func TestSQLiteStoreMigrationIsIdempotentAndSyncsRecords(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	observed := time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC)
	if err := store.InsertMetric(context.Background(), MetricRecord{ProjectID: "proj", NodeID: "node-1", ServiceID: "svc", Name: "cpu", Value: 0.5, Unit: "cores", ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertLog(context.Background(), LogRecord{ProjectID: "proj", NodeID: "node-1", ServiceID: "svc", Namespace: "default", Level: "error", Message: "pod 123 failed", Unread: true, ObservedAt: observed.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	records, err := store.SyncRecords(context.Background(), "proj", observed.Add(-time.Second), observed.Add(time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Kind != "metric" || records[1].Kind != "log" {
		t.Fatalf("unexpected records: %+v", records)
	}
	if records[1].Log.Fingerprint == "" {
		t.Fatal("log fingerprint was not set")
	}
}

func TestRetainAggregatesExpiredRawMetrics(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	observed := now.Add(-31 * 24 * time.Hour)
	if err := store.InsertMetric(context.Background(), MetricRecord{ProjectID: "proj", NodeID: "node", Name: "old", Value: 1, Unit: "count", ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertMetric(context.Background(), MetricRecord{ProjectID: "proj", NodeID: "node", Name: "old", Value: 3, Unit: "count", ObservedAt: observed.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Retain(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	records, err := store.SyncRecords(context.Background(), "proj", now.Add(-40*24*time.Hour), now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Kind != "metric_aggregate" {
		t.Fatalf("expected aggregate only, got %+v", records)
	}
	aggregate := records[0].MetricAggregate
	if aggregate == nil || aggregate.Count != 2 || aggregate.Avg != 2 || aggregate.Min != 1 || aggregate.Max != 3 {
		t.Fatalf("unexpected aggregate: %+v", aggregate)
	}
}
