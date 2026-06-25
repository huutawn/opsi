package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store interface {
	UpsertService(ctx context.Context, service ServiceRecord) error
	Insert(ctx context.Context, record Record) error
	Update(ctx context.Context, record Record) error
	FindSuccessful(ctx context.Context, projectID, serviceID, gitSHA string) (*Record, error)
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
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("enable sqlite wal: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS services (
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  namespace TEXT NOT NULL,
  repo_url TEXT NOT NULL,
  branch TEXT NOT NULL,
  build_context TEXT NOT NULL,
  dockerfile TEXT NOT NULL,
  manifest_path TEXT NOT NULL,
  watch_paths TEXT NOT NULL DEFAULT '[]',
  termination_grace_period_seconds INTEGER NOT NULL DEFAULT 30,
  resource_requests_json TEXT NOT NULL DEFAULT '{"cpu":"100m","memory":"128Mi"}',
  resource_limits_json TEXT NOT NULL DEFAULT '{"cpu":"500m","memory":"512Mi"}',
  desired_state_json TEXT NOT NULL DEFAULT '{}',
  current_image_tag TEXT NOT NULL DEFAULT '',
  health TEXT NOT NULL DEFAULT 'unknown',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(project_id, id)
);
CREATE INDEX IF NOT EXISTS services_project_name_idx
  ON services(project_id, name);
CREATE TABLE IF NOT EXISTS deployments (
  deploy_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  service_id TEXT NOT NULL,
  service_name TEXT NOT NULL,
  started_at_unix INTEGER NOT NULL,
  finished_at_unix INTEGER NOT NULL DEFAULT 0,
  git_sha TEXT NOT NULL,
  image_tag TEXT NOT NULL,
  status TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  triggered_by TEXT NOT NULL,
  migration_ran BOOLEAN NOT NULL DEFAULT 0,
  rollback_safe BOOLEAN NOT NULL DEFAULT 0,
  rollback_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS deployments_project_service_sha_status_idx
  ON deployments(project_id, service_id, git_sha, status);
`)
	if err != nil {
		return fmt.Errorf("init deployments schema: %w", err)
	}
	if err := s.ensureServiceColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureDeploymentColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) ensureServiceColumns(ctx context.Context) error {
	columns, err := s.tableColumns(ctx, "services")
	if err != nil {
		return err
	}
	for name, ddl := range map[string]string{
		"watch_paths":                      "ALTER TABLE services ADD COLUMN watch_paths TEXT NOT NULL DEFAULT '[]'",
		"termination_grace_period_seconds": "ALTER TABLE services ADD COLUMN termination_grace_period_seconds INTEGER NOT NULL DEFAULT 30",
		"resource_requests_json":           "ALTER TABLE services ADD COLUMN resource_requests_json TEXT NOT NULL DEFAULT '{\"cpu\":\"100m\",\"memory\":\"128Mi\"}'",
		"resource_limits_json":             "ALTER TABLE services ADD COLUMN resource_limits_json TEXT NOT NULL DEFAULT '{\"cpu\":\"500m\",\"memory\":\"512Mi\"}'",
	} {
		if columns[name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate services.%s: %w", name, err)
		}
	}
	return nil
}

func (s *SQLiteStore) ensureDeploymentColumns(ctx context.Context) error {
	columns, err := s.tableColumns(ctx, "deployments")
	if err != nil {
		return err
	}
	for name, ddl := range map[string]string{
		"project_id":      "ALTER TABLE deployments ADD COLUMN project_id TEXT NOT NULL DEFAULT ''",
		"service_id":      "ALTER TABLE deployments ADD COLUMN service_id TEXT NOT NULL DEFAULT ''",
		"service_name":    "ALTER TABLE deployments ADD COLUMN service_name TEXT NOT NULL DEFAULT ''",
		"migration_ran":   "ALTER TABLE deployments ADD COLUMN migration_ran BOOLEAN NOT NULL DEFAULT 0",
		"rollback_safe":   "ALTER TABLE deployments ADD COLUMN rollback_safe BOOLEAN NOT NULL DEFAULT 0",
		"rollback_reason": "ALTER TABLE deployments ADD COLUMN rollback_reason TEXT NOT NULL DEFAULT ''",
	} {
		if columns[name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate deployments.%s: %w", name, err)
		}
	}
	_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS deployments_project_service_sha_status_idx ON deployments(project_id, service_id, git_sha, status)`)
	if err != nil {
		return fmt.Errorf("init deployments index: %w", err)
	}
	return nil
}

func (s *SQLiteStore) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func (s *SQLiteStore) UpsertService(ctx context.Context, service ServiceRecord) error {
	now := service.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	watchPathsValue := service.WatchPaths
	if watchPathsValue == nil {
		watchPathsValue = []string{}
	}
	watchPaths, err := json.Marshal(watchPathsValue)
	if err != nil {
		return fmt.Errorf("marshal watch paths: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO services(id, project_id, name, type, namespace, repo_url, branch, build_context, dockerfile, manifest_path, watch_paths, termination_grace_period_seconds, resource_requests_json, resource_limits_json, current_image_tag, health, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, id) DO UPDATE SET
  name = excluded.name,
  type = excluded.type,
  namespace = excluded.namespace,
  repo_url = excluded.repo_url,
  branch = excluded.branch,
  build_context = excluded.build_context,
  dockerfile = excluded.dockerfile,
  manifest_path = excluded.manifest_path,
  watch_paths = excluded.watch_paths,
  termination_grace_period_seconds = excluded.termination_grace_period_seconds,
  resource_requests_json = excluded.resource_requests_json,
  resource_limits_json = excluded.resource_limits_json,
  current_image_tag = excluded.current_image_tag,
  health = excluded.health,
  updated_at = excluded.updated_at
`, service.ID, service.ProjectID, service.Name, service.Type, service.Namespace, service.RepoURL, service.Branch, service.BuildContext, service.Dockerfile, service.ManifestPath, string(watchPaths), service.TerminationGracePeriodSeconds, service.ResourceRequestsJSON, service.ResourceLimitsJSON, service.CurrentImage, service.Health, now.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("upsert service: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Insert(ctx context.Context, record Record) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO deployments(deploy_id, project_id, service_id, service_name, started_at_unix, git_sha, image_tag, status, triggered_by, migration_ran, rollback_safe, rollback_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, record.DeployID, record.ProjectID, record.ServiceID, record.ServiceName, record.StartedAt.Unix(), record.GitSHA, record.ImageTag, record.Status, record.TriggeredBy, record.MigrationRan, record.RollbackSafe, record.RollbackReason)
	if err != nil {
		return fmt.Errorf("insert deployment: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Update(ctx context.Context, record Record) error {
	finished := int64(0)
	if !record.FinishedAt.IsZero() {
		finished = record.FinishedAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE deployments
SET finished_at_unix = ?, status = ?, duration_ms = ?, error = ?, image_tag = ?, migration_ran = ?, rollback_safe = ?, rollback_reason = ?
WHERE deploy_id = ?
`, finished, record.Status, record.Duration.Milliseconds(), record.Error, record.ImageTag, record.MigrationRan, record.RollbackSafe, record.RollbackReason, record.DeployID)
	if err != nil {
		return fmt.Errorf("update deployment: %w", err)
	}
	return nil
}

func (s *SQLiteStore) FindSuccessful(ctx context.Context, projectID, serviceID, gitSHA string) (*Record, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT deploy_id, project_id, service_id, service_name, started_at_unix, finished_at_unix, git_sha, image_tag, status, duration_ms, error, triggered_by, migration_ran, rollback_safe, rollback_reason
FROM deployments
WHERE project_id = ? AND service_id = ? AND git_sha = ? AND status = ?
ORDER BY started_at_unix DESC
LIMIT 1
`, projectID, serviceID, gitSHA, StatusSuccess)
	return scanRecord(row)
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(row scanner) (*Record, error) {
	var rec Record
	var startedUnix, finishedUnix int64
	var durationMS int64
	if err := row.Scan(&rec.DeployID, &rec.ProjectID, &rec.ServiceID, &rec.ServiceName, &startedUnix, &finishedUnix, &rec.GitSHA, &rec.ImageTag, &rec.Status, &durationMS, &rec.Error, &rec.TriggeredBy, &rec.MigrationRan, &rec.RollbackSafe, &rec.RollbackReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	rec.StartedAt = time.Unix(startedUnix, 0).UTC()
	if finishedUnix > 0 {
		rec.FinishedAt = time.Unix(finishedUnix, 0).UTC()
	}
	rec.Duration = time.Duration(durationMS) * time.Millisecond
	return &rec, nil
}
