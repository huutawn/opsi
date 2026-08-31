package publichostname

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type PostgresStore struct{ DB *sql.DB }

const columns = `id,hostname,owner_user_id,project_id,environment_id,runtime_id,COALESCE(target_ip::text,''),COALESCE(cloudflare_record_id,''),status,COALESCE(publication_error_code,''),COALESCE(publication_error_message,''),created_at,updated_at,released_at`

func (s PostgresStore) Reserve(ctx context.Context, req ReserveRequest, limit int, now time.Time) (Allocation, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Allocation{}, false, err
	}
	defer tx.Rollback()
	var locked string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, req.OwnerUserID).Scan(&locked); err != nil {
		return Allocation{}, false, err
	}
	current, err := scanOne(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM public_hostname_allocations WHERE hostname=$1 FOR UPDATE`, req.Hostname))
	if err == nil && current.Status != StatusReleased {
		if current.ProjectID == req.ProjectID && current.EnvironmentID == req.EnvironmentID {
			if current.RuntimeID == "" && req.RuntimeID != "" {
				row := tx.QueryRowContext(ctx, `UPDATE public_hostname_allocations SET runtime_id=$2,updated_at=$3 WHERE id=$1 RETURNING `+columns, current.ID, req.RuntimeID, now)
				value, updateErr := scanOne(row)
				if updateErr != nil {
					return Allocation{}, false, updateErr
				}
				return value, true, tx.Commit()
			}
			return current, true, tx.Commit()
		}
		return Allocation{}, false, Error{Code: "PUBLIC_HOSTNAME_UNAVAILABLE", Message: "This public subdomain has already been issued by Opsi.", NextAction: "Choose a different public subdomain."}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, false, err
	}
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM public_hostname_allocations WHERE owner_user_id=$1 AND status<>'released'`, req.OwnerUserID).Scan(&used); err != nil {
		return Allocation{}, false, err
	}
	if used >= limit {
		return Allocation{}, false, quotaError(limit)
	}
	if current.ID != "" {
		row := tx.QueryRowContext(ctx, `UPDATE public_hostname_allocations SET owner_user_id=$2,project_id=$3,environment_id=$4,runtime_id=NULLIF($5,''),target_ip=NULL,cloudflare_record_id=NULL,status='reserved',publication_error_code=NULL,publication_error_message=NULL,released_at=NULL,updated_at=$6 WHERE id=$1 RETURNING `+columns, current.ID, req.OwnerUserID, req.ProjectID, req.EnvironmentID, req.RuntimeID, now)
		value, updateErr := scanOne(row)
		if updateErr != nil {
			return Allocation{}, false, updateErr
		}
		return value, false, tx.Commit()
	}
	id, err := newID()
	if err != nil {
		return Allocation{}, false, err
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO public_hostname_allocations(id,hostname,owner_user_id,project_id,environment_id,runtime_id,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),'reserved',$7,$7) RETURNING `+columns, id, req.Hostname, req.OwnerUserID, req.ProjectID, req.EnvironmentID, req.RuntimeID, now)
	value, err := scanOne(row)
	if err != nil {
		// A different user can race for the same globally unique hostname. The
		// constraint remains authoritative and the caller receives a safe conflict.
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(err.Error(), "23505") {
			return Allocation{}, false, Error{Code: "PUBLIC_HOSTNAME_UNAVAILABLE", Message: "This public subdomain has already been issued by Opsi.", NextAction: "Choose a different public subdomain."}
		}
		return Allocation{}, false, err
	}
	return value, false, tx.Commit()
}

type rowScanner interface{ Scan(...any) error }

func scanOne(row rowScanner) (Allocation, error) {
	var value Allocation
	var runtime sql.NullString
	err := row.Scan(&value.ID, &value.Hostname, &value.OwnerUserID, &value.ProjectID, &value.EnvironmentID, &runtime, &value.TargetIP, &value.CloudflareRecordID, &value.Status, &value.PublicationErrorCode, &value.PublicationError, &value.CreatedAt, &value.UpdatedAt, &value.ReleasedAt)
	value.RuntimeID = runtime.String
	return value, err
}

func (s PostgresStore) Get(ctx context.Context, id string) (Allocation, error) {
	value, err := scanOne(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM public_hostname_allocations WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNotFound
	}
	return value, err
}

func (s PostgresStore) GetByHostname(ctx context.Context, hostname string) (Allocation, error) {
	value, err := scanOne(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM public_hostname_allocations WHERE hostname=$1`, hostname))
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNotFound
	}
	return value, err
}

func (s PostgresStore) ListForUser(ctx context.Context, userID string) ([]Allocation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+columns+` FROM public_hostname_allocations WHERE owner_user_id=$1 ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMany(rows)
}

func (s PostgresStore) ListForProject(ctx context.Context, projectID string) ([]Allocation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+columns+` FROM public_hostname_allocations WHERE project_id=$1 ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMany(rows)
}

func (s PostgresStore) ListPending(ctx context.Context, limit int) ([]Allocation, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+columns+` FROM public_hostname_allocations WHERE status IN ('provisioning','failed','release_pending') ORDER BY updated_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMany(rows)
}

func scanMany(rows *sql.Rows) ([]Allocation, error) {
	values := []Allocation{}
	for rows.Next() {
		value, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s PostgresStore) UpdatePublication(ctx context.Context, id, targetIP, recordID string, status Status, code, message string, now time.Time) (Allocation, error) {
	value, err := scanOne(s.DB.QueryRowContext(ctx, `UPDATE public_hostname_allocations SET target_ip=NULLIF($2,'')::inet,cloudflare_record_id=COALESCE(NULLIF($3,''),cloudflare_record_id),status=$4,publication_error_code=NULLIF($5,''),publication_error_message=NULLIF($6,''),updated_at=$7 WHERE id=$1 AND status<>'released' RETURNING `+columns, id, targetIP, recordID, status, code, message, now))
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNotFound
	}
	return value, err
}

func (s PostgresStore) MarkReleasePending(ctx context.Context, id string, now time.Time) (Allocation, error) {
	value, err := scanOne(s.DB.QueryRowContext(ctx, `UPDATE public_hostname_allocations SET status=CASE WHEN status='released' THEN status ELSE 'release_pending' END,updated_at=CASE WHEN status='released' THEN updated_at ELSE $2 END WHERE id=$1 RETURNING `+columns, id, now))
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNotFound
	}
	return value, err
}

func (s PostgresStore) MarkReleased(ctx context.Context, id string, now time.Time) (Allocation, error) {
	value, err := scanOne(s.DB.QueryRowContext(ctx, `UPDATE public_hostname_allocations SET status='released',target_ip=NULL,cloudflare_record_id=NULL,publication_error_code=NULL,publication_error_message=NULL,released_at=COALESCE(released_at,$2),updated_at=CASE WHEN status='released' THEN updated_at ELSE $2 END WHERE id=$1 RETURNING `+columns, id, now))
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNotFound
	}
	return value, err
}

// Backfill imports only exact hostnames beneath the configured managed domain.
// Historical nip.io exposure records therefore never become allocation authority.
func (s PostgresStore) Backfill(ctx context.Context, domain string) error {
	if strings.TrimSpace(domain) == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO public_hostname_allocations(id,hostname,owner_user_id,project_id,environment_id,runtime_id,target_ip,status,created_at,updated_at)
		SELECT 'phn-backfill-'||md5(d.hostname),d.hostname,d.requested_by,d.project_id,d.environment_id,d.runtime_id,
		       CASE WHEN n.public_host ~ '^[0-9]+(\.[0-9]+){3}$' THEN n.public_host::inet ELSE NULL END,
		       'reserved',d.created_at,d.updated_at
		FROM (
			SELECT DISTINCT ON (exposure_spec_json->>'hostname') exposure_spec_json->>'hostname' AS hostname,requested_by,project_id,environment_id,runtime_id,node_id,created_at,updated_at
			FROM deployment_jobs
			WHERE requested_by IS NOT NULL AND COALESCE(exposure_spec_json->>'hostname','') LIKE '%.'||$1
			ORDER BY exposure_spec_json->>'hostname',updated_at DESC,id DESC
		) d LEFT JOIN nodes n ON n.id=d.node_id
		ON CONFLICT(hostname) DO NOTHING`, domain)
	return err
}
