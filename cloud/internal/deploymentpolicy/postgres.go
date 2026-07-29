package deploymentpolicy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Get(ctx context.Context, projectID, policyID string) (deploymentpolicyv1.Policy, error) {
	if s.DB == nil {
		return deploymentpolicyv1.Policy{}, unavailable()
	}
	return scanPolicy(s.DB.QueryRowContext(ctx, policySelect+` WHERE h.project_id=$1 AND h.policy_id=$2`, projectID, policyID))
}

func (s PostgresStore) List(ctx context.Context, projectID string) ([]deploymentpolicyv1.Policy, error) {
	if s.DB == nil {
		return nil, unavailable()
	}
	rows, err := s.DB.QueryContext(ctx, policySelect+` WHERE h.project_id=$1 ORDER BY h.policy_id`, projectID)
	if err != nil {
		return nil, unavailable()
	}
	defer rows.Close()
	result := []deploymentpolicyv1.Policy{}
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	if rows.Err() != nil {
		return nil, unavailable()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s PostgresStore) ReplayPolicy(ctx context.Context, projectID, operation, key, payloadHash string) (deploymentpolicyv1.Policy, bool, error) {
	if s.DB == nil {
		return deploymentpolicyv1.Policy{}, false, unavailable()
	}
	return replayPolicyRow(ctx, s.DB, projectID, operation, key, payloadHash)
}

func (s PostgresStore) Apply(ctx context.Context, _ string, key, payloadHash string, request deploymentpolicyv1.ApplyRequest, policy deploymentpolicyv1.Policy) (deploymentpolicyv1.Policy, bool, error) {
	if s.DB == nil {
		return deploymentpolicyv1.Policy{}, false, unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return policy, false, unavailable()
	}
	defer tx.Rollback()
	if err = lockProject(ctx, tx, policy.Draft.ProjectID); err != nil {
		return policy, false, err
	}
	if replay, found, err := replayPolicy(ctx, tx, policy.Draft.ProjectID, "policy_apply", key, payloadHash); err != nil || found {
		return replay, found, err
	}
	current := deploymentpolicyv1.Policy{}
	if request.PolicyID != "" {
		current, err = scanPolicy(tx.QueryRowContext(ctx, policySelect+` WHERE h.project_id=$1 AND h.policy_id=$2`, policy.Draft.ProjectID, request.PolicyID))
		if err != nil {
			return policy, false, err
		}
	}
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return policy, false, Error{Code: "DEPLOYMENT_POLICY_STATE_CONFLICT", Status: 409, Message: "policy state changed; refresh diff and retry"}
	}
	if current.ID == "" {
		policy.ID = newID("pol")
	} else {
		policy.ID = current.ID
		policy.CreatedBy = current.CreatedBy
		policy.CreatedAt = current.CreatedAt
	}
	policy.Revision = current.Revision + 1
	policy.StateHash = stateHash(policy.ID, policy.Revision, policy.PolicyHash)
	if err = insertRevision(ctx, tx, policy); err != nil {
		return policy, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO deployment_policy_heads(policy_id,project_id,current_revision,state_hash,enabled,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(policy_id) DO UPDATE SET current_revision=EXCLUDED.current_revision,state_hash=EXCLUDED.state_hash,enabled=EXCLUDED.enabled,updated_at=EXCLUDED.updated_at`, policy.ID, policy.Draft.ProjectID, policy.Revision, policy.StateHash, policy.Draft.Enabled, policy.AppliedAt); err != nil {
		return policy, false, unavailable()
	}
	if err = insertPolicyReplay(ctx, tx, policy.Draft.ProjectID, "policy_apply", key, payloadHash, policy, policy.AppliedAt); err != nil {
		return policy, false, err
	}
	if err = tx.Commit(); err != nil {
		return policy, false, unavailable()
	}
	return policy, false, nil
}

func (s PostgresStore) Disable(ctx context.Context, projectID, policyID, actor, key, payloadHash string, request deploymentpolicyv1.DisableRequest, now time.Time) (deploymentpolicyv1.Policy, bool, error) {
	if s.DB == nil {
		return deploymentpolicyv1.Policy{}, false, unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return deploymentpolicyv1.Policy{}, false, unavailable()
	}
	defer tx.Rollback()
	if err = lockProject(ctx, tx, projectID); err != nil {
		return deploymentpolicyv1.Policy{}, false, err
	}
	if replay, found, err := replayPolicy(ctx, tx, projectID, "policy_disable", key, payloadHash); err != nil || found {
		return replay, found, err
	}
	current, err := scanPolicy(tx.QueryRowContext(ctx, policySelect+` WHERE h.project_id=$1 AND h.policy_id=$2`, projectID, policyID))
	if err != nil {
		return current, false, err
	}
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return current, false, Error{Code: "DEPLOYMENT_POLICY_STATE_CONFLICT", Status: 409, Message: "policy state changed; refresh and retry"}
	}
	if !current.Draft.Enabled {
		return current, false, Error{Code: "DEPLOYMENT_POLICY_ALREADY_DISABLED", Status: 409, Message: "deployment policy is already disabled"}
	}
	current.Revision++
	current.Draft.Enabled = false
	current.AppliedBy = actor
	current.AppliedAt = now
	current.PolicyHash, _ = hashJSON(current.Draft)
	current.StateHash = stateHash(current.ID, current.Revision, current.PolicyHash)
	if err = insertRevision(ctx, tx, current); err != nil {
		return current, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE deployment_policy_heads SET current_revision=$3,state_hash=$4,enabled=false,updated_at=$5 WHERE project_id=$1 AND policy_id=$2`, projectID, policyID, current.Revision, current.StateHash, now); err != nil {
		return current, false, unavailable()
	}
	if err = insertPolicyReplay(ctx, tx, projectID, "policy_disable", key, payloadHash, current, now); err != nil {
		return current, false, err
	}
	if err = tx.Commit(); err != nil {
		return current, false, unavailable()
	}
	return current, false, nil
}

const policySelect = `SELECT r.id,r.revision,r.schema_version,r.policy_hash,r.state_hash,r.policy_json,r.created_by,r.applied_by,r.created_at,r.applied_at FROM deployment_policy_heads h JOIN deployment_policy_revisions r ON r.id=h.policy_id AND r.revision=h.current_revision`

type scanner interface{ Scan(...any) error }

func scanPolicy(row scanner) (deploymentpolicyv1.Policy, error) {
	var policy deploymentpolicyv1.Policy
	var raw []byte
	if err := row.Scan(&policy.ID, &policy.Revision, &policy.SchemaVersion, &policy.PolicyHash, &policy.StateHash, &raw, &policy.CreatedBy, &policy.AppliedBy, &policy.CreatedAt, &policy.AppliedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return policy, ErrNotFound
		}
		return policy, unavailable()
	}
	if json.Unmarshal(raw, &policy.Draft) != nil {
		return policy, unavailable()
	}
	return policy, nil
}
func insertRevision(ctx context.Context, tx *sql.Tx, policy deploymentpolicyv1.Policy) error {
	raw, _ := json.Marshal(policy.Draft)
	_, err := tx.ExecContext(ctx, `INSERT INTO deployment_policy_revisions(id,revision,project_id,schema_version,policy_hash,state_hash,policy_json,enabled,created_by,applied_by,created_at,applied_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, policy.ID, policy.Revision, policy.Draft.ProjectID, policy.SchemaVersion, policy.PolicyHash, policy.StateHash, raw, policy.Draft.Enabled, policy.CreatedBy, policy.AppliedBy, policy.CreatedAt, policy.AppliedAt)
	if err != nil {
		return unavailable()
	}
	return nil
}
func lockProject(ctx context.Context, tx *sql.Tx, projectID string) error {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, projectID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Error{Code: "DEPLOYMENT_POLICY_PROJECT_NOT_FOUND", Status: 404, Message: "project not found"}
		}
		return unavailable()
	}
	return nil
}
func insertPolicyReplay(ctx context.Context, tx *sql.Tx, projectID, operation, key, payloadHash string, policy deploymentpolicyv1.Policy, createdAt time.Time) error {
	raw, _ := json.Marshal(policy)
	_, err := tx.ExecContext(ctx, `INSERT INTO control_mutation_idempotency(project_id,operation,idempotency_key,payload_hash,response_json,created_at) VALUES($1,$2,$3,$4,$5,$6)`, projectID, operation, key, payloadHash, raw, createdAt)
	if err != nil {
		return unavailable()
	}
	return nil
}
func replayPolicy(ctx context.Context, tx *sql.Tx, projectID, operation, key, payloadHash string) (deploymentpolicyv1.Policy, bool, error) {
	return replayPolicyRow(ctx, tx, projectID, operation, key, payloadHash)
}
func replayPolicyRow(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, operation, key, payloadHash string) (deploymentpolicyv1.Policy, bool, error) {
	var stored string
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT payload_hash,response_json FROM control_mutation_idempotency WHERE project_id=$1 AND operation=$2 AND idempotency_key=$3`, projectID, operation, key).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return deploymentpolicyv1.Policy{}, false, nil
	}
	if err != nil {
		return deploymentpolicyv1.Policy{}, false, unavailable()
	}
	if stored != payloadHash {
		return deploymentpolicyv1.Policy{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
	}
	var policy deploymentpolicyv1.Policy
	if json.Unmarshal(raw, &policy) != nil {
		return policy, false, unavailable()
	}
	return policy, true, nil
}
