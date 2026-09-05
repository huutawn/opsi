package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const sshHostKeyTrustSelectSQL = `SELECT id, project_id, host, port, algorithm, public_key, fingerprint, status, COALESCE(created_by,''), created_at, superseded_at, COALESCE(superseded_by,'') FROM ssh_host_key_trusts`

const sshHostKeyObservationSelectSQL = `SELECT id, project_id, host, port, resolved_ip, algorithm, public_key, fingerprint, trust_state, previous_fingerprint, status, COALESCE(created_by,''), expires_at, created_at, updated_at FROM ssh_host_key_observations`

func scanSSHHostKeyTrust(row rowScanner) (SSHHostKeyTrust, error) {
	var t SSHHostKeyTrust
	var supersededAt sql.NullTime
	err := row.Scan(&t.ID, &t.ProjectID, &t.Host, &t.Port, &t.Algorithm, &t.PublicKey, &t.Fingerprint, &t.Status, &t.CreatedBy, &t.CreatedAt, &supersededAt, &t.SupersededBy)
	t.SupersededAt = nullTimePtr(supersededAt)
	return t, err
}

func scanSSHHostKeyObservation(row rowScanner) (SSHHostKeyObservation, error) {
	var obs SSHHostKeyObservation
	err := row.Scan(&obs.ID, &obs.ProjectID, &obs.Host, &obs.Port, &obs.ResolvedIP, &obs.Algorithm, &obs.PublicKey, &obs.Fingerprint, &obs.TrustState, &obs.PreviousFingerprint, &obs.Status, &obs.CreatedBy, &obs.ExpiresAt, &obs.CreatedAt, &obs.UpdatedAt)
	return obs, err
}

func (s PostgresService) CreateSSHHostKeyObservation(projectID, host string, port int, resolvedIP, algorithm, publicKey, fingerprint, createdBy string, now time.Time) (SSHHostKeyObservation, error) {
	ctx := context.Background()
	var orgID string
	if err := s.DB.QueryRowContext(ctx, `SELECT org_id FROM projects WHERE id = $1`, projectID).Scan(&orgID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SSHHostKeyObservation{}, ErrNotFound
		}
		return SSHHostKeyObservation{}, err
	}

	normHost := normalizeHost(host)
	now = now.UTC()
	expiresAt := now.Add(10 * time.Minute)

	// Check active trust for endpoint
	trustState := TrustStateFirstSeen
	var previousFingerprint string

	var activeTrust SSHHostKeyTrust
	err := s.DB.QueryRowContext(ctx, sshHostKeyTrustSelectSQL+` WHERE project_id = $1 AND host = $2 AND port = $3 AND status = 'active'`, projectID, normHost, port).Scan(
		&activeTrust.ID, &activeTrust.ProjectID, &activeTrust.Host, &activeTrust.Port, &activeTrust.Algorithm,
		&activeTrust.PublicKey, &activeTrust.Fingerprint, &activeTrust.Status, &activeTrust.CreatedBy,
		&activeTrust.CreatedAt, &activeTrust.SupersededAt, &activeTrust.SupersededBy,
	)
	if err == nil {
		if comparePublicKeys(activeTrust.PublicKey, publicKey) {
			trustState = TrustStateMatched
		} else {
			trustState = TrustStateChanged
			previousFingerprint = activeTrust.Fingerprint
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SSHHostKeyObservation{}, err
	}

	obs := SSHHostKeyObservation{
		ID:                  newID("probe"),
		ProjectID:           projectID,
		Host:                normHost,
		Port:                port,
		ResolvedIP:          resolvedIP,
		Algorithm:           algorithm,
		PublicKey:           publicKey,
		Fingerprint:         fingerprint,
		TrustState:          trustState,
		PreviousFingerprint: previousFingerprint,
		Status:              ObservationStatusPending,
		CreatedBy:           createdBy,
		ExpiresAt:           expiresAt,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SSHHostKeyObservation{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO ssh_host_key_observations(id, project_id, host, port, resolved_ip, algorithm, public_key, fingerprint, trust_state, previous_fingerprint, status, created_by, expires_at, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),$13,$14,$15)`,
		obs.ID, obs.ProjectID, obs.Host, obs.Port, obs.ResolvedIP, obs.Algorithm, obs.PublicKey, obs.Fingerprint, obs.TrustState, obs.PreviousFingerprint, obs.Status, obs.CreatedBy, obs.ExpiresAt, obs.CreatedAt, obs.UpdatedAt,
	); err != nil {
		return SSHHostKeyObservation{}, err
	}

	meta, _ := json.Marshal(map[string]any{
		"host":        normHost,
		"port":        port,
		"trust_state": trustState,
		"fingerprint": fingerprint,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id, org_id, project_id, actor_type, actor_id, action, resource_type, resource_id, result, metadata_redacted, created_at) VALUES($1,$2,$3,'user',NULLIF($4,''),'SSH_HOST_KEY_OBSERVED','ssh_host_key_observation',$5,'success',$6,$7)`,
		newID("aud"), orgID, projectID, createdBy, obs.ID, meta, now,
	); err != nil {
		return SSHHostKeyObservation{}, err
	}

	return obs, tx.Commit()
}

func (s PostgresService) GetSSHHostKeyObservation(projectID, observationID string) (SSHHostKeyObservation, error) {
	ctx := context.Background()
	obs, err := scanSSHHostKeyObservation(s.DB.QueryRowContext(ctx, sshHostKeyObservationSelectSQL+` WHERE project_id = $1 AND id = $2`, projectID, observationID))
	if errors.Is(err, sql.ErrNoRows) {
		return SSHHostKeyObservation{}, ErrNotFound
	}
	return obs, err
}

func (s PostgresService) GetActiveSSHHostKeyTrust(projectID, host string, port int) (SSHHostKeyTrust, error) {
	ctx := context.Background()
	trust, err := scanSSHHostKeyTrust(s.DB.QueryRowContext(ctx, sshHostKeyTrustSelectSQL+` WHERE project_id = $1 AND host = $2 AND port = $3 AND status = 'active'`, projectID, normalizeHost(host), port))
	if errors.Is(err, sql.ErrNoRows) {
		return SSHHostKeyTrust{}, ErrNotFound
	}
	return trust, err
}

func (s PostgresService) GetSSHHostKeyTrust(projectID, trustID string) (SSHHostKeyTrust, error) {
	ctx := context.Background()
	trust, err := scanSSHHostKeyTrust(s.DB.QueryRowContext(ctx, sshHostKeyTrustSelectSQL+` WHERE project_id = $1 AND id = $2`, projectID, trustID))
	if errors.Is(err, sql.ErrNoRows) {
		return SSHHostKeyTrust{}, ErrNotFound
	}
	return trust, err
}

func (s PostgresService) ListSSHHostKeyTrusts(projectID string) ([]SSHHostKeyTrust, error) {
	ctx := context.Background()
	rows, err := s.DB.QueryContext(ctx, sshHostKeyTrustSelectSQL+` WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SSHHostKeyTrust
	for rows.Next() {
		t, err := scanSSHHostKeyTrust(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

func (s PostgresService) ConfirmSSHHostKeyRotation(projectID, observationID, actorID, expectedFingerprint, idempotencyKey string, now time.Time) (SSHHostKeyTrust, error) {
	ctx := context.Background()
	scope := "confirm-rotation:" + projectID + ":" + observationID
	if idempotencyKey != "" {
		if id, ok, err := s.idempotentResource(ctx, scope, idempotencyKey); err != nil || ok {
			if err != nil {
				return SSHHostKeyTrust{}, err
			}
			return s.GetSSHHostKeyTrust(projectID, id)
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SSHHostKeyTrust{}, err
	}
	defer tx.Rollback()

	var orgID string
	if err := tx.QueryRowContext(ctx, `SELECT org_id FROM projects WHERE id = $1`, projectID).Scan(&orgID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SSHHostKeyTrust{}, ErrNotFound
		}
		return SSHHostKeyTrust{}, err
	}

	obs, err := scanSSHHostKeyObservation(tx.QueryRowContext(ctx, sshHostKeyObservationSelectSQL+` WHERE project_id = $1 AND id = $2 FOR UPDATE`, projectID, observationID))
	if errors.Is(err, sql.ErrNoRows) {
		return SSHHostKeyTrust{}, ErrNotFound
	}
	if err != nil {
		return SSHHostKeyTrust{}, err
	}

	now = now.UTC()
	if !now.Before(obs.ExpiresAt) {
		return SSHHostKeyTrust{}, APIError{Status: 410, Code: "SSH_HOST_KEY_PROBE_EXPIRED", Message: "SSH host-key probe has expired"}
	}
	if obs.Status != ObservationStatusPending && obs.Status != ObservationStatusConfirmed {
		return SSHHostKeyTrust{}, APIError{Status: 409, Code: "SSH_HOST_KEY_PROBE_INVALID_STATUS", Message: "SSH host-key probe is not in a confirmable status"}
	}

	if expectedFingerprint != "" && expectedFingerprint != obs.Fingerprint {
		return SSHHostKeyTrust{}, APIError{Status: 400, Code: "FINGERPRINT_MISMATCH", Message: "provided fingerprint does not match the observed host key"}
	}

	// Supersede existing active trust
	if _, err := tx.ExecContext(ctx, `UPDATE ssh_host_key_trusts SET status = 'superseded', superseded_at = $1, superseded_by = NULLIF($2,'') WHERE project_id = $3 AND host = $4 AND port = $5 AND status = 'active'`,
		now, actorID, projectID, obs.Host, obs.Port,
	); err != nil {
		return SSHHostKeyTrust{}, err
	}

	newTrust := SSHHostKeyTrust{
		ID:          newID("trust"),
		ProjectID:   projectID,
		Host:        obs.Host,
		Port:        obs.Port,
		Algorithm:   obs.Algorithm,
		PublicKey:   obs.PublicKey,
		Fingerprint: obs.Fingerprint,
		Status:      TrustStatusActive,
		CreatedBy:   actorID,
		CreatedAt:   now,
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO ssh_host_key_trusts(id, project_id, host, port, algorithm, public_key, fingerprint, status, created_by, created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10)`,
		newTrust.ID, newTrust.ProjectID, newTrust.Host, newTrust.Port, newTrust.Algorithm, newTrust.PublicKey, newTrust.Fingerprint, newTrust.Status, newTrust.CreatedBy, newTrust.CreatedAt,
	); err != nil {
		return SSHHostKeyTrust{}, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE ssh_host_key_observations SET status = 'confirmed', updated_at = $1 WHERE id = $2`, now, observationID); err != nil {
		return SSHHostKeyTrust{}, err
	}

	meta, _ := json.Marshal(map[string]any{
		"host":        newTrust.Host,
		"port":        newTrust.Port,
		"fingerprint": newTrust.Fingerprint,
		"probe_id":    observationID,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id, org_id, project_id, actor_type, actor_id, action, resource_type, resource_id, result, metadata_redacted, created_at) VALUES($1,$2,$3,'user',NULLIF($4,''),'SSH_HOST_KEY_ROTATION_CONFIRMED','ssh_host_key_trust',$5,'success',$6,$7)`,
		newID("aud"), orgID, projectID, actorID, newTrust.ID, meta, now,
	); err != nil {
		return SSHHostKeyTrust{}, err
	}

	if idempotencyKey != "" {
		if err := insertIdempotency(ctx, tx, scope, idempotencyKey, "ssh_host_key_trust", newTrust.ID); err != nil {
			return SSHHostKeyTrust{}, err
		}
	}

	return newTrust, tx.Commit()
}

func (s PostgresService) ResumeBootstrapSession(projectID, sessionID, observationID, actorID, idempotencyKey string, now time.Time) (BootstrapSession, error) {
	ctx := context.Background()
	scope := "resume-bootstrap:" + projectID + ":" + sessionID
	if idempotencyKey != "" {
		if id, ok, err := s.idempotentResource(ctx, scope, idempotencyKey); err != nil || ok {
			if err != nil {
				return BootstrapSession{}, err
			}
			return s.GetBootstrapSession(projectID, id)
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSession{}, err
	}
	defer tx.Rollback()

	session, err := scanBootstrapSession(tx.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE project_id=$1 AND id=$2 FOR UPDATE`, projectID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapSession{}, ErrNotFound
	}
	if err != nil {
		return BootstrapSession{}, err
	}

	if session.Status != BootstrapWaitingHostKeyConfirmation {
		return BootstrapSession{}, APIError{Status: 409, Code: "BOOTSTRAP_NOT_WAITING_HOST_KEY_CONFIRMATION", Message: "bootstrap session is not waiting for host-key confirmation"}
	}

	obs, err := scanSSHHostKeyObservation(tx.QueryRowContext(ctx, sshHostKeyObservationSelectSQL+` WHERE project_id=$1 AND id=$2`, projectID, observationID))
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapSession{}, ErrNotFound
	}
	if err != nil {
		return BootstrapSession{}, err
	}

	now = now.UTC()
	if !now.Before(obs.ExpiresAt) {
		return BootstrapSession{}, APIError{Status: 410, Code: "SSH_HOST_KEY_PROBE_EXPIRED", Message: "SSH host-key probe has expired"}
	}
	if obs.Status != ObservationStatusConfirmed {
		return BootstrapSession{}, APIError{Status: 409, Code: "SSH_HOST_KEY_PROBE_NOT_CONFIRMED", Message: "SSH host-key probe must be confirmed before resuming"}
	}
	if normalizeHost(session.PublicHost) != obs.Host || session.SSHPort != obs.Port {
		return BootstrapSession{}, APIError{Status: 400, Code: "SSH_HOST_KEY_ENDPOINT_MISMATCH", Message: "probe endpoint does not match bootstrap session"}
	}

	activeTrust, err := scanSSHHostKeyTrust(tx.QueryRowContext(ctx, sshHostKeyTrustSelectSQL+` WHERE project_id=$1 AND host=$2 AND port=$3 AND status='active'`, projectID, obs.Host, obs.Port))
	if err != nil || activeTrust.Fingerprint != obs.Fingerprint {
		return BootstrapSession{}, APIError{Status: 409, Code: "SSH_HOST_KEY_ACTIVE_TRUST_MISMATCH", Message: "active trust does not match confirmed probe"}
	}

	session.SSHHostKeyTrustID = activeTrust.ID
	session.ResolvedIP = obs.ResolvedIP
	session.Status = BootstrapPending
	session.LastFailureCode = ""
	session.LastFailureRedacted = ""
	session.UpdatedAt = now
	clearBootstrapLease(&session)

	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status = 'pending', ssh_host_key_trust_id = $1, resolved_ip = $2, lease_owner = NULL, lease_token_hash = NULL, lease_expires_at = NULL, lease_heartbeat_at = NULL, leased_at = NULL, last_failure_code = '', last_failure_message_redacted = '', updated_at = $3 WHERE id = $4`,
		session.SSHHostKeyTrustID, session.ResolvedIP, now, session.ID,
	); err != nil {
		return BootstrapSession{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,$5,'info','resumed','bootstrap session resumed with confirmed SSH host key',0,$6)`,
		newID("evt"), session.OrgID, session.ProjectID, session.ID, session.NodeID, now,
	); err != nil {
		return BootstrapSession{}, err
	}

	meta, _ := json.Marshal(map[string]any{
		"host":     session.PublicHost,
		"port":     session.SSHPort,
		"trust_id": activeTrust.ID,
		"probe_id": observationID,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id, org_id, project_id, actor_type, actor_id, action, resource_type, resource_id, result, metadata_redacted, created_at) VALUES($1,$2,$3,'user',NULLIF($4,''),'BOOTSTRAP_SESSION_RESUMED','bootstrap_session',$5,'success',$6,$7)`,
		newID("aud"), session.OrgID, projectID, actorID, session.ID, meta, now,
	); err != nil {
		return BootstrapSession{}, err
	}

	if idempotencyKey != "" {
		if err := insertIdempotency(ctx, tx, scope, idempotencyKey, "bootstrap_session", session.ID); err != nil {
			return BootstrapSession{}, err
		}
	}

	return session, tx.Commit()
}
