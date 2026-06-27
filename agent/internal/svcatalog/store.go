package svcatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type ManagedService struct {
	ID            string
	ProjectID     string
	Name          string
	Type          string
	Namespace     string
	Mode          string
	Status        string
	Host          string
	Port          string
	Version       string
	Config        map[string]string
	SecretName    string
	ConfigMapName string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type ServiceBinding struct {
	ProjectID           string
	AppServiceID        string
	DependencyServiceID string
	Namespace           string
	EnvPolicy           map[string]string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store := &Store{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("enable sqlite wal: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS managed_services (
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  namespace TEXT NOT NULL,
  mode TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'unknown',
  host TEXT NOT NULL,
  port TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  config_json TEXT NOT NULL DEFAULT '{}',
  secret_name TEXT NOT NULL,
  configmap_name TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(project_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS managed_services_project_name_idx
  ON managed_services(project_id, name);
CREATE TABLE IF NOT EXISTS service_bindings (
  project_id TEXT NOT NULL,
  app_service_id TEXT NOT NULL,
  dependency_service_id TEXT NOT NULL,
  namespace TEXT NOT NULL,
  env_policy_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(project_id, app_service_id, dependency_service_id)
);
`)
	if err != nil {
		return fmt.Errorf("init service catalog schema: %w", err)
	}
	return nil
}

func (s *Store) UpsertManagedService(ctx context.Context, service ManagedService) error {
	now := service.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	created := service.CreatedAt
	if created.IsZero() {
		created = now
	}
	config, err := json.Marshal(nonNilMap(service.Config))
	if err != nil {
		return fmt.Errorf("marshal service config: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO managed_services(id, project_id, name, type, namespace, mode, status, host, port, version, config_json, secret_name, configmap_name, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, id) DO UPDATE SET
  name = excluded.name,
  type = excluded.type,
  namespace = excluded.namespace,
  mode = excluded.mode,
  status = excluded.status,
  host = excluded.host,
  port = excluded.port,
  version = excluded.version,
  config_json = excluded.config_json,
  secret_name = excluded.secret_name,
  configmap_name = excluded.configmap_name,
  updated_at = excluded.updated_at
`, service.ID, service.ProjectID, service.Name, service.Type, service.Namespace, service.Mode, defaultString(service.Status, "unknown"), service.Host, service.Port, service.Version, string(config), service.SecretName, service.ConfigMapName, created.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("upsert managed service: %w", err)
	}
	return nil
}

func (s *Store) GetManagedService(ctx context.Context, projectID, id string) (*ManagedService, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, name, type, namespace, mode, status, host, port, version, config_json, secret_name, configmap_name, created_at, updated_at
FROM managed_services
WHERE project_id = ? AND id = ?
`, projectID, id)
	return scanManagedService(row)
}

func (s *Store) DeleteManagedService(ctx context.Context, projectID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM managed_services WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete managed service: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM service_bindings WHERE project_id = ? AND dependency_service_id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete service bindings: %w", err)
	}
	return nil
}

func (s *Store) UpsertBinding(ctx context.Context, binding ServiceBinding) error {
	now := binding.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	created := binding.CreatedAt
	if created.IsZero() {
		created = now
	}
	policy, err := json.Marshal(nonNilMap(binding.EnvPolicy))
	if err != nil {
		return fmt.Errorf("marshal env policy: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO service_bindings(project_id, app_service_id, dependency_service_id, namespace, env_policy_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, app_service_id, dependency_service_id) DO UPDATE SET
  namespace = excluded.namespace,
  env_policy_json = excluded.env_policy_json,
  updated_at = excluded.updated_at
`, binding.ProjectID, binding.AppServiceID, binding.DependencyServiceID, binding.Namespace, string(policy), created.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("upsert service binding: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

type scanner interface {
	Scan(dest ...any) error
}

func scanManagedService(row scanner) (*ManagedService, error) {
	var service ManagedService
	var configJSON string
	var created, updated int64
	err := row.Scan(&service.ID, &service.ProjectID, &service.Name, &service.Type, &service.Namespace, &service.Mode, &service.Status, &service.Host, &service.Port, &service.Version, &configJSON, &service.SecretName, &service.ConfigMapName, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(configJSON), &service.Config); err != nil {
		return nil, fmt.Errorf("decode service config: %w", err)
	}
	service.CreatedAt = time.Unix(created, 0).UTC()
	service.UpdatedAt = time.Unix(updated, 0).UTC()
	return &service, nil
}

func nonNilMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
