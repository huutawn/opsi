package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Create(ctx context.Context, value resourcev1.Resource, key, payload string) (resourcev1.Resource, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return resourcev1.Resource{}, false, err
	}
	defer tx.Rollback()
	var resourceID, previousPayload string
	err = tx.QueryRowContext(ctx, `SELECT resource_id,payload_hash FROM resource_idempotency WHERE project_id=$1 AND operation='create_resource' AND idempotency_key=$2`, value.ProjectID, key).Scan(&resourceID, &previousPayload)
	if err == nil {
		if previousPayload != payload {
			return resourcev1.Resource{}, false, Error{Code: "RESOURCE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another payload"}
		}
		loaded, getErr := getResource(ctx, tx, value.ProjectID, resourceID)
		return loaded, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resourcev1.Resource{}, false, err
	}
	managed, _ := json.Marshal(value.Managed)
	external, _ := json.Marshal(value.External)
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_idempotency(project_id,operation,idempotency_key,payload_hash,resource_id,created_at) VALUES($1,'create_resource',$2,$3,$4,$5)`, value.ProjectID, key, payload, value.ID, value.CreatedAt); err != nil {
		_ = tx.Rollback()
		return s.replayResource(ctx, value.ProjectID, key, payload, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resources(id,project_id,environment_id,name,kind,provider,type,lifecycle,managed_spec,external_spec,internal_name,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,NULLIF($11,''),NULLIF($12,''),$13,$14)`, value.ID, value.ProjectID, value.EnvironmentID, value.Name, value.Kind, value.Provider, value.Type, value.Lifecycle, string(managed), string(external), value.InternalName, value.CreatedBy, value.CreatedAt, value.UpdatedAt); err != nil {
		return resourcev1.Resource{}, false, err
	}
	return value, false, tx.Commit()
}

func (s PostgresStore) Get(ctx context.Context, projectID, resourceID string) (resourcev1.Resource, error) {
	return getResource(ctx, s.DB, projectID, resourceID)
}

func (s PostgresStore) List(ctx context.Context, projectID, environmentID string) ([]resourcev1.Resource, error) {
	query := resourceColumns + ` WHERE project_id=$1`
	args := []any{projectID}
	if environmentID != "" {
		query += ` AND environment_id=$2`
		args = append(args, environmentID)
	}
	query += ` ORDER BY created_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resourcev1.Resource{}
	for rows.Next() {
		value, scanErr := scanResource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s PostgresStore) Update(ctx context.Context, value resourcev1.Resource) (resourcev1.Resource, error) {
	managed, _ := json.Marshal(value.Managed)
	external, _ := json.Marshal(value.External)
	runtime, _ := json.Marshal(value.Runtime)
	result, err := s.DB.ExecContext(ctx, `UPDATE resources SET lifecycle=$1,managed_spec=$2::jsonb,external_spec=$3::jsonb,runtime_state=$4::jsonb,updated_at=$5 WHERE project_id=$6 AND id=$7`, value.Lifecycle, string(managed), string(external), string(runtime), value.UpdatedAt, value.ProjectID, value.ID)
	if err != nil {
		return resourcev1.Resource{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return resourcev1.Resource{}, ErrNotFound
	}
	return value, nil
}

func (s PostgresStore) ClaimManaged(ctx context.Context, projectID, nodeID, token string, now, expires time.Time) (resourcev1.Resource, bool, error) {
	row := s.DB.QueryRowContext(ctx, `WITH candidate AS (
		SELECT id FROM resources
		WHERE project_id=$1 AND runtime_state<>'null'::jsonb AND runtime_state#>>'{spec,assignment,node_id}'=$2
		AND lifecycle IN ('planned','provisioning','deleting')
		AND (managed_lease_token IS NULL OR managed_lease_expires_at<=$3)
		ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE resources r SET managed_lease_token=$4,managed_lease_expires_at=$5,lifecycle=CASE WHEN r.lifecycle='deleting' THEN r.lifecycle ELSE 'provisioning' END,updated_at=$3
	FROM candidate c WHERE r.id=c.id RETURNING `+qualifiedResourceReturning, projectID, nodeID, now, token, expires)
	value, err := scanResource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.Resource{}, false, nil
	}
	return value, err == nil, err
}

func (s PostgresStore) UpdateClaimed(ctx context.Context, value resourcev1.Resource, token string) (resourcev1.Resource, error) {
	managed, _ := json.Marshal(value.Managed)
	external, _ := json.Marshal(value.External)
	runtime, _ := json.Marshal(value.Runtime)
	row := s.DB.QueryRowContext(ctx, `UPDATE resources SET lifecycle=$1,managed_spec=$2::jsonb,external_spec=$3::jsonb,runtime_state=$4::jsonb,managed_lease_token=NULL,managed_lease_expires_at=NULL,updated_at=$5 WHERE project_id=$6 AND id=$7 AND managed_lease_token=$8 RETURNING `+resourceReturning, value.Lifecycle, string(managed), string(external), string(runtime), value.UpdatedAt, value.ProjectID, value.ID, token)
	updated, err := scanResource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.Resource{}, invalid("MANAGED_RESOURCE_APPLY_FAILED", "managed resource lease is invalid")
	}
	return updated, err
}

func (s PostgresStore) Delete(ctx context.Context, projectID, resourceID string) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM resources WHERE project_id=$1 AND id=$2`, projectID, resourceID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrNotFound
	}
	return nil
}

func (s PostgresStore) DeleteClaimed(ctx context.Context, projectID, resourceID, token string) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM resources WHERE project_id=$1 AND id=$2 AND managed_lease_token=$3`, projectID, resourceID, token)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return invalid("MANAGED_RESOURCE_DELETE_FAILED", "managed resource delete lease is invalid")
	}
	return nil
}

func (s PostgresStore) CreateBinding(ctx context.Context, value resourcev1.Binding, key, payload string) (resourcev1.Binding, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return resourcev1.Binding{}, false, err
	}
	defer tx.Rollback()
	var bindingID, previousPayload string
	err = tx.QueryRowContext(ctx, `SELECT resource_id,payload_hash FROM resource_idempotency WHERE project_id=$1 AND operation='create_binding' AND idempotency_key=$2`, value.ProjectID, key).Scan(&bindingID, &previousPayload)
	if err == nil {
		if previousPayload != payload {
			return resourcev1.Binding{}, false, Error{Code: "RESOURCE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another payload"}
		}
		loaded, getErr := getBinding(ctx, tx, value.ProjectID, bindingID)
		return loaded, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return resourcev1.Binding{}, false, err
	}
	references, _ := json.Marshal(value.RuntimeRefs)
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_idempotency(project_id,operation,idempotency_key,payload_hash,resource_id,created_at) VALUES($1,'create_binding',$2,$3,$4,$5)`, value.ProjectID, key, payload, value.ID, value.CreatedAt); err != nil {
		_ = tx.Rollback()
		return s.replayBinding(ctx, value.ProjectID, key, payload, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_bindings(id,project_id,environment_id,source_kind,source_id,target_kind,target_id,protocol,logical_name,runtime_references,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12)`, value.ID, value.ProjectID, value.EnvironmentID, value.Source.Kind, value.Source.ID, value.Target.Kind, value.Target.ID, value.Protocol, value.LogicalName, string(references), value.CreatedAt, value.UpdatedAt); err != nil {
		return resourcev1.Binding{}, false, err
	}
	return value, false, tx.Commit()
}

func (s PostgresStore) replayResource(ctx context.Context, projectID, key, payload string, original error) (resourcev1.Resource, bool, error) {
	var id, storedPayload string
	if err := s.DB.QueryRowContext(ctx, `SELECT resource_id,payload_hash FROM resource_idempotency WHERE project_id=$1 AND operation='create_resource' AND idempotency_key=$2`, projectID, key).Scan(&id, &storedPayload); err != nil {
		return resourcev1.Resource{}, false, original
	}
	if storedPayload != payload {
		return resourcev1.Resource{}, false, Error{Code: "RESOURCE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another payload"}
	}
	value, err := s.Get(ctx, projectID, id)
	return value, true, err
}

func (s PostgresStore) replayBinding(ctx context.Context, projectID, key, payload string, original error) (resourcev1.Binding, bool, error) {
	var id, storedPayload string
	if err := s.DB.QueryRowContext(ctx, `SELECT resource_id,payload_hash FROM resource_idempotency WHERE project_id=$1 AND operation='create_binding' AND idempotency_key=$2`, projectID, key).Scan(&id, &storedPayload); err != nil {
		return resourcev1.Binding{}, false, original
	}
	if storedPayload != payload {
		return resourcev1.Binding{}, false, Error{Code: "RESOURCE_IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was used with another payload"}
	}
	value, err := getBinding(ctx, s.DB, projectID, id)
	return value, true, err
}

func (s PostgresStore) ListBindings(ctx context.Context, projectID, environmentID string) ([]resourcev1.Binding, error) {
	query := bindingColumns + ` WHERE project_id=$1`
	args := []any{projectID}
	if environmentID != "" {
		query += ` AND environment_id=$2`
		args = append(args, environmentID)
	}
	query += ` ORDER BY created_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resourcev1.Binding{}
	for rows.Next() {
		value, scanErr := scanBinding(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

const resourceReturning = `id,project_id,environment_id,name,kind,provider,type,lifecycle,managed_spec::text,external_spec::text,runtime_state::text,COALESCE(managed_lease_token,''),managed_lease_expires_at,COALESCE(internal_name,''),COALESCE(created_by,''),created_at,updated_at`
const qualifiedResourceReturning = `r.id,r.project_id,r.environment_id,r.name,r.kind,r.provider,r.type,r.lifecycle,r.managed_spec::text,r.external_spec::text,r.runtime_state::text,COALESCE(r.managed_lease_token,''),r.managed_lease_expires_at,COALESCE(r.internal_name,''),COALESCE(r.created_by,''),r.created_at,r.updated_at`
const resourceColumns = `SELECT ` + resourceReturning + ` FROM resources`
const bindingColumns = `SELECT id,project_id,environment_id,source_kind,source_id,target_kind,target_id,protocol,logical_name,runtime_references::text,created_at,updated_at FROM resource_bindings`

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type scanner interface{ Scan(...any) error }

func getResource(ctx context.Context, db queryer, projectID, resourceID string) (resourcev1.Resource, error) {
	value, err := scanResource(db.QueryRowContext(ctx, resourceColumns+` WHERE project_id=$1 AND id=$2`, projectID, resourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.Resource{}, ErrNotFound
	}
	return value, err
}

func scanResource(row scanner) (resourcev1.Resource, error) {
	var value resourcev1.Resource
	var managed, external, runtime string
	var leaseToken string
	var leaseExpiresAt sql.NullTime
	err := row.Scan(&value.ID, &value.ProjectID, &value.EnvironmentID, &value.Name, &value.Kind, &value.Provider, &value.Type, &value.Lifecycle, &managed, &external, &runtime, &leaseToken, &leaseExpiresAt, &value.InternalName, &value.CreatedBy, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return value, err
	}
	value.SchemaVersion = resourcev1.SchemaVersion
	if managed != "null" {
		value.Managed = &resourcev1.ManagedSpec{}
		if err := json.Unmarshal([]byte(managed), value.Managed); err != nil {
			return resourcev1.Resource{}, err
		}
	}
	if external != "null" {
		value.External = &resourcev1.ExternalSpec{}
		if err := json.Unmarshal([]byte(external), value.External); err != nil {
			return resourcev1.Resource{}, err
		}
	}
	if runtime != "null" {
		value.Runtime = &resourcev1.ManagedResourceRuntime{}
		if err := json.Unmarshal([]byte(runtime), value.Runtime); err != nil {
			return resourcev1.Resource{}, err
		}
		value.Runtime.LeaseToken = leaseToken
		if leaseExpiresAt.Valid {
			value.Runtime.LeaseExpiresAt = leaseExpiresAt.Time
		}
	}
	return value, nil
}

func getBinding(ctx context.Context, db queryer, projectID, bindingID string) (resourcev1.Binding, error) {
	value, err := scanBinding(db.QueryRowContext(ctx, bindingColumns+` WHERE project_id=$1 AND id=$2`, projectID, bindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return resourcev1.Binding{}, ErrNotFound
	}
	return value, err
}

func scanBinding(row scanner) (resourcev1.Binding, error) {
	var value resourcev1.Binding
	var references string
	err := row.Scan(&value.ID, &value.ProjectID, &value.EnvironmentID, &value.Source.Kind, &value.Source.ID, &value.Target.Kind, &value.Target.ID, &value.Protocol, &value.LogicalName, &references, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return value, err
	}
	value.SchemaVersion = resourcev1.SchemaVersion
	err = json.Unmarshal([]byte(references), &value.RuntimeRefs)
	return value, err
}
