package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/secret"
	_ "modernc.org/sqlite"
)

type Store interface {
	InsertMetric(ctx context.Context, record MetricRecord) error
	InsertLog(ctx context.Context, record LogRecord) error
	SyncRecords(ctx context.Context, projectID string, since time.Time, until time.Time, resourceIDs []string) ([]SyncRecord, error)
	Retain(ctx context.Context, now time.Time) error
	Close() error
}

type SQLiteStore struct {
	db *sql.DB
}

func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("enable sqlite wal: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS metrics (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  service_id TEXT NOT NULL DEFAULT '',
  pod_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  value REAL NOT NULL,
  unit TEXT NOT NULL,
  observed_at_unix INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS metrics_project_observed_idx
  ON metrics(project_id, observed_at_unix);
CREATE INDEX IF NOT EXISTS metrics_resource_idx
  ON metrics(project_id, node_id, service_id, pod_id, observed_at_unix);

CREATE TABLE IF NOT EXISTS metric_aggregates (
  project_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  service_id TEXT NOT NULL DEFAULT '',
  pod_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  unit TEXT NOT NULL,
  bucket_start_unix INTEGER NOT NULL,
  bucket_seconds INTEGER NOT NULL,
  count INTEGER NOT NULL,
  avg_value REAL NOT NULL,
  min_value REAL NOT NULL,
  max_value REAL NOT NULL,
  updated_at_unix INTEGER NOT NULL,
  PRIMARY KEY(project_id, node_id, service_id, pod_id, name, bucket_start_unix, bucket_seconds)
);
CREATE INDEX IF NOT EXISTS metric_aggregates_project_bucket_idx
  ON metric_aggregates(project_id, bucket_start_unix);

CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  service_id TEXT NOT NULL DEFAULT '',
  pod_id TEXT NOT NULL DEFAULT '',
  namespace TEXT NOT NULL,
  level TEXT NOT NULL,
  message TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  unread INTEGER NOT NULL DEFAULT 1,
  observed_at_unix INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS logs_project_observed_idx
  ON logs(project_id, observed_at_unix);
CREATE INDEX IF NOT EXISTS logs_fingerprint_idx
  ON logs(project_id, fingerprint, observed_at_unix);

CREATE TABLE IF NOT EXISTS incidents (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  node_id TEXT NOT NULL DEFAULT '',
  service_id TEXT NOT NULL DEFAULT '',
  pod_id TEXT NOT NULL DEFAULT '',
  severity TEXT NOT NULL,
  status TEXT NOT NULL,
  context_json TEXT NOT NULL DEFAULT '{}',
  rca_json TEXT NOT NULL DEFAULT '{}',
  created_at_unix INTEGER NOT NULL,
  updated_at_unix INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS incidents_project_status_idx
  ON incidents(project_id, status, updated_at_unix);

CREATE TABLE IF NOT EXISTS audit_log (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  result TEXT NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at_unix INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_log_project_created_idx
  ON audit_log(project_id, created_at_unix);
`)
	if err != nil {
		return fmt.Errorf("init telemetry schema: %w", err)
	}
	return nil
}

func (s *SQLiteStore) InsertMetric(ctx context.Context, record MetricRecord) error {
	observed := record.ObservedAt
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO metrics(project_id, node_id, service_id, pod_id, name, value, unit, observed_at_unix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, record.ProjectID, record.NodeID, record.ServiceID, record.PodID, record.Name, record.Value, record.Unit, observed.Unix())
	if err != nil {
		return fmt.Errorf("insert metric: %w", err)
	}
	return nil
}

func (s *SQLiteStore) InsertLog(ctx context.Context, record LogRecord) error {
	observed := record.ObservedAt
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	fingerprint := record.Fingerprint
	if fingerprint == "" {
		fingerprint = Fingerprint(record.Message)
	}
	unread := 0
	if record.Unread {
		unread = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO logs(project_id, node_id, service_id, pod_id, namespace, level, message, fingerprint, unread, observed_at_unix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, record.ProjectID, record.NodeID, record.ServiceID, record.PodID, record.Namespace, record.Level, record.Message, fingerprint, unread, observed.Unix())
	if err != nil {
		return fmt.Errorf("insert log: %w", err)
	}
	return nil
}

func (s *SQLiteStore) InsertAudit(ctx context.Context, record secret.AuditRecord) error {
	created := record.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	if record.MetadataJSON == "" {
		record.MetadataJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO audit_log(id, project_id, actor, action, resource_type, resource_id, result, metadata_json, created_at_unix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, record.ID, record.ProjectID, record.Actor, record.Action, record.ResourceType, record.ResourceID, record.Result, record.MetadataJSON, created.Unix())
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

func (s *SQLiteStore) SyncRecords(ctx context.Context, projectID string, since time.Time, until time.Time, resourceIDs []string) ([]SyncRecord, error) {
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if until.IsZero() {
		until = time.Now().UTC()
	}
	resources := map[string]bool{}
	for _, id := range resourceIDs {
		if id != "" {
			resources[id] = true
		}
	}

	records, err := s.metricRecords(ctx, projectID, since, until, resources)
	if err != nil {
		return nil, err
	}
	aggregates, err := s.aggregateRecords(ctx, projectID, since, until, resources)
	if err != nil {
		return nil, err
	}
	records = append(records, aggregates...)
	logs, err := s.logRecords(ctx, projectID, since, until, resources)
	if err != nil {
		return nil, err
	}
	records = append(records, logs...)
	sortSyncRecords(records)
	return records, nil
}

func (s *SQLiteStore) metricRecords(ctx context.Context, projectID string, since, until time.Time, resources map[string]bool) ([]SyncRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT project_id, node_id, service_id, pod_id, name, value, unit, observed_at_unix
FROM metrics
WHERE project_id = ? AND observed_at_unix > ? AND observed_at_unix <= ?
ORDER BY observed_at_unix ASC
`, projectID, since.Unix(), until.Unix())
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	defer rows.Close()

	var records []SyncRecord
	for rows.Next() {
		var rec MetricRecord
		var observed int64
		if err := rows.Scan(&rec.ProjectID, &rec.NodeID, &rec.ServiceID, &rec.PodID, &rec.Name, &rec.Value, &rec.Unit, &observed); err != nil {
			return nil, err
		}
		rec.ObservedAt = time.Unix(observed, 0).UTC()
		if !resourceAllowed(resources, rec.NodeID, rec.ServiceID, rec.PodID) {
			continue
		}
		metric := rec
		records = append(records, SyncRecord{Kind: "metric", Metric: &metric, ObservedAt: rec.ObservedAt})
	}
	return records, rows.Err()
}

func (s *SQLiteStore) aggregateRecords(ctx context.Context, projectID string, since, until time.Time, resources map[string]bool) ([]SyncRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT project_id, node_id, service_id, pod_id, name, unit, bucket_start_unix, bucket_seconds, count, avg_value, min_value, max_value
FROM metric_aggregates
WHERE project_id = ? AND bucket_start_unix > ? AND bucket_start_unix <= ?
ORDER BY bucket_start_unix ASC
`, projectID, since.Unix(), until.Unix())
	if err != nil {
		return nil, fmt.Errorf("query metric aggregates: %w", err)
	}
	defer rows.Close()

	var records []SyncRecord
	for rows.Next() {
		var rec MetricAggregateRecord
		var bucketStart int64
		if err := rows.Scan(&rec.ProjectID, &rec.NodeID, &rec.ServiceID, &rec.PodID, &rec.Name, &rec.Unit, &bucketStart, &rec.BucketSeconds, &rec.Count, &rec.Avg, &rec.Min, &rec.Max); err != nil {
			return nil, err
		}
		rec.BucketStart = time.Unix(bucketStart, 0).UTC()
		if !resourceAllowed(resources, rec.NodeID, rec.ServiceID, rec.PodID) {
			continue
		}
		aggregate := rec
		records = append(records, SyncRecord{Kind: "metric_aggregate", MetricAggregate: &aggregate, ObservedAt: rec.BucketStart})
	}
	return records, rows.Err()
}

func (s *SQLiteStore) logRecords(ctx context.Context, projectID string, since, until time.Time, resources map[string]bool) ([]SyncRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT project_id, node_id, service_id, pod_id, namespace, level, message, fingerprint, unread, observed_at_unix
FROM logs
WHERE project_id = ? AND observed_at_unix > ? AND observed_at_unix <= ?
ORDER BY observed_at_unix ASC
`, projectID, since.Unix(), until.Unix())
	if err != nil {
		return nil, fmt.Errorf("query logs: %w", err)
	}
	defer rows.Close()

	var records []SyncRecord
	for rows.Next() {
		var rec LogRecord
		var observed int64
		var unread int
		if err := rows.Scan(&rec.ProjectID, &rec.NodeID, &rec.ServiceID, &rec.PodID, &rec.Namespace, &rec.Level, &rec.Message, &rec.Fingerprint, &unread, &observed); err != nil {
			return nil, err
		}
		rec.Unread = unread == 1
		rec.ObservedAt = time.Unix(observed, 0).UTC()
		if !resourceAllowed(resources, rec.NodeID, rec.ServiceID, rec.PodID) {
			continue
		}
		logRecord := rec
		records = append(records, SyncRecord{Kind: "log", Log: &logRecord, ObservedAt: rec.ObservedAt})
	}
	return records, rows.Err()
}

func (s *SQLiteStore) Retain(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	metricCutoff := now.Add(-30 * 24 * time.Hour).Unix()
	logCutoff := now.Add(-7 * 24 * time.Hour).Unix()
	aggregateCutoff := now.Add(-365 * 24 * time.Hour).Unix()
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO metric_aggregates(project_id, node_id, service_id, pod_id, name, unit, bucket_start_unix, bucket_seconds, count, avg_value, min_value, max_value, updated_at_unix)
SELECT project_id,
       node_id,
       service_id,
       pod_id,
       name,
       unit,
       (observed_at_unix / 86400) * 86400 AS bucket_start_unix,
       86400 AS bucket_seconds,
       COUNT(*) AS count,
       AVG(value) AS avg_value,
       MIN(value) AS min_value,
       MAX(value) AS max_value,
       ? AS updated_at_unix
FROM metrics
WHERE observed_at_unix < ? AND observed_at_unix >= ?
GROUP BY project_id, node_id, service_id, pod_id, name, unit, bucket_start_unix
ON CONFLICT(project_id, node_id, service_id, pod_id, name, bucket_start_unix, bucket_seconds) DO UPDATE SET
  count = excluded.count,
  avg_value = excluded.avg_value,
  min_value = excluded.min_value,
  max_value = excluded.max_value,
  updated_at_unix = excluded.updated_at_unix
`, now.Unix(), metricCutoff, aggregateCutoff); err != nil {
		return fmt.Errorf("aggregate metrics: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM metrics WHERE observed_at_unix < ?`, metricCutoff); err != nil {
		return fmt.Errorf("retain metrics: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM logs WHERE observed_at_unix < ?`, logCutoff); err != nil {
		return fmt.Errorf("retain logs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM metric_aggregates WHERE bucket_start_unix < ?`, aggregateCutoff); err != nil {
		return fmt.Errorf("retain metric aggregates: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func resourceAllowed(resources map[string]bool, nodeID, serviceID, podID string) bool {
	if len(resources) == 0 {
		return true
	}
	return resources[nodeID] || resources[serviceID] || resources[podID]
}
