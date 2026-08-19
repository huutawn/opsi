package verificationstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

var ErrNotFound = errors.New("verification run not found")

type Store interface {
	Create(ctx context.Context, run verificationv1.VerificationRun) (verificationv1.VerificationRun, error)
	Update(ctx context.Context, run verificationv1.VerificationRun) (verificationv1.VerificationRun, error)
	Get(ctx context.Context, projectID, runID string) (verificationv1.VerificationRun, error)
	GetLatest(ctx context.Context, projectID, environmentID, consumerApplicationID, dependencyLogicalName string) (verificationv1.VerificationRun, error)
	ListForDeployment(ctx context.Context, projectID, deploymentJobID string) ([]verificationv1.VerificationRun, error)
}

type PostgresStore struct {
	DB     *sql.DB
	Random io.Reader
}

func (s PostgresStore) Create(ctx context.Context, r verificationv1.VerificationRun) (verificationv1.VerificationRun, error) {
	if s.DB == nil {
		return r, errors.New("database unavailable")
	}
	if r.ID == "" {
		id, err := newOpaqueID(s.random(), "dvr-")
		if err != nil {
			return r, err
		}
		r.ID = id
	}
	r.SchemaVersion = verificationv1.SchemaVersion
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}

	pHealth, _ := json.Marshal(r.ProviderHealth)
	cRes, _ := json.Marshal(r.ContractResolution)
	conn, _ := json.Marshal(r.Connection)
	cHealth, _ := json.Marshal(r.ConsumerHealth)
	cAssert, _ := json.Marshal(r.ConsumerAssertion)

	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO dependency_verification_runs(
			id, project_id, environment_id, consumer_application_id,
			dependency_logical_name, deployment_job_id, config_revision,
			target_binding_id, source_commit_sha, staleness_hash,
			provider_health, contract_resolution, connection,
			consumer_health, consumer_assertion, overall_status,
			failure_code, triggered_by, started_at, completed_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), NULLIF($9,''),
			$10, $11, $12, $13, $14, $15, $16, NULLIF($17,''), $18, $19, $20
		)
	`, r.ID, r.ProjectID, r.EnvironmentID, r.ConsumerApplicationID,
		r.DependencyLogicalName, r.DeploymentJobID, r.ConfigRevision,
		r.TargetBindingID, r.SourceCommitSHA, r.StalenessHash,
		pHealth, cRes, conn, cHealth, cAssert, r.OverallStatus,
		r.FailureCode, r.TriggeredBy, r.StartedAt, r.CompletedAt)
	if err != nil {
		return r, err
	}
	return r, nil
}

func (s PostgresStore) Update(ctx context.Context, r verificationv1.VerificationRun) (verificationv1.VerificationRun, error) {
	if s.DB == nil {
		return r, errors.New("database unavailable")
	}
	pHealth, _ := json.Marshal(r.ProviderHealth)
	cRes, _ := json.Marshal(r.ContractResolution)
	conn, _ := json.Marshal(r.Connection)
	cHealth, _ := json.Marshal(r.ConsumerHealth)
	cAssert, _ := json.Marshal(r.ConsumerAssertion)

	_, err := s.DB.ExecContext(ctx, `
		UPDATE dependency_verification_runs SET
			provider_health = $3,
			contract_resolution = $4,
			connection = $5,
			consumer_health = $6,
			consumer_assertion = $7,
			overall_status = $8,
			failure_code = NULLIF($9,''),
			completed_at = $10
		WHERE id = $1 AND project_id = $2
	`, r.ID, r.ProjectID, pHealth, cRes, conn, cHealth, cAssert,
		r.OverallStatus, r.FailureCode, r.CompletedAt)
	if err != nil {
		return r, err
	}
	return r, nil
}

func (s PostgresStore) Get(ctx context.Context, projectID, runID string) (verificationv1.VerificationRun, error) {
	if s.DB == nil {
		return verificationv1.VerificationRun{}, errors.New("database unavailable")
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, project_id, environment_id, consumer_application_id,
			dependency_logical_name, deployment_job_id, config_revision,
			COALESCE(target_binding_id, ''), COALESCE(source_commit_sha, ''), staleness_hash,
			provider_health, contract_resolution, connection,
			consumer_health, consumer_assertion, overall_status,
			COALESCE(failure_code, ''), triggered_by, started_at, completed_at
		FROM dependency_verification_runs
		WHERE id = $1 AND project_id = $2
	`, runID, projectID)
	return scanRun(row)
}

func (s PostgresStore) GetLatest(ctx context.Context, projectID, environmentID, consumerApplicationID, dependencyLogicalName string) (verificationv1.VerificationRun, error) {
	if s.DB == nil {
		return verificationv1.VerificationRun{}, errors.New("database unavailable")
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, project_id, environment_id, consumer_application_id,
			dependency_logical_name, deployment_job_id, config_revision,
			COALESCE(target_binding_id, ''), COALESCE(source_commit_sha, ''), staleness_hash,
			provider_health, contract_resolution, connection,
			consumer_health, consumer_assertion, overall_status,
			COALESCE(failure_code, ''), triggered_by, started_at, completed_at
		FROM dependency_verification_runs
		WHERE project_id = $1 AND environment_id = $2 AND consumer_application_id = $3 AND dependency_logical_name = $4
		ORDER BY started_at DESC
		LIMIT 1
	`, projectID, environmentID, consumerApplicationID, dependencyLogicalName)
	return scanRun(row)
}

func (s PostgresStore) ListForDeployment(ctx context.Context, projectID, deploymentJobID string) ([]verificationv1.VerificationRun, error) {
	if s.DB == nil {
		return nil, errors.New("database unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, project_id, environment_id, consumer_application_id,
			dependency_logical_name, deployment_job_id, config_revision,
			COALESCE(target_binding_id, ''), COALESCE(source_commit_sha, ''), staleness_hash,
			provider_health, contract_resolution, connection,
			consumer_health, consumer_assertion, overall_status,
			COALESCE(failure_code, ''), triggered_by, started_at, completed_at
		FROM dependency_verification_runs
		WHERE project_id = $1 AND deployment_job_id = $2
		ORDER BY started_at DESC
	`, projectID, deploymentJobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []verificationv1.VerificationRun
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (verificationv1.VerificationRun, error) {
	var r verificationv1.VerificationRun
	r.SchemaVersion = verificationv1.SchemaVersion
	var pHealthRaw, cResRaw, connRaw, cHealthRaw, cAssertRaw []byte
	err := row.Scan(
		&r.ID, &r.ProjectID, &r.EnvironmentID, &r.ConsumerApplicationID,
		&r.DependencyLogicalName, &r.DeploymentJobID, &r.ConfigRevision,
		&r.TargetBindingID, &r.SourceCommitSHA, &r.StalenessHash,
		&pHealthRaw, &cResRaw, &connRaw, &cHealthRaw, &cAssertRaw,
		&r.OverallStatus, &r.FailureCode, &r.TriggeredBy,
		&r.StartedAt, &r.CompletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return verificationv1.VerificationRun{}, ErrNotFound
	}
	if err != nil {
		return verificationv1.VerificationRun{}, err
	}
	if len(pHealthRaw) > 0 {
		_ = json.Unmarshal(pHealthRaw, &r.ProviderHealth)
	}
	if len(cResRaw) > 0 {
		_ = json.Unmarshal(cResRaw, &r.ContractResolution)
	}
	if len(connRaw) > 0 {
		_ = json.Unmarshal(connRaw, &r.Connection)
	}
	if len(cHealthRaw) > 0 {
		_ = json.Unmarshal(cHealthRaw, &r.ConsumerHealth)
	}
	if len(cAssertRaw) > 0 {
		_ = json.Unmarshal(cAssertRaw, &r.ConsumerAssertion)
	}
	return r, nil
}

func (s PostgresStore) random() io.Reader {
	if s.Random != nil {
		return s.Random
	}
	return rand.Reader
}

func newOpaqueID(r io.Reader, prefix string) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
