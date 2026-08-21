package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func (s PostgresStore) RetainAndDeleteClaimed(ctx context.Context, resource resourcev1.Resource, retained resourcev1.RetainedStorage, token string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	assignment, _ := json.Marshal(retained.Assignment)
	if _, err = tx.ExecContext(ctx, `INSERT INTO retained_storages(
		id,original_resource_id,project_id,environment_id,resource_type,resource_name,namespace,pvc_name,pvc_uid,pv_name,pv_uid,storage_class,reclaim_policy,requested_bytes,actual_size,storage_hash,assignment,lifecycle,revision,original_created_by,retained_by,retained_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),$12,$13,$14,$15,$16,$17::jsonb,$18,$19,NULLIF($20,''),NULLIF($21,''),$22)`,
		retained.ID, retained.OriginalResourceID, retained.ProjectID, retained.EnvironmentID, retained.ResourceType, retained.ResourceName,
		retained.Namespace, retained.PVCName, retained.PVCUID, retained.PVName, retained.PVUID, retained.StorageClass, retained.ReclaimPolicy,
		retained.RequestedBytes, retained.ActualSize, retained.StorageHash, string(assignment), retained.Lifecycle, retained.Revision,
		retained.OriginalCreatedBy, retained.RetainedBy, retained.RetainedAt); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM resources WHERE project_id=$1 AND id=$2 AND managed_lease_token=$3`, resource.ProjectID, resource.ID, token)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return invalid("MANAGED_RESOURCE_DELETE_FAILED", "managed resource delete lease is invalid")
	}
	return tx.Commit()
}

func (s PostgresStore) GetRetainedStorage(ctx context.Context, projectID, id string) (resourcev1.RetainedStorage, error) {
	return getRetainedStorage(ctx, s.DB, projectID, id)
}

func (s PostgresStore) GetRetainedStorageByResource(ctx context.Context, projectID, resourceID string) (resourcev1.RetainedStorage, error) {
	value, err := scanRetainedStorage(s.DB.QueryRowContext(ctx, retainedStorageColumns+` WHERE project_id=$1 AND original_resource_id=$2`, projectID, resourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.RetainedStorage{}, ErrNotFound
	}
	return value, err
}

func (s PostgresStore) ListRetainedStorage(ctx context.Context, projectID, environmentID string) ([]resourcev1.RetainedStorage, error) {
	query := retainedStorageColumns + ` WHERE project_id=$1`
	args := []any{projectID}
	if environmentID != "" {
		query += ` AND environment_id=$2`
		args = append(args, environmentID)
	}
	query += ` ORDER BY retained_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resourcev1.RetainedStorage{}
	for rows.Next() {
		value, err := scanRetainedStorage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s PostgresStore) SaveRetainedStorageReview(ctx context.Context, projectID, id string, revision uint64, token, actor string, now time.Time) (resourcev1.RetainedStorage, bool, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return resourcev1.RetainedStorage{}, false, false, err
	}
	defer tx.Rollback()
	current, err := getRetainedStorageForUpdate(ctx, tx, projectID, id)
	if err != nil {
		return resourcev1.RetainedStorage{}, false, false, err
	}
	if current.Revision != revision || current.Lifecycle != resourcev1.RetainedStorageRetained && current.Lifecycle != resourcev1.RetainedStorageDestroyFailed && current.Lifecycle != resourcev1.RetainedStorageUnknown {
		return resourcev1.RetainedStorage{}, false, false, Error{Code: resourcev1.FailureRetainedStorageStaleReview, Status: 409, Message: "retained storage authority changed during review"}
	}
	activeResource, activeBinding, err := retainedStorageReferences(ctx, tx, projectID, current.OriginalResourceID)
	if err != nil {
		return resourcev1.RetainedStorage{}, false, false, err
	}
	updated, err := scanRetainedStorage(tx.QueryRowContext(ctx, `UPDATE retained_storages SET revision=revision+1,review_token=$1,reviewed_by=NULLIF($2,''),reviewed_at=$3 WHERE project_id=$4 AND id=$5 AND revision=$6 RETURNING `+retainedStorageReturning, token, actor, now, projectID, id, revision))
	if err != nil {
		return resourcev1.RetainedStorage{}, false, false, err
	}
	return updated, activeResource, activeBinding, tx.Commit()
}

func (s PostgresStore) RequestRetainedStorageDestroy(ctx context.Context, projectID, id, key, payload, reviewToken, actor string, now time.Time) (resourcev1.RetainedStorage, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return resourcev1.RetainedStorage{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "retained-storage-destroy:v1:"+projectID+":"+key); err != nil {
		return resourcev1.RetainedStorage{}, false, err
	}
	var storedID, storedPayload string
	err = tx.QueryRowContext(ctx, `SELECT retained_storage_id,payload_hash FROM retained_storage_destroy_intents WHERE project_id=$1 AND idempotency_key=$2`, projectID, key).Scan(&storedID, &storedPayload)
	if err == nil {
		if storedID != id || storedPayload != payload {
			return resourcev1.RetainedStorage{}, false, Error{Code: "RETAINED_STORAGE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another destruction request"}
		}
		value, getErr := getRetainedStorage(ctx, tx, projectID, id)
		return value, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resourcev1.RetainedStorage{}, false, err
	}
	current, err := getRetainedStorageForUpdate(ctx, tx, projectID, id)
	if err != nil {
		return resourcev1.RetainedStorage{}, false, err
	}
	var storedReview string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(review_token,'') FROM retained_storages WHERE project_id=$1 AND id=$2`, projectID, id).Scan(&storedReview); err != nil {
		return resourcev1.RetainedStorage{}, false, err
	}
	if storedReview == "" || storedReview != reviewToken {
		return resourcev1.RetainedStorage{}, false, Error{Code: resourcev1.FailureRetainedStorageStaleReview, Status: 409, Message: "retained storage destruction review is stale"}
	}
	activeResource, activeBinding, err := retainedStorageReferences(ctx, tx, projectID, current.OriginalResourceID)
	if err != nil {
		return resourcev1.RetainedStorage{}, false, err
	}
	if activeResource || activeBinding {
		return resourcev1.RetainedStorage{}, false, Error{Code: resourcev1.FailureRetainedStorageActiveReference, Status: 409, Message: "retained storage has an active Resource or Binding reference"}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO retained_storage_destroy_intents(project_id,idempotency_key,payload_hash,retained_storage_id,created_at) VALUES($1,$2,$3,$4,$5)`, projectID, key, payload, id, now); err != nil {
		return resourcev1.RetainedStorage{}, false, err
	}
	reused := current.Lifecycle == resourcev1.RetainedStorageDestroying || current.Lifecycle == resourcev1.RetainedStorageDestroyed
	if current.Lifecycle == resourcev1.RetainedStorageRetained || current.Lifecycle == resourcev1.RetainedStorageDestroyFailed || current.Lifecycle == resourcev1.RetainedStorageUnknown {
		current, err = scanRetainedStorage(tx.QueryRowContext(ctx, `UPDATE retained_storages SET lifecycle='destroying',revision=revision+1,review_token=NULL,reviewed_by=NULL,reviewed_at=NULL,destroy_requested_by=NULLIF($1,''),destroy_requested_at=$2,failure_code=NULL,failure_message_redacted=NULL WHERE project_id=$3 AND id=$4 RETURNING `+retainedStorageReturning, actor, now, projectID, id))
		if err != nil {
			return resourcev1.RetainedStorage{}, false, err
		}
	}
	return current, reused, tx.Commit()
}

func (s PostgresStore) ClaimRetainedStorageDestroy(ctx context.Context, projectID, nodeID, token string, now, expires time.Time) (resourcev1.RetainedStorage, bool, error) {
	row := s.DB.QueryRowContext(ctx, `WITH candidate AS (
		SELECT id FROM retained_storages WHERE project_id=$1 AND lifecycle='destroying' AND assignment#>>'{node_id}'=$2
		AND (lease_token IS NULL OR lease_expires_at<=$3) ORDER BY retained_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE retained_storages r SET lease_token=$4,lease_expires_at=$5 FROM candidate c WHERE r.id=c.id RETURNING `+qualifiedRetainedStorageReturning, projectID, nodeID, now, token, expires)
	value, err := scanRetainedStorage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.RetainedStorage{}, false, nil
	}
	return value, err == nil, err
}

func (s PostgresStore) UpdateRetainedStorageClaimed(ctx context.Context, value resourcev1.RetainedStorage, token string) (resourcev1.RetainedStorage, error) {
	row := s.DB.QueryRowContext(ctx, `UPDATE retained_storages SET lifecycle=$1,revision=$2,destroyed_at=$3,failure_code=NULLIF($4,''),failure_message_redacted=NULLIF($5,''),lease_token=NULL,lease_expires_at=NULL WHERE project_id=$6 AND id=$7 AND lease_token=$8 RETURNING `+retainedStorageReturning,
		value.Lifecycle, value.Revision, value.DestroyedAt, value.FailureCode, value.FailureMessage, value.ProjectID, value.ID, token)
	updated, err := scanRetainedStorage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.RetainedStorage{}, invalid(resourcev1.FailureStorageDestroyFailed, "retained storage destruction lease is invalid")
	}
	return updated, err
}

func retainedStorageReferences(ctx context.Context, db queryer, projectID, resourceID string) (bool, bool, error) {
	var activeResource, activeBinding bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resources WHERE project_id=$1 AND id=$2),EXISTS(SELECT 1 FROM resource_bindings WHERE project_id=$1 AND target_id=$2)`, projectID, resourceID).Scan(&activeResource, &activeBinding); err != nil {
		return false, false, err
	}
	return activeResource, activeBinding, nil
}

const retainedStorageReturning = `id,original_resource_id,project_id,environment_id,resource_type,resource_name,namespace,pvc_name,pvc_uid,pv_name,COALESCE(pv_uid,''),storage_class,reclaim_policy,requested_bytes,actual_size,storage_hash,assignment::text,lifecycle,revision,COALESCE(original_created_by,''),COALESCE(retained_by,''),retained_at,COALESCE(destroy_requested_by,''),destroy_requested_at,destroyed_at,COALESCE(failure_code,''),COALESCE(failure_message_redacted,''),COALESCE(lease_token,''),lease_expires_at`
const qualifiedRetainedStorageReturning = `r.id,r.original_resource_id,r.project_id,r.environment_id,r.resource_type,r.resource_name,r.namespace,r.pvc_name,r.pvc_uid,r.pv_name,COALESCE(r.pv_uid,''),r.storage_class,r.reclaim_policy,r.requested_bytes,r.actual_size,r.storage_hash,r.assignment::text,r.lifecycle,r.revision,COALESCE(r.original_created_by,''),COALESCE(r.retained_by,''),r.retained_at,COALESCE(r.destroy_requested_by,''),r.destroy_requested_at,r.destroyed_at,COALESCE(r.failure_code,''),COALESCE(r.failure_message_redacted,''),COALESCE(r.lease_token,''),r.lease_expires_at`
const retainedStorageColumns = `SELECT ` + retainedStorageReturning + ` FROM retained_storages`

func getRetainedStorage(ctx context.Context, db queryer, projectID, id string) (resourcev1.RetainedStorage, error) {
	value, err := scanRetainedStorage(db.QueryRowContext(ctx, retainedStorageColumns+` WHERE project_id=$1 AND id=$2`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.RetainedStorage{}, ErrNotFound
	}
	return value, err
}

func getRetainedStorageForUpdate(ctx context.Context, db queryer, projectID, id string) (resourcev1.RetainedStorage, error) {
	value, err := scanRetainedStorage(db.QueryRowContext(ctx, retainedStorageColumns+` WHERE project_id=$1 AND id=$2 FOR UPDATE`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.RetainedStorage{}, ErrNotFound
	}
	return value, err
}

func scanRetainedStorage(row scanner) (resourcev1.RetainedStorage, error) {
	var value resourcev1.RetainedStorage
	var assignment string
	var destroyRequestedAt, destroyedAt, leaseExpiresAt sql.NullTime
	err := row.Scan(&value.ID, &value.OriginalResourceID, &value.ProjectID, &value.EnvironmentID, &value.ResourceType, &value.ResourceName,
		&value.Namespace, &value.PVCName, &value.PVCUID, &value.PVName, &value.PVUID, &value.StorageClass, &value.ReclaimPolicy,
		&value.RequestedBytes, &value.ActualSize, &value.StorageHash, &assignment, &value.Lifecycle, &value.Revision,
		&value.OriginalCreatedBy, &value.RetainedBy, &value.RetainedAt, &value.DestroyRequestedBy, &destroyRequestedAt, &destroyedAt,
		&value.FailureCode, &value.FailureMessage, &value.LeaseToken, &leaseExpiresAt)
	if err != nil {
		return value, err
	}
	value.SchemaVersion = resourcev1.RetainedStorageSchemaVersion
	if err := json.Unmarshal([]byte(assignment), &value.Assignment); err != nil {
		return resourcev1.RetainedStorage{}, err
	}
	if destroyRequestedAt.Valid {
		value.DestroyRequestedAt = &destroyRequestedAt.Time
	}
	if destroyedAt.Valid {
		value.DestroyedAt = &destroyedAt.Time
	}
	if leaseExpiresAt.Valid {
		value.LeaseExpiresAt = leaseExpiresAt.Time
	}
	return value, nil
}
