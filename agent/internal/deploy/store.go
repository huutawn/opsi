package deploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store interface {
	Insert(ctx context.Context, record Record) error
	Update(ctx context.Context, record Record) error
	FindSuccessful(ctx context.Context, service, gitSHA string) (*Record, error)
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
CREATE TABLE IF NOT EXISTS deployments (
  deploy_id TEXT PRIMARY KEY,
  started_at_unix INTEGER NOT NULL,
  finished_at_unix INTEGER NOT NULL DEFAULT 0,
  service TEXT NOT NULL,
  git_sha TEXT NOT NULL,
  image_tag TEXT NOT NULL,
  status TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  triggered_by TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS deployments_service_sha_status_idx
  ON deployments(service, git_sha, status);
`)
	if err != nil {
		return fmt.Errorf("init deployments schema: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Insert(ctx context.Context, record Record) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO deployments(deploy_id, started_at_unix, service, git_sha, image_tag, status, triggered_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, record.DeployID, record.StartedAt.Unix(), record.Service, record.GitSHA, record.ImageTag, record.Status, record.TriggeredBy)
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
SET finished_at_unix = ?, status = ?, duration_ms = ?, error = ?, image_tag = ?
WHERE deploy_id = ?
`, finished, record.Status, record.Duration.Milliseconds(), record.Error, record.ImageTag, record.DeployID)
	if err != nil {
		return fmt.Errorf("update deployment: %w", err)
	}
	return nil
}

func (s *SQLiteStore) FindSuccessful(ctx context.Context, service, gitSHA string) (*Record, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT deploy_id, started_at_unix, finished_at_unix, service, git_sha, image_tag, status, duration_ms, error, triggered_by
FROM deployments
WHERE service = ? AND git_sha = ? AND status = ?
ORDER BY started_at_unix DESC
LIMIT 1
`, service, gitSHA, StatusSuccess)
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
	if err := row.Scan(&rec.DeployID, &startedUnix, &finishedUnix, &rec.Service, &rec.GitSHA, &rec.ImageTag, &rec.Status, &durationMS, &rec.Error, &rec.TriggeredBy); err != nil {
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
