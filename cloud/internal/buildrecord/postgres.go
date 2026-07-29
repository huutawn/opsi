package buildrecord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Create(ctx context.Context, payloadHash string, record buildrecordv1.Record) (buildrecordv1.Record, bool, error) {
	if s.DB == nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO build_records(
		id,schema_version,project_id,repository_id,repository_owner_id,active_binding_id,service_id,service_key,
		issuer,subject,ref,sha,event_name,workflow,workflow_ref,job_workflow_ref,run_id,run_attempt,
		config_hash,plan_hash,platform,oci_repository,oci_digest,provenance_digest,build_status,payload_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),$17,$18,$19,NULLIF($20,''),$21,$22,$23,NULLIF($24,''),$25,$26,$27)
	ON CONFLICT (repository_id,run_id,run_attempt,service_key) DO NOTHING`,
		record.ID, record.SchemaVersion, record.ProjectID, record.RepositoryID, record.RepositoryOwnerID, record.ActiveBindingID, record.ServiceID, record.ServiceKey,
		record.Workload.Issuer, record.Workload.Subject, record.Workload.Ref, record.Workload.SHA, record.Workload.EventName, record.Workload.Workflow, record.Workload.WorkflowRef, record.Workload.JobWorkflowRef, record.Workload.RunID, record.Workload.RunAttempt,
		record.Build.ConfigHash, record.Build.PlanHash, record.Build.Platform, record.Build.OCIRepository, record.Build.OCIDigest, record.Build.ProvenanceDigest, record.Build.Status, payloadHash, record.CreatedAt)
	if err != nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	if rows == 1 {
		if err := tx.Commit(); err != nil {
			return buildrecordv1.Record{}, false, unavailable()
		}
		return record, false, nil
	}
	current, currentHash, err := scanRecord(tx.QueryRowContext(ctx, selectRecordColumns+` WHERE repository_id=$1 AND run_id=$2 AND run_attempt=$3 AND service_key=$4`, record.RepositoryID, record.Workload.RunID, record.Workload.RunAttempt, record.ServiceKey))
	if err != nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	if currentHash != payloadHash {
		return buildrecordv1.Record{}, false, Error{Code: "BUILD_RECORD_CONFLICT", Status: 409, Message: "build identity was already submitted with different metadata"}
	}
	if err := tx.Commit(); err != nil {
		return buildrecordv1.Record{}, false, unavailable()
	}
	return current, true, nil
}

func (s PostgresStore) List(ctx context.Context, projectID string, filter ListFilter) (buildrecordv1.ListResult, error) {
	if s.DB == nil {
		return buildrecordv1.ListResult{}, unavailable()
	}
	query := selectRecordColumns + ` WHERE project_id=$1`
	args := []any{projectID}
	add := func(clause string, value any) { args = append(args, value); query += fmt.Sprintf(clause, len(args)) }
	if filter.ServiceKey != "" {
		add(` AND service_key=$%d`, filter.ServiceKey)
	}
	if filter.RepositoryID != 0 {
		add(` AND repository_id=$%d`, filter.RepositoryID)
	}
	if filter.SHA != "" {
		add(` AND sha=$%d`, filter.SHA)
	}
	if filter.Status != "" {
		add(` AND build_status=$%d`, filter.Status)
	}
	if filter.Cursor != "" {
		cursor, err := decodeCursor(filter.Cursor)
		if err != nil {
			return buildrecordv1.ListResult{}, invalid("BUILD_RECORD_CURSOR_INVALID", "cursor is invalid")
		}
		args = append(args, cursor.Time, cursor.ID)
		query += fmt.Sprintf(` AND (created_at,id) < ($%d,$%d)`, len(args)-1, len(args))
	}
	args = append(args, filter.Limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return buildrecordv1.ListResult{}, unavailable()
	}
	defer rows.Close()
	result := buildrecordv1.ListResult{Records: []buildrecordv1.Record{}}
	for rows.Next() {
		record, _, err := scanRecord(rows)
		if err != nil {
			return buildrecordv1.ListResult{}, unavailable()
		}
		result.Records = append(result.Records, record)
	}
	if err := rows.Err(); err != nil {
		return buildrecordv1.ListResult{}, unavailable()
	}
	if len(result.Records) > filter.Limit {
		last := result.Records[filter.Limit-1]
		result.Records = result.Records[:filter.Limit]
		result.NextCursor = encodeCursor(cursorValue{Time: last.CreatedAt, ID: last.ID})
	}
	return result, nil
}

func (s PostgresStore) Get(ctx context.Context, projectID, recordID string) (buildrecordv1.Record, error) {
	if s.DB == nil {
		return buildrecordv1.Record{}, unavailable()
	}
	record, _, err := scanRecord(s.DB.QueryRowContext(ctx, selectRecordColumns+` WHERE project_id=$1 AND id=$2`, projectID, recordID))
	if errors.Is(err, sql.ErrNoRows) {
		return buildrecordv1.Record{}, Error{Code: "BUILD_RECORD_NOT_FOUND", Status: 404, Message: "build record was not found"}
	}
	if err != nil {
		return buildrecordv1.Record{}, unavailable()
	}
	return record, nil
}

const selectRecordColumns = `SELECT id,schema_version,project_id,repository_id,repository_owner_id,active_binding_id,service_id,service_key,issuer,subject,ref,sha,event_name,workflow,workflow_ref,COALESCE(job_workflow_ref,''),run_id,run_attempt,config_hash,COALESCE(plan_hash,''),platform,oci_repository,oci_digest,COALESCE(provenance_digest,''),build_status,payload_hash,created_at FROM build_records`

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner) (buildrecordv1.Record, string, error) {
	var record buildrecordv1.Record
	var payloadHash string
	err := row.Scan(&record.ID, &record.SchemaVersion, &record.ProjectID, &record.RepositoryID, &record.RepositoryOwnerID, &record.ActiveBindingID, &record.ServiceID, &record.ServiceKey, &record.Workload.Issuer, &record.Workload.Subject, &record.Workload.Ref, &record.Workload.SHA, &record.Workload.EventName, &record.Workload.Workflow, &record.Workload.WorkflowRef, &record.Workload.JobWorkflowRef, &record.Workload.RunID, &record.Workload.RunAttempt, &record.Build.ConfigHash, &record.Build.PlanHash, &record.Build.Platform, &record.Build.OCIRepository, &record.Build.OCIDigest, &record.Build.ProvenanceDigest, &record.Build.Status, &payloadHash, &record.CreatedAt)
	record.Workload.RepositoryID = record.RepositoryID
	record.Workload.RepositoryOwnerID = record.RepositoryOwnerID
	return record, payloadHash, err
}
