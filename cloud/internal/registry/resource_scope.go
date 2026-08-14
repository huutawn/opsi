package registry

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Service) EnvironmentExists(_ context.Context, projectID, environmentID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	environment, ok := s.envs[environmentID]
	return ok && environment.ProjectID == projectID, nil
}

func (s *Service) RuntimeBelongs(_ context.Context, projectID, environmentID, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime, ok := s.runtimes[runtimeID]
	return ok && runtime.ProjectID == projectID && runtime.EnvironmentID == environmentID, nil
}

func (s *Service) ApplicationBelongs(_ context.Context, projectID, environmentID, applicationID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	application, ok := s.services[applicationID]
	return ok && application.ProjectID == projectID && application.EnvironmentID == environmentID, nil
}

func (s PostgresService) EnvironmentExists(ctx context.Context, projectID, environmentID string) (bool, error) {
	return exists(ctx, s.DB, `SELECT EXISTS(SELECT 1 FROM environments WHERE project_id=$1 AND id=$2)`, projectID, environmentID)
}

func (s PostgresService) RuntimeBelongs(ctx context.Context, projectID, environmentID, runtimeID string) (bool, error) {
	return exists(ctx, s.DB, `SELECT EXISTS(SELECT 1 FROM runtimes WHERE project_id=$1 AND environment_id=$2 AND id=$3)`, projectID, environmentID, runtimeID)
}

func (s PostgresService) ApplicationBelongs(ctx context.Context, projectID, environmentID, applicationID string) (bool, error) {
	return exists(ctx, s.DB, `SELECT EXISTS(SELECT 1 FROM control_services WHERE project_id=$1 AND environment_id=$2 AND id=$3)`, projectID, environmentID, applicationID)
}

func exists(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var value bool
	err := db.QueryRowContext(ctx, query, args...).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value, err
}
