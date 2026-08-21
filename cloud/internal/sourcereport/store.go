package sourcereport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/sourcescanner"
)

var ErrNotFound = errors.New("source risk report not found")

type Report struct {
	ID              string                         `json:"id"`
	ProjectID       string                         `json:"project_id"`
	ApplicationID   string                         `json:"application_id"`
	RepositoryID    int64                          `json:"repository_id"`
	CommitSHA       string                         `json:"commit_sha"`
	ApplicationRoot string                         `json:"application_root"`
	ScannerVersion  string                         `json:"scanner_version"`
	BuildJobID      string                         `json:"build_job_id,omitempty"`
	AnalysisStatus  string                         `json:"analysis_status"`
	FilesScanned    int                            `json:"files_scanned"`
	BytesScanned    int64                          `json:"bytes_scanned"`
	Truncated       bool                           `json:"truncated"`
	Findings        []sourcescanner.Finding        `json:"findings"`
	EnvReferences   []sourcescanner.EnvReference   `json:"env_references"`
	ReportHash      string                         `json:"report_hash"`
	CreatedAt       time.Time                      `json:"created_at"`
}

type Store interface {
	Upsert(ctx context.Context, report Report) (Report, bool, error)
	GetLatest(ctx context.Context, projectID, applicationID string, repositoryID int64, commitSHA, applicationRoot, scannerVersion string) (Report, error)
	GetForBuildJob(ctx context.Context, buildJobID string) (Report, error)
	GetForCommit(ctx context.Context, projectID, applicationID, commitSHA string) (Report, error)
}

type PostgresStore struct {
	DB     *sql.DB
	Random io.Reader
}

func (s PostgresStore) Upsert(ctx context.Context, r Report) (Report, bool, error) {
	if s.DB == nil {
		return Report{}, false, errors.New("database unavailable")
	}
	if r.ID == "" {
		id, err := newOpaqueID(s.random(), "srr-")
		if err != nil {
			return Report{}, false, err
		}
		r.ID = id
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	findingsJSON, err := json.Marshal(r.Findings)
	if err != nil {
		return Report{}, false, err
	}
	envRefsJSON, err := json.Marshal(r.EnvReferences)
	if err != nil {
		return Report{}, false, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, false, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO source_risk_reports(
			id, project_id, application_id, repository_id, resolved_commit_sha,
			application_root, scanner_version, build_job_id, analysis_status,
			files_scanned, bytes_scanned, truncated, findings, env_references,
			report_hash, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (project_id, application_id, repository_id, resolved_commit_sha, application_root, scanner_version)
		DO UPDATE SET
			build_job_id = COALESCE(NULLIF(EXCLUDED.build_job_id,''), source_risk_reports.build_job_id),
			analysis_status = EXCLUDED.analysis_status,
			files_scanned = EXCLUDED.files_scanned,
			bytes_scanned = EXCLUDED.bytes_scanned,
			truncated = EXCLUDED.truncated,
			findings = EXCLUDED.findings,
			env_references = EXCLUDED.env_references,
			report_hash = EXCLUDED.report_hash
	`, r.ID, r.ProjectID, r.ApplicationID, r.RepositoryID, r.CommitSHA,
		r.ApplicationRoot, r.ScannerVersion, r.BuildJobID, r.AnalysisStatus,
		r.FilesScanned, r.BytesScanned, r.Truncated, findingsJSON, envRefsJSON,
		r.ReportHash, r.CreatedAt)
	if err != nil {
		return Report{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return Report{}, false, err
	}
	rows, _ := result.RowsAffected()
	return r, rows > 0, nil
}

func (s PostgresStore) GetLatest(ctx context.Context, projectID, applicationID string, repositoryID int64, commitSHA, applicationRoot, scannerVersion string) (Report, error) {
	if s.DB == nil {
		return Report{}, errors.New("database unavailable")
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, project_id, application_id, repository_id, resolved_commit_sha,
			application_root, scanner_version, COALESCE(build_job_id, ''), analysis_status,
			files_scanned, bytes_scanned, truncated, findings, env_references,
			report_hash, created_at
		FROM source_risk_reports
		WHERE project_id=$1 AND application_id=$2 AND repository_id=$3 AND resolved_commit_sha=$4 AND application_root=$5 AND scanner_version=$6
		ORDER BY created_at DESC
		LIMIT 1
	`, projectID, applicationID, repositoryID, commitSHA, applicationRoot, scannerVersion)
	return scanReport(row)
}

func (s PostgresStore) GetForBuildJob(ctx context.Context, buildJobID string) (Report, error) {
	if s.DB == nil {
		return Report{}, errors.New("database unavailable")
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, project_id, application_id, repository_id, resolved_commit_sha,
			application_root, scanner_version, COALESCE(build_job_id, ''), analysis_status,
			files_scanned, bytes_scanned, truncated, findings, env_references,
			report_hash, created_at
		FROM source_risk_reports
		WHERE build_job_id=$1
		ORDER BY created_at DESC
		LIMIT 1
	`, buildJobID)
	return scanReport(row)
}

func (s PostgresStore) GetForCommit(ctx context.Context, projectID, applicationID, commitSHA string) (Report, error) {
	if s.DB == nil {
		return Report{}, errors.New("database unavailable")
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, project_id, application_id, repository_id, resolved_commit_sha,
			application_root, scanner_version, COALESCE(build_job_id, ''), analysis_status,
			files_scanned, bytes_scanned, truncated, findings, env_references,
			report_hash, created_at
		FROM source_risk_reports
		WHERE project_id=$1 AND application_id=$2 AND resolved_commit_sha=$3
		ORDER BY created_at DESC
		LIMIT 1
	`, projectID, applicationID, commitSHA)
	return scanReport(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanReport(row rowScanner) (Report, error) {
	var r Report
	var findingsRaw, envRefsRaw []byte
	err := row.Scan(
		&r.ID, &r.ProjectID, &r.ApplicationID, &r.RepositoryID, &r.CommitSHA,
		&r.ApplicationRoot, &r.ScannerVersion, &r.BuildJobID, &r.AnalysisStatus,
		&r.FilesScanned, &r.BytesScanned, &r.Truncated, &findingsRaw, &envRefsRaw,
		&r.ReportHash, &r.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, ErrNotFound
	}
	if err != nil {
		return Report{}, err
	}
	if len(findingsRaw) > 0 {
		_ = json.Unmarshal(findingsRaw, &r.Findings)
	}
	if len(envRefsRaw) > 0 {
		_ = json.Unmarshal(envRefsRaw, &r.EnvReferences)
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
