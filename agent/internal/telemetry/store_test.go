package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestBoundedTelemetryWindowsDefaultsAndRejection(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	// 1. Zero-window defaults to 1 hour
	reqZero := &agentv1.TelemetryQueryRequest{
		ProjectID:      "proj-1",
		SinceUnix:      0,
		IncludeSummary: true,
	}
	resp, err := BuildQueryResponse(context.Background(), store, reqZero, now)
	if err != nil {
		t.Fatalf("unexpected error for zero-window: %v", err)
	}
	if resp.Summary == nil || resp.Summary.SinceUnix != now.Add(-1*time.Hour).Unix() {
		t.Fatalf("expected since_unix to default to 1h ago (%d), got %+v", now.Add(-1*time.Hour).Unix(), resp.Summary)
	}

	// 2. Window > 24 hours rejected with InvalidArgument
	reqOver24 := &agentv1.TelemetryQueryRequest{
		ProjectID:      "proj-1",
		SinceUnix:      now.Add(-25 * time.Hour).Unix(),
		IncludeSummary: true,
	}
	_, err = BuildQueryResponse(context.Background(), store, reqOver24, now)
	if err == nil {
		t.Fatal("expected error for >24h window")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got code %v: %v", status.Code(err), err)
	}

	// 3. Exactly 24h window accepted
	req24 := &agentv1.TelemetryQueryRequest{
		ProjectID:      "proj-1",
		SinceUnix:      now.Add(-24 * time.Hour).Unix(),
		IncludeSummary: true,
	}
	resp24, err := BuildQueryResponse(context.Background(), store, req24, now)
	if err != nil {
		t.Fatalf("expected 24h window to be accepted, got: %v", err)
	}
	if resp24.Summary.SinceUnix != now.Add(-24*time.Hour).Unix() {
		t.Fatalf("unexpected since_unix: %d", resp24.Summary.SinceUnix)
	}
}

func TestBoundedTelemetryLatestSampleSemanticsNeverSumsHistorical(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	// Pod 1 historical samples (should take latest only: CPU=0.8, Memory=500, Ready=0, Restarts=4)
	metrics := []MetricRecord{
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.cpu", Value: 0.2, Unit: "cores", ObservedAt: now.Add(-30 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.cpu", Value: 0.4, Unit: "cores", ObservedAt: now.Add(-20 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.cpu", Value: 0.8, Unit: "cores", ObservedAt: now.Add(-10 * time.Minute)},

		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.memory", Value: 100, Unit: "bytes", ObservedAt: now.Add(-30 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.memory", Value: 500, Unit: "bytes", ObservedAt: now.Add(-10 * time.Minute)},

		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.ready", Value: 1, Unit: "bool", ObservedAt: now.Add(-30 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.ready", Value: 0, Unit: "bool", ObservedAt: now.Add(-10 * time.Minute)},

		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.restart_count", Value: 1, Unit: "count", ObservedAt: now.Add(-30 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-1", Name: "pod.restart_count", Value: 4, Unit: "count", ObservedAt: now.Add(-10 * time.Minute)},

		// Pod 2 single sample (CPU=0.3, Memory=300, Ready=1, Restarts=2)
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-2", Name: "pod.cpu", Value: 0.3, Unit: "cores", ObservedAt: now.Add(-10 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-2", Name: "pod.memory", Value: 300, Unit: "bytes", ObservedAt: now.Add(-10 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-2", Name: "pod.ready", Value: 1, Unit: "bool", ObservedAt: now.Add(-10 * time.Minute)},
		{ProjectID: "proj-1", NodeID: "node-1", ServiceID: "web", PodID: "pod-2", Name: "pod.restart_count", Value: 2, Unit: "count", ObservedAt: now.Add(-10 * time.Minute)},
	}

	for _, m := range metrics {
		if err := store.InsertMetric(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}

	req := &agentv1.TelemetryQueryRequest{
		ProjectID:       "proj-1",
		SinceUnix:       now.Add(-1 * time.Hour).Unix(),
		IncludeSummary:  true,
		IncludeServices: true,
	}

	resp, err := BuildQueryResponse(context.Background(), store, req, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(resp.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(resp.Services))
	}
	svc := resp.Services[0]
	if svc.ServiceID != "web" {
		t.Fatalf("unexpected service id: %s", svc.ServiceID)
	}
	// CPU must be 0.8 + 0.3 = 1.1 (never 0.2 + 0.4 + 0.8 + 0.3 = 1.7)
	if svc.CPUCores < 1.09 || svc.CPUCores > 1.11 {
		t.Fatalf("CPU was summed historically! expected ~1.1, got %f", svc.CPUCores)
	}
	// Memory must be 500 + 300 = 800 (never 100 + 500 + 300 = 900)
	if svc.MemoryBytes != 800 {
		t.Fatalf("Memory was summed historically! expected 800, got %f", svc.MemoryBytes)
	}
	// Restarts must be 4 + 2 = 6 (never 1 + 4 + 2 = 7)
	if svc.RestartCount != 6 {
		t.Fatalf("Restarts were summed historically! expected 6, got %d", svc.RestartCount)
	}
	// Pod count 2, ready pods 1 (pod-1 ready=0, pod-2 ready=1)
	if svc.PodCount != 2 || svc.ReadyPods != 1 {
		t.Fatalf("expected 2 pods with 1 ready, got pods=%d ready=%d", svc.PodCount, svc.ReadyPods)
	}
	// Health must be degraded
	if svc.Health != "degraded" {
		t.Fatalf("expected degraded health, got %s", svc.Health)
	}
	// MetricCount in summary counts all 13 metric entries inserted
	if resp.Summary.MetricCount != 13 {
		t.Fatalf("expected 13 total metric samples counted, got %d", resp.Summary.MetricCount)
	}
}

func TestBoundedTelemetryPagedLogsAndCursor(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	for i := 1; i <= 5; i++ {
		log := LogRecord{
			ProjectID:   "proj-1",
			NodeID:      "node-1",
			ServiceID:   "api",
			Namespace:   "default",
			Level:       "info",
			Message:     fmt.Sprintf("log line %d with token=secret-token-%d", i, i),
			ObservedAt:  now.Add(time.Duration(i) * time.Minute),
			Fingerprint: fmt.Sprintf("fp-%d", i),
		}
		if err := store.InsertLog(context.Background(), log); err != nil {
			t.Fatal(err)
		}
	}

	// Page 1: limit 2
	reqPage1 := &agentv1.TelemetryQueryRequest{
		ProjectID:   "proj-1",
		SinceUnix:   now.Unix(),
		IncludeLogs: true,
		Limit:       2,
	}
	resp1, err := BuildQueryResponse(context.Background(), store, reqPage1, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp1.Logs) != 2 || resp1.NextCursor == "" {
		t.Fatalf("page 1 failed: len=%d cursor=%q", len(resp1.Logs), resp1.NextCursor)
	}
	if strings.Contains(resp1.Logs[0].Message, "secret-token-1") {
		t.Fatalf("secret not redacted in page 1: %s", resp1.Logs[0].Message)
	}

	// Page 2: with cursor from page 1, limit 2
	reqPage2 := &agentv1.TelemetryQueryRequest{
		ProjectID:   "proj-1",
		SinceUnix:   now.Unix(),
		Cursor:      resp1.NextCursor,
		IncludeLogs: true,
		Limit:       2,
	}
	resp2, err := BuildQueryResponse(context.Background(), store, reqPage2, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp2.Logs) != 2 || resp2.NextCursor == "" {
		t.Fatalf("page 2 failed: len=%d cursor=%q", len(resp2.Logs), resp2.NextCursor)
	}
	if resp2.Logs[0].Fingerprint != "fp-3" || resp2.Logs[1].Fingerprint != "fp-4" {
		t.Fatalf("unexpected logs on page 2: %+v", resp2.Logs)
	}

	// Page 3: with cursor from page 2, limit 2 (only 1 log remaining -> NextCursor must be empty)
	reqPage3 := &agentv1.TelemetryQueryRequest{
		ProjectID:   "proj-1",
		SinceUnix:   now.Unix(),
		Cursor:      resp2.NextCursor,
		IncludeLogs: true,
		Limit:       2,
	}
	resp3, err := BuildQueryResponse(context.Background(), store, reqPage3, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp3.Logs) != 1 || resp3.NextCursor != "" {
		t.Fatalf("page 3 failed: len=%d cursor=%q (expected empty cursor on last page)", len(resp3.Logs), resp3.NextCursor)
	}
	if resp3.Logs[0].Fingerprint != "fp-5" {
		t.Fatalf("unexpected log on page 3: %+v", resp3.Logs[0])
	}
}

func TestBoundedTelemetryCancellation(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	now := time.Now().UTC()
	_, err = store.QueryBoundedTelemetry(ctx, "proj-1", "", now.Add(-time.Hour), now, true, true, true, 100, "")
	if err == nil {
		t.Fatal("expected error on canceled context")
	}
	if !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context canceled error, got: %v", err)
	}
}
