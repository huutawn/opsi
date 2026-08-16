package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type PostgresStore struct{ DB *sql.DB }

const backupColumns = `id,project_id,environment_id,source_resource_id,source_node_id,resource_type,backup_type,source_database,source_postgres_version,source_profile,source_image,source_spec_revision,source_spec_hash,source_pvc_name,source_pvc_uid,source_pv_name,source_pv_uid,source_storage_hash,dump_format,dump_options,lifecycle,store_id,object_key,object_etag,object_version_id,artifact_size,sha256,pg_dump_version,archive_verified,requested_by,requested_at,created_at,leased_at,started_at,completed_at,failure_code,failure_message_redacted,attempt_count,lease_token,lease_expires_at`

var qualifiedBackupColumns = "b." + strings.ReplaceAll(backupColumns, ",", ",b.")

func (s PostgresStore) Create(ctx context.Context, value backupv1.Backup, key, payload string) (backupv1.Backup, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return backupv1.Backup{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, value.ProjectID+"\x1f"+key); err != nil {
		return backupv1.Backup{}, false, err
	}
	var id, previousPayload string
	err = tx.QueryRowContext(ctx, `SELECT backup_id,payload_hash FROM backup_idempotency WHERE project_id=$1 AND idempotency_key=$2`, value.ProjectID, key).Scan(&id, &previousPayload)
	if err == nil {
		if previousPayload != payload {
			return backupv1.Backup{}, false, Error{Code: "BACKUP_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another backup request"}
		}
		loaded, getErr := getBackup(ctx, tx, value.ProjectID, id)
		return loaded, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return backupv1.Backup{}, false, err
	}
	options, _ := json.Marshal(value.DumpOptions)
	_, err = tx.ExecContext(ctx, `INSERT INTO backups(id,project_id,environment_id,source_resource_id,source_node_id,resource_type,backup_type,source_database,source_postgres_version,source_profile,source_image,source_spec_revision,source_spec_hash,source_pvc_name,source_pvc_uid,source_pv_name,source_pv_uid,source_storage_hash,dump_format,dump_options,lifecycle,store_id,object_key,requested_by,requested_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),NULLIF($17,''),$18,$19,$20::jsonb,$21,$22,$23,$24,$25,$26)`, value.ID, value.ProjectID, value.EnvironmentID, value.SourceResourceID, value.SourceNodeID, value.ResourceType, value.BackupType, value.SourceDatabase, value.SourcePostgresVersion, value.SourceProfile, value.SourceImage, value.SourceSpecRevision, value.SourceSpecHash, value.SourcePVCName, value.SourcePVCUID, value.SourcePVName, value.SourcePVUID, value.SourceStorageHash, value.Format, string(options), value.Lifecycle, value.StoreID, value.ObjectKey, value.RequestedBy, value.RequestedAt, value.CreatedAt)
	if err != nil {
		if constraint(err) == "backups_one_active_per_resource_uidx" {
			return backupv1.Backup{}, false, Error{Code: backupv1.FailureAlreadyRunning, Status: 409, Message: "a logical backup is already active for this resource"}
		}
		return backupv1.Backup{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO backup_idempotency(project_id,idempotency_key,payload_hash,backup_id,created_at) VALUES($1,$2,$3,$4,$5)`, value.ProjectID, key, payload, value.ID, value.CreatedAt); err != nil {
		return backupv1.Backup{}, false, err
	}
	return value, false, tx.Commit()
}

func (s PostgresStore) Get(ctx context.Context, projectID, backupID string) (backupv1.Backup, error) {
	return getBackup(ctx, s.DB, projectID, backupID)
}

func (s PostgresStore) List(ctx context.Context, projectID, resourceID string) ([]backupv1.Backup, error) {
	query, args := `SELECT `+backupColumns+` FROM backups WHERE project_id=$1`, []any{projectID}
	if resourceID != "" {
		query += ` AND source_resource_id=$2`
		args = append(args, resourceID)
	}
	query += ` ORDER BY created_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backupv1.Backup{}
	for rows.Next() {
		value, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s PostgresStore) Claim(ctx context.Context, projectID, nodeID, token string, now, expires time.Time) (backupv1.Backup, bool, error) {
	row := s.DB.QueryRowContext(ctx, `WITH candidate AS (
		SELECT id FROM backups WHERE project_id=$1 AND source_node_id=$2 AND lifecycle IN ('queued','leased','running') AND (lifecycle='queued' OR lease_expires_at<=$3)
		ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE backups b SET lifecycle='leased',lease_token=$4,lease_expires_at=$5,leased_at=$3,attempt_count=b.attempt_count+1
	FROM candidate c WHERE b.id=c.id RETURNING `+qualifiedBackupColumns, projectID, nodeID, now, token, expires)
	value, err := scanBackup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return backupv1.Backup{}, false, nil
	}
	return value, err == nil, err
}

func (s PostgresStore) UpdateClaimed(ctx context.Context, value backupv1.Backup, token string) (backupv1.Backup, error) {
	terminal := value.Lifecycle == backupv1.LifecycleSucceeded || value.Lifecycle == backupv1.LifecycleFailed
	row := s.DB.QueryRowContext(ctx, `UPDATE backups SET lifecycle=$1,source_postgres_version=NULLIF($2,''),object_etag=NULLIF($3,''),object_version_id=NULLIF($4,''),artifact_size=NULLIF($5,0),sha256=NULLIF($6,''),pg_dump_version=NULLIF($7,''),archive_verified=$8,started_at=$9,completed_at=$10,failure_code=NULLIF($11,''),failure_message_redacted=NULLIF($12,''),lease_token=CASE WHEN $13 THEN NULL ELSE lease_token END,lease_expires_at=CASE WHEN $13 THEN NULL::timestamptz ELSE $14::timestamptz END WHERE project_id=$15 AND id=$16 AND lease_token=$17 AND lifecycle<>'succeeded' RETURNING `+backupColumns, value.Lifecycle, value.SourcePostgresVersion, value.ObjectETag, value.ObjectVersionID, value.ArtifactSize, value.SHA256, value.PGDumpVersion, value.ArchiveVerified, value.StartedAt, value.CompletedAt, value.FailureCode, value.FailureMessageRedacted, terminal, value.LeaseExpiresAt, value.ProjectID, value.ID, token)
	updated, err := scanBackup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return backupv1.Backup{}, invalid(backupv1.FailureLeaseLost, "backup lease is invalid")
	}
	return updated, err
}

func (s PostgresStore) HasActive(ctx context.Context, projectID, resourceID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backups WHERE project_id=$1 AND source_resource_id=$2 AND lifecycle IN ('queued','leased','running'))`, projectID, resourceID).Scan(&exists)
	return exists, err
}

type rowScanner interface{ Scan(...any) error }

func getBackup(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, backupID string) (backupv1.Backup, error) {
	value, err := scanBackup(q.QueryRowContext(ctx, `SELECT `+backupColumns+` FROM backups WHERE project_id=$1 AND id=$2`, projectID, backupID))
	if errors.Is(err, sql.ErrNoRows) {
		return backupv1.Backup{}, ErrNotFound
	}
	return value, err
}

func scanBackup(row rowScanner) (backupv1.Backup, error) {
	var value backupv1.Backup
	var resourceType string
	var options []byte
	var postgresVersion, sourceProfile, sourceImage, pvName, pvUID, etag, versionID, sha, pgDump, failureCode, failureMessage, leaseToken sql.NullString
	var artifactSize sql.NullInt64
	var leasedAt, startedAt, completedAt, leaseExpiresAt sql.NullTime
	err := row.Scan(&value.ID, &value.ProjectID, &value.EnvironmentID, &value.SourceResourceID, &value.SourceNodeID, &resourceType, &value.BackupType, &value.SourceDatabase, &postgresVersion, &sourceProfile, &sourceImage, &value.SourceSpecRevision, &value.SourceSpecHash, &value.SourcePVCName, &value.SourcePVCUID, &pvName, &pvUID, &value.SourceStorageHash, &value.Format, &options, &value.Lifecycle, &value.StoreID, &value.ObjectKey, &etag, &versionID, &artifactSize, &sha, &pgDump, &value.ArchiveVerified, &value.RequestedBy, &value.RequestedAt, &value.CreatedAt, &leasedAt, &startedAt, &completedAt, &failureCode, &failureMessage, &value.AttemptCount, &leaseToken, &leaseExpiresAt)
	if err != nil {
		return backupv1.Backup{}, err
	}
	value.SchemaVersion, value.ResourceType = backupv1.SchemaVersion, resourcev1.Type(resourceType)
	value.SourcePostgresVersion, value.SourceProfile, value.SourceImage, value.SourcePVName, value.SourcePVUID = postgresVersion.String, sourceProfile.String, sourceImage.String, pvName.String, pvUID.String
	value.ObjectETag, value.ObjectVersionID, value.ArtifactSize, value.SHA256, value.PGDumpVersion = etag.String, versionID.String, artifactSize.Int64, sha.String, pgDump.String
	value.FailureCode, value.FailureMessageRedacted, value.LeaseToken = failureCode.String, failureMessage.String, leaseToken.String
	if leasedAt.Valid {
		value.LeasedAt = &leasedAt.Time
	}
	if startedAt.Valid {
		value.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		value.CompletedAt = &completedAt.Time
	}
	if leaseExpiresAt.Valid {
		value.LeaseExpiresAt = leaseExpiresAt.Time
	}
	if err := json.Unmarshal(options, &value.DumpOptions); err != nil {
		return backupv1.Backup{}, err
	}
	return value, nil
}

func constraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}
