package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	_ "modernc.org/sqlite"
)

type Store interface {
	UpsertService(ctx context.Context, service ServiceRecord) error
	Insert(ctx context.Context, record Record) error
	Update(ctx context.Context, record Record) error
	FindSuccessful(ctx context.Context, projectID, serviceID, gitSHA string) (*Record, error)
	BeginRollout(ctx context.Context, intent deploymentv1.RolloutIntent, resources []deploymentv1.ResourceIdentity) (*deploymentv1.RolloutRecord, error)
	GetRollout(ctx context.Context, rolloutID string) (*deploymentv1.RolloutRecord, error)
	ListNonTerminalRollouts(ctx context.Context, limit int) ([]deploymentv1.RolloutRecord, error)
	TransitionRollout(ctx context.Context, rolloutID, state string, failure *deploymentv1.RolloutError, resources []deploymentv1.ResourceIdentity, evidence *deploymentv1.ReadinessEvidence, terminal bool) (*deploymentv1.RolloutRecord, error)
	CommitRolloutSuccess(ctx context.Context, rolloutID string, snapshot deploymentv1.KnownGoodSnapshot, resources []deploymentv1.ResourceIdentity, evidence deploymentv1.ReadinessEvidence) (*deploymentv1.RolloutRecord, error)
	GetKnownGood(ctx context.Context, snapshotID string) (*deploymentv1.KnownGoodSnapshot, error)
	CurrentKnownGood(ctx context.Context, target deploymentv1.RuntimeTarget) (*deploymentv1.KnownGoodSnapshot, error)
	Close() error
}

type rolloutStoreError struct {
	Code string
	Msg  string
}

func (e *rolloutStoreError) Error() string { return e.Code + ": " + e.Msg }

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
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("configure sqlite busy timeout: %w", err)
	}
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
  rollback_reason TEXT NOT NULL DEFAULT '',
  spec_hash TEXT NOT NULL DEFAULT '',
  image_id TEXT NOT NULL DEFAULT '',
  namespace TEXT NOT NULL DEFAULT '',
  deployment_name TEXT NOT NULL DEFAULT '',
  kubernetes_service_name TEXT NOT NULL DEFAULT '',
  available_replicas INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS deployments_project_service_sha_status_idx
  ON deployments(project_id, service_id, git_sha, status);
CREATE TABLE IF NOT EXISTS rollouts (
  rollout_id TEXT PRIMARY KEY,
  schema_version TEXT NOT NULL,
  target_key TEXT NOT NULL,
  project_id TEXT NOT NULL,
  environment_id TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  service_key TEXT NOT NULL,
  intent_json TEXT NOT NULL,
  state TEXT NOT NULL,
  version INTEGER NOT NULL,
  state_hash TEXT NOT NULL,
  error_json TEXT NOT NULL DEFAULT '',
  resources_json TEXT NOT NULL DEFAULT '[]',
  evidence_json TEXT NOT NULL DEFAULT '',
  created_at_unix_nano INTEGER NOT NULL,
  updated_at_unix_nano INTEGER NOT NULL,
  terminal_at_unix_nano INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS rollouts_active_target_idx
  ON rollouts(target_key) WHERE terminal_at_unix_nano = 0;
CREATE INDEX IF NOT EXISTS rollouts_nonterminal_idx
  ON rollouts(terminal_at_unix_nano, updated_at_unix_nano);
CREATE TABLE IF NOT EXISTS rollout_events (
  rollout_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  event_json TEXT NOT NULL,
  created_at_unix_nano INTEGER NOT NULL,
  PRIMARY KEY(rollout_id, version)
);
CREATE TABLE IF NOT EXISTS known_good_snapshots (
  snapshot_id TEXT PRIMARY KEY,
  target_key TEXT NOT NULL,
  snapshot_hash TEXT NOT NULL UNIQUE,
  snapshot_json TEXT NOT NULL,
  verified_at_unix_nano INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS known_good_target_verified_idx
  ON known_good_snapshots(target_key, verified_at_unix_nano DESC);
CREATE TABLE IF NOT EXISTS known_good_current (
  target_key TEXT PRIMARY KEY,
  snapshot_id TEXT NOT NULL
);
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
		"project_id":              "ALTER TABLE deployments ADD COLUMN project_id TEXT NOT NULL DEFAULT ''",
		"service_id":              "ALTER TABLE deployments ADD COLUMN service_id TEXT NOT NULL DEFAULT ''",
		"service_name":            "ALTER TABLE deployments ADD COLUMN service_name TEXT NOT NULL DEFAULT ''",
		"migration_ran":           "ALTER TABLE deployments ADD COLUMN migration_ran BOOLEAN NOT NULL DEFAULT 0",
		"rollback_safe":           "ALTER TABLE deployments ADD COLUMN rollback_safe BOOLEAN NOT NULL DEFAULT 0",
		"rollback_reason":         "ALTER TABLE deployments ADD COLUMN rollback_reason TEXT NOT NULL DEFAULT ''",
		"spec_hash":               "ALTER TABLE deployments ADD COLUMN spec_hash TEXT NOT NULL DEFAULT ''",
		"image_id":                "ALTER TABLE deployments ADD COLUMN image_id TEXT NOT NULL DEFAULT ''",
		"namespace":               "ALTER TABLE deployments ADD COLUMN namespace TEXT NOT NULL DEFAULT ''",
		"deployment_name":         "ALTER TABLE deployments ADD COLUMN deployment_name TEXT NOT NULL DEFAULT ''",
		"kubernetes_service_name": "ALTER TABLE deployments ADD COLUMN kubernetes_service_name TEXT NOT NULL DEFAULT ''",
		"available_replicas":      "ALTER TABLE deployments ADD COLUMN available_replicas INTEGER NOT NULL DEFAULT 0",
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
INSERT INTO deployments(deploy_id, project_id, service_id, service_name, started_at_unix, git_sha, image_tag, status, triggered_by, migration_ran, rollback_safe, rollback_reason, spec_hash, image_id, namespace, deployment_name, kubernetes_service_name, available_replicas)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deploy_id) DO UPDATE SET
  status = excluded.status,
  image_tag = excluded.image_tag,
  spec_hash = excluded.spec_hash,
  error = '',
  finished_at_unix = 0,
  duration_ms = 0
WHERE deployments.project_id = excluded.project_id
  AND deployments.service_id = excluded.service_id
  AND deployments.git_sha = excluded.git_sha
`, record.DeployID, record.ProjectID, record.ServiceID, record.ServiceName, record.StartedAt.Unix(), record.GitSHA, record.ImageTag, record.Status, record.TriggeredBy, record.MigrationRan, record.RollbackSafe, record.RollbackReason, record.SpecHash, record.ImageID, record.Namespace, record.DeploymentName, record.KubernetesServiceName, record.AvailableReplicas)
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
SET finished_at_unix = ?, status = ?, duration_ms = ?, error = ?, image_tag = ?, migration_ran = ?, rollback_safe = ?, rollback_reason = ?, spec_hash = ?, image_id = ?, namespace = ?, deployment_name = ?, kubernetes_service_name = ?, available_replicas = ?
WHERE deploy_id = ?
`, finished, record.Status, record.Duration.Milliseconds(), record.Error, record.ImageTag, record.MigrationRan, record.RollbackSafe, record.RollbackReason, record.SpecHash, record.ImageID, record.Namespace, record.DeploymentName, record.KubernetesServiceName, record.AvailableReplicas, record.DeployID)
	if err != nil {
		return fmt.Errorf("update deployment: %w", err)
	}
	return nil
}

func (s *SQLiteStore) FindSuccessful(ctx context.Context, projectID, serviceID, gitSHA string) (*Record, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT deploy_id, project_id, service_id, service_name, started_at_unix, finished_at_unix, git_sha, image_tag, status, duration_ms, error, triggered_by, migration_ran, rollback_safe, rollback_reason, spec_hash, image_id, namespace, deployment_name, kubernetes_service_name, available_replicas
FROM deployments
WHERE project_id = ? AND service_id = ? AND git_sha = ? AND status = ?
ORDER BY started_at_unix DESC
LIMIT 1
`, projectID, serviceID, gitSHA, StatusSuccess)
	return scanRecord(row)
}

func (s *SQLiteStore) BeginRollout(ctx context.Context, intent deploymentv1.RolloutIntent, resources []deploymentv1.ResourceIdentity) (*deploymentv1.RolloutRecord, error) {
	canonical, err := intent.Canonicalize()
	if err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: err.Error()}
	}
	if len(resources) > deploymentv1.MaxRolloutResources {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "resource observation bound exceeded"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin rollout transaction: %w", err)
	}
	defer tx.Rollback()
	if existing, err := scanRollout(tx.QueryRowContext(ctx, `SELECT schema_version, intent_json, state, version, state_hash, error_json, resources_json, evidence_json, created_at_unix_nano, updated_at_unix_nano, terminal_at_unix_nano FROM rollouts WHERE rollout_id = ?`, canonical.RolloutID)); err == nil {
		if existing.Intent.IntentHash != canonical.IntentHash {
			return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeConflict, Msg: "rollout id already has a different intent"}
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read rollout identity: %w", err)
	}
	var activeID string
	err = tx.QueryRowContext(ctx, `SELECT rollout_id FROM rollouts WHERE target_key = ? AND terminal_at_unix_nano = 0`, canonical.Target.Key()).Scan(&activeID)
	if err == nil && activeID != canonical.RolloutID {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeTargetBusy, Msg: "another rollout owns this runtime target"}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check active rollout: %w", err)
	}
	var currentID, currentHash string
	err = tx.QueryRowContext(ctx, `SELECT c.snapshot_id, s.snapshot_hash FROM known_good_current c JOIN known_good_snapshots s ON s.snapshot_id = c.snapshot_id WHERE c.target_key = ?`, canonical.Target.Key()).Scan(&currentID, &currentHash)
	if errors.Is(err, sql.ErrNoRows) {
		if canonical.PreviousKnownGoodID != "" {
			return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeConflict, Msg: "previous known-good reference is stale"}
		}
	} else if err != nil {
		return nil, fmt.Errorf("check current known-good: %w", err)
	} else if currentID != canonical.PreviousKnownGoodID || currentHash != canonical.PreviousKnownGoodHash {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeConflict, Msg: "previous known-good reference is stale"}
	}
	now := time.Now().UTC()
	record := deploymentv1.RolloutRecord{SchemaVersion: deploymentv1.RolloutRecordVersion, Intent: canonical, State: deploymentv1.RolloutStatePrepared, Version: 1, Resources: append([]deploymentv1.ResourceIdentity(nil), resources...), CreatedAt: now, UpdatedAt: now}
	record.StateHash, err = record.CalculateStateHash()
	if err != nil {
		return nil, err
	}
	intentJSON, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("marshal rollout intent: %w", err)
	}
	resourcesJSON, err := json.Marshal(record.Resources)
	if err != nil {
		return nil, fmt.Errorf("marshal rollout resources: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO rollouts(rollout_id, schema_version, target_key, project_id, environment_id, runtime_id, service_key, intent_json, state, version, state_hash, resources_json, created_at_unix_nano, updated_at_unix_nano) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, canonical.RolloutID, deploymentv1.RolloutRecordVersion, canonical.Target.Key(), canonical.Target.ProjectID, canonical.Target.EnvironmentID, canonical.Target.RuntimeID, canonical.Target.ServiceKey, string(intentJSON), record.State, record.Version, record.StateHash, string(resourcesJSON), now.UnixNano(), now.UnixNano()); err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeTargetBusy, Msg: "rollout target was claimed concurrently"}
	}
	if err := appendRolloutEvent(ctx, tx, record); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit rollout intent: %w", err)
	}
	return &record, nil
}

func (s *SQLiteStore) GetRollout(ctx context.Context, rolloutID string) (*deploymentv1.RolloutRecord, error) {
	if rolloutID == "" {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "rollout id is required"}
	}
	record, err := scanRollout(s.db.QueryRowContext(ctx, `SELECT schema_version, intent_json, state, version, state_hash, error_json, resources_json, evidence_json, created_at_unix_nano, updated_at_unix_nano, terminal_at_unix_nano FROM rollouts WHERE rollout_id = ?`, rolloutID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rollout: %w", err)
	}
	return record, nil
}

func (s *SQLiteStore) ListNonTerminalRollouts(ctx context.Context, limit int) ([]deploymentv1.RolloutRecord, error) {
	if limit <= 0 || limit > 64 {
		limit = 64
	}
	rows, err := s.db.QueryContext(ctx, `SELECT schema_version, intent_json, state, version, state_hash, error_json, resources_json, evidence_json, created_at_unix_nano, updated_at_unix_nano, terminal_at_unix_nano FROM rollouts WHERE terminal_at_unix_nano = 0 ORDER BY updated_at_unix_nano, rollout_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list nonterminal rollouts: %w", err)
	}
	defer rows.Close()
	var result []deploymentv1.RolloutRecord
	for rows.Next() {
		record, err := scanRollout(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SQLiteStore) TransitionRollout(ctx context.Context, rolloutID, state string, failure *deploymentv1.RolloutError, resources []deploymentv1.ResourceIdentity, evidence *deploymentv1.ReadinessEvidence, terminal bool) (*deploymentv1.RolloutRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := scanRollout(tx.QueryRowContext(ctx, `SELECT schema_version, intent_json, state, version, state_hash, error_json, resources_json, evidence_json, created_at_unix_nano, updated_at_unix_nano, terminal_at_unix_nano FROM rollouts WHERE rollout_id = ?`, rolloutID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "rollout does not exist"}
	}
	if err != nil {
		return nil, err
	}
	if record.TerminalAt != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeTerminalImmutable, Msg: "terminal rollout cannot transition"}
	}
	if !deploymentv1.CanTransitionRollout(record.State, state) {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalidTransition, Msg: "rollout state transition is not allowed"}
	}
	if state == deploymentv1.RolloutStateSucceeded || (terminal && state != deploymentv1.RolloutStateFailed && !deploymentv1.IsTerminalRolloutState(state)) {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalidTransition, Msg: "known-good and terminal state transitions require their dedicated commit path"}
	}
	if len(resources) > deploymentv1.MaxRolloutResources {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "resource observation bound exceeded"}
	}
	if state == deploymentv1.RolloutStateRolledBack {
		if evidence == nil || evidence.Validate(true, false) != nil {
			return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeReadinessFailed, Msg: "rolled_back requires verified readiness evidence"}
		}
	}
	record.State = state
	record.Version++
	if failure != nil {
		record.Error = deploymentv1.NewRolloutError(failure.Code, failure.Message, failure.Retryable)
	} else {
		record.Error = nil
	}
	record.Resources = append([]deploymentv1.ResourceIdentity(nil), resources...)
	record.Evidence = evidence
	record.UpdatedAt = time.Now().UTC()
	if terminal || deploymentv1.IsTerminalRolloutState(state) {
		terminalAt := record.UpdatedAt
		record.TerminalAt = &terminalAt
	}
	record.StateHash, err = record.CalculateStateHash()
	if err != nil {
		return nil, err
	}
	if err := persistRolloutTransition(ctx, tx, *record); err != nil {
		return nil, err
	}
	if err := appendRolloutEvent(ctx, tx, *record); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *SQLiteStore) CommitRolloutSuccess(ctx context.Context, rolloutID string, snapshot deploymentv1.KnownGoodSnapshot, resources []deploymentv1.ResourceIdentity, evidence deploymentv1.ReadinessEvidence) (*deploymentv1.RolloutRecord, error) {
	canonical, err := snapshot.Canonicalize()
	if err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeKnownGoodCorrupt, Msg: err.Error()}
	}
	if err := evidence.Validate(true, false); err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeReadinessFailed, Msg: err.Error()}
	}
	evidenceHash, err := evidence.Hash()
	if err != nil || evidenceHash != canonical.EvidenceHash {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeKnownGoodCorrupt, Msg: "known-good evidence hash does not match readiness evidence"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := scanRollout(tx.QueryRowContext(ctx, `SELECT schema_version, intent_json, state, version, state_hash, error_json, resources_json, evidence_json, created_at_unix_nano, updated_at_unix_nano, terminal_at_unix_nano FROM rollouts WHERE rollout_id = ?`, rolloutID))
	if err != nil {
		return nil, err
	}
	if record.State == deploymentv1.RolloutStateSucceeded && record.TerminalAt != nil {
		return record, nil
	}
	if record.TerminalAt != nil || record.State != deploymentv1.RolloutStateWaiting {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalidTransition, Msg: "rollout is not waiting for readiness commit"}
	}
	if canonical.Target.Key() != record.Intent.Target.Key() {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeKnownGoodCorrupt, Msg: "known-good target does not match rollout"}
	}
	snapshotJSON, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO known_good_snapshots(snapshot_id, target_key, snapshot_hash, snapshot_json, verified_at_unix_nano) VALUES (?, ?, ?, ?, ?)`, canonical.ID, canonical.Target.Key(), canonical.SnapshotHash, string(snapshotJSON), canonical.VerifiedAt.UnixNano()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO known_good_current(target_key, snapshot_id) VALUES (?, ?) ON CONFLICT(target_key) DO UPDATE SET snapshot_id = excluded.snapshot_id`, canonical.Target.Key(), canonical.ID); err != nil {
		return nil, err
	}
	if err := pruneKnownGood(ctx, tx, canonical.Target.Key()); err != nil {
		return nil, err
	}
	record.State = deploymentv1.RolloutStateSucceeded
	record.Version++
	record.Error = nil
	record.Resources = append([]deploymentv1.ResourceIdentity(nil), resources...)
	record.Evidence = &evidence
	record.UpdatedAt = time.Now().UTC()
	terminalAt := record.UpdatedAt
	record.TerminalAt = &terminalAt
	record.StateHash, err = record.CalculateStateHash()
	if err != nil {
		return nil, err
	}
	if err := persistRolloutTransition(ctx, tx, *record); err != nil {
		return nil, err
	}
	if err := appendRolloutEvent(ctx, tx, *record); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *SQLiteStore) GetKnownGood(ctx context.Context, snapshotID string) (*deploymentv1.KnownGoodSnapshot, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM known_good_snapshots WHERE snapshot_id = ?`, snapshotID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot deploymentv1.KnownGoodSnapshot
	if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeKnownGoodCorrupt, Msg: "known-good JSON is invalid"}
	}
	if _, err := snapshot.Canonicalize(); err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeKnownGoodCorrupt, Msg: "known-good snapshot failed validation"}
	}
	return &snapshot, nil
}

func (s *SQLiteStore) CurrentKnownGood(ctx context.Context, target deploymentv1.RuntimeTarget) (*deploymentv1.KnownGoodSnapshot, error) {
	var snapshotID string
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_id FROM known_good_current WHERE target_key = ?`, target.Key()).Scan(&snapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetKnownGood(ctx, snapshotID)
}

func persistRolloutTransition(ctx context.Context, tx *sql.Tx, record deploymentv1.RolloutRecord) error {
	errorJSON := ""
	if record.Error != nil {
		data, err := json.Marshal(record.Error)
		if err != nil {
			return err
		}
		errorJSON = string(data)
	}
	resourcesJSON, err := json.Marshal(record.Resources)
	if err != nil {
		return err
	}
	evidenceJSON := ""
	if record.Evidence != nil {
		data, err := json.Marshal(record.Evidence)
		if err != nil {
			return err
		}
		evidenceJSON = string(data)
	}
	terminal := int64(0)
	if record.TerminalAt != nil {
		terminal = record.TerminalAt.UnixNano()
	}
	_, err = tx.ExecContext(ctx, `UPDATE rollouts SET state = ?, version = ?, state_hash = ?, error_json = ?, resources_json = ?, evidence_json = ?, updated_at_unix_nano = ?, terminal_at_unix_nano = ? WHERE rollout_id = ?`, record.State, record.Version, record.StateHash, errorJSON, string(resourcesJSON), evidenceJSON, record.UpdatedAt.UnixNano(), terminal, record.Intent.RolloutID)
	return err
}

func appendRolloutEvent(ctx context.Context, tx *sql.Tx, record deploymentv1.RolloutRecord) error {
	event := deploymentv1.RolloutEvent{SchemaVersion: deploymentv1.RolloutEventVersion, RolloutID: record.Intent.RolloutID, Version: record.Version, State: record.State, StateHash: record.StateHash, Error: record.Error, CreatedAt: record.UpdatedAt}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rollout_events(rollout_id, version, event_json, created_at_unix_nano) VALUES (?, ?, ?, ?)`, event.RolloutID, event.Version, string(data), event.CreatedAt.UnixNano())
	return err
}

func pruneKnownGood(ctx context.Context, tx *sql.Tx, targetKey string) error {
	rows, err := tx.QueryContext(ctx, `SELECT snapshot_id FROM known_good_snapshots WHERE target_key = ? ORDER BY verified_at_unix_nano DESC, snapshot_id DESC`, targetKey)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ids) <= 32 {
		return nil
	}
	for _, id := range ids[32:] {
		if _, err := tx.ExecContext(ctx, `DELETE FROM known_good_snapshots WHERE snapshot_id = ? AND snapshot_id NOT IN (SELECT snapshot_id FROM known_good_current)`, id); err != nil {
			return err
		}
	}
	return nil
}

func scanRollout(row interface{ Scan(...any) error }) (*deploymentv1.RolloutRecord, error) {
	var schema, intentJSON, state, stateHash, errorJSON, resourcesJSON, evidenceJSON string
	var version uint64
	var created, updated, terminal int64
	if err := row.Scan(&schema, &intentJSON, &state, &version, &stateHash, &errorJSON, &resourcesJSON, &evidenceJSON, &created, &updated, &terminal); err != nil {
		return nil, err
	}
	var intent deploymentv1.RolloutIntent
	if err := json.Unmarshal([]byte(intentJSON), &intent); err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "stored rollout intent is invalid"}
	}
	record := &deploymentv1.RolloutRecord{SchemaVersion: schema, Intent: intent, State: state, Version: version, StateHash: stateHash, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}
	if errorJSON != "" {
		var failure deploymentv1.RolloutError
		if err := json.Unmarshal([]byte(errorJSON), &failure); err != nil {
			return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "stored rollout error is invalid"}
		}
		record.Error = &failure
	}
	if err := json.Unmarshal([]byte(resourcesJSON), &record.Resources); err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "stored rollout resources are invalid"}
	}
	if evidenceJSON != "" {
		var evidence deploymentv1.ReadinessEvidence
		if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
			return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "stored rollout evidence is invalid"}
		}
		record.Evidence = &evidence
	}
	if terminal > 0 {
		value := time.Unix(0, terminal).UTC()
		record.TerminalAt = &value
	}
	if schema != deploymentv1.RolloutRecordVersion {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "stored rollout schema is unsupported"}
	}
	if _, err := intent.Canonicalize(); err != nil {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "stored rollout intent failed validation"}
	}
	calculated, err := record.CalculateStateHash()
	if err != nil || calculated != record.StateHash {
		return nil, &rolloutStoreError{Code: deploymentv1.RolloutCodeInvalid, Msg: "stored rollout state hash mismatch"}
	}
	return record, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(row scanner) (*Record, error) {
	var rec Record
	var startedUnix, finishedUnix int64
	var durationMS int64
	if err := row.Scan(&rec.DeployID, &rec.ProjectID, &rec.ServiceID, &rec.ServiceName, &startedUnix, &finishedUnix, &rec.GitSHA, &rec.ImageTag, &rec.Status, &durationMS, &rec.Error, &rec.TriggeredBy, &rec.MigrationRan, &rec.RollbackSafe, &rec.RollbackReason, &rec.SpecHash, &rec.ImageID, &rec.Namespace, &rec.DeploymentName, &rec.KubernetesServiceName, &rec.AvailableReplicas); err != nil {
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
