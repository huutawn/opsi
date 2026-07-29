package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
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
	if err := store.InsertIncident(context.Background(), IncidentRecord{ID: "inc-1", ProjectID: "proj", NodeID: "node-1", ServiceID: "svc", AnomalyType: "cpu_spike", Severity: "P2", Status: "detecting", ContextJSON: `{}`, CreatedAt: observed.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	records, err := store.SyncRecords(context.Background(), "proj", observed.Add(-time.Second), observed.Add(time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].Kind != "metric" || records[1].Kind != "log" || records[2].Kind != "incident" {
		t.Fatalf("unexpected records: %+v", records)
	}
	if records[1].Log.Fingerprint == "" {
		t.Fatal("log fingerprint was not set")
	}
}

func TestIncidentEvidenceColumnsMigratePersistAndAuditProjectionStaysFactual(t *testing.T) {
	path := t.TempDir() + "/telemetry.sqlite"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE incidents (
id TEXT PRIMARY KEY, project_id TEXT NOT NULL, node_id TEXT NOT NULL DEFAULT '', service_id TEXT NOT NULL DEFAULT '', pod_id TEXT NOT NULL DEFAULT '',
affected_services TEXT NOT NULL DEFAULT '', affected_nodes TEXT NOT NULL DEFAULT '', affected_pods TEXT NOT NULL DEFAULT '', anomaly_type TEXT NOT NULL DEFAULT '',
severity TEXT NOT NULL, status TEXT NOT NULL, context_json TEXT NOT NULL DEFAULT '{}', rca_json TEXT NOT NULL DEFAULT '{}', rca_result TEXT NOT NULL DEFAULT '',
mitigation_actions_json TEXT NOT NULL DEFAULT '[]', created_at_unix INTEGER NOT NULL, resolved_at_unix INTEGER NOT NULL DEFAULT 0,
mttr_seconds INTEGER NOT NULL DEFAULT 0, updated_at_unix INTEGER NOT NULL);
INSERT INTO incidents(id, project_id, severity, status, created_at_unix, updated_at_unix) VALUES ('historical', 'p1', 'P2', 'open', 10, 10);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetIncident(context.Background(), "p1", "historical")
	if err != nil || record.EvidenceJSON != "" || record.EvidenceSHA256 != "" || !record.EvidenceGeneratedAt.IsZero() {
		t.Fatalf("historical migration=%+v err=%v", record, err)
	}
	generated := time.Unix(20, 0).UTC()
	if err := store.PersistIncidentEvidence(context.Background(), "p1", "historical", `{"schema_version":"opsi.incident_evidence.v1"}`, strings.Repeat("a", 64), generated); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistIncidentEvidence(context.Background(), "p1", "historical", `{"changed":true}`, strings.Repeat("b", 64), time.Unix(30, 0)); err != nil {
		t.Fatal(err)
	}
	for _, audit := range []secret.AuditRecord{
		{ID: "audit-incident", ProjectID: "p1", Actor: "user", Action: "incident.open", ResourceType: "incident", ResourceID: "historical", IPAddress: "10.0.0.1", MetadataJSON: `{"token":"audit-canary"}`, Result: "success", CreatedAt: generated},
		{ID: "audit-other", ProjectID: "p1", Actor: "user", Action: "other", ResourceType: "service", ResourceID: "other", Result: "success", CreatedAt: generated},
	} {
		if err := store.InsertAudit(context.Background(), audit); err != nil {
			t.Fatal(err)
		}
	}
	audits, total, err := store.EvidenceAuditRecords(context.Background(), "p1", "historical", "", generated.Add(-time.Second), generated.Add(time.Second), 65)
	if err != nil || total != 1 || len(audits) != 1 || audits[0].ID != "audit-incident" {
		t.Fatalf("audit projection=%+v total=%d err=%v", audits, total, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, err = reopened.GetIncident(context.Background(), "p1", "historical")
	if err != nil || record.EvidenceSHA256 != strings.Repeat("a", 64) || record.EvidenceGeneratedAt.Unix() != generated.Unix() || strings.Contains(record.EvidenceJSON, "changed") {
		t.Fatalf("persisted evidence changed=%+v err=%v", record, err)
	}
}

func TestIncidentEvidenceTelemetryProjectionIsBounded(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observed := time.Unix(100, 0).UTC()
	for index := 0; index < 70; index++ {
		podID := fmt.Sprintf("pod-%03d", index)
		if err := store.InsertLog(context.Background(), LogRecord{ProjectID: "p1", NodeID: "node", ServiceID: "svc", PodID: podID, Fingerprint: fmt.Sprintf("fp-%03d", index), Level: "error", Message: strings.Repeat("界", 3000), ObservedAt: observed}); err != nil {
			t.Fatal(err)
		}
		for _, metric := range []MetricRecord{{ProjectID: "p1", NodeID: "node", ServiceID: "svc", PodID: podID, Name: "pod.ready", Value: 1, ObservedAt: observed}, {ProjectID: "p1", NodeID: "node", ServiceID: "svc", PodID: podID, Name: "pod.restart_count", Value: float64(index), ObservedAt: observed}} {
			if err := store.InsertMetric(context.Background(), metric); err != nil {
				t.Fatal(err)
			}
		}
	}
	projection, err := store.IncidentEvidenceTelemetry(context.Background(), "p1", "node", "svc", "", observed.Add(-time.Second), observed.Add(time.Second), 65)
	if err != nil {
		t.Fatal(err)
	}
	if projection.TotalLogGroups != 70 || projection.TotalPods != 70 || len(projection.LogGroups) != 65 || len(projection.Pods) != 65 {
		t.Fatalf("projection is not bounded: %+v", projection)
	}
}

func TestUptimeChecksStoreAndPercentage(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	for _, success := range []bool{true, false, true} {
		if err := store.InsertUptimeCheck(context.Background(), UptimeCheckRecord{ProjectID: "proj", ServiceID: "svc", Timestamp: now, Success: success, LatencyMS: 12, HTTPStatus: 200}); err != nil {
			t.Fatal(err)
		}
	}
	percent, err := store.UptimePercent(context.Background(), "proj", "svc", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if percent < 66 || percent > 67 {
		t.Fatalf("unexpected uptime percent: %f", percent)
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
