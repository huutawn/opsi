package topology

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Get(ctx context.Context, projectID string) (topologyv1.Plan, error) {
	if s.DB == nil {
		return topologyv1.Plan{}, unavailable()
	}
	return scanPlan(s.DB.QueryRowContext(ctx, `SELECT r.id,r.revision,r.project_id,r.schema_version,r.plan_hash,r.state_hash,r.assignments_json,r.created_by,r.applied_by,r.created_at,r.applied_at FROM topology_plan_heads h JOIN topology_plan_revisions r ON r.id=h.plan_id AND r.revision=h.current_revision WHERE h.project_id=$1`, projectID))
}

func (s PostgresStore) ReplayPlan(ctx context.Context, projectID, key, payloadHash string) (topologyv1.Plan, bool, error) {
	if s.DB == nil {
		return topologyv1.Plan{}, false, unavailable()
	}
	return replayPlanRow(ctx, s.DB, projectID, "topology_apply", key, payloadHash)
}

func (s PostgresStore) Apply(ctx context.Context, _ string, key, payloadHash string, request topologyv1.ApplyRequest, plan topologyv1.Plan) (topologyv1.Plan, bool, error) {
	if s.DB == nil {
		return topologyv1.Plan{}, false, unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return topologyv1.Plan{}, false, unavailable()
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, plan.ProjectID); err != nil {
		return topologyv1.Plan{}, false, projectError(err)
	}
	if replay, found, err := replayPlan(ctx, tx, plan.ProjectID, "topology_apply", key, payloadHash); err != nil || found {
		return replay, found, err
	}
	current, err := scanPlan(tx.QueryRowContext(ctx, `SELECT r.id,r.revision,r.project_id,r.schema_version,r.plan_hash,r.state_hash,r.assignments_json,r.created_by,r.applied_by,r.created_at,r.applied_at FROM topology_plan_heads h JOIN topology_plan_revisions r ON r.id=h.plan_id AND r.revision=h.current_revision WHERE h.project_id=$1`, plan.ProjectID))
	if errors.Is(err, ErrNotFound) {
		current = topologyv1.Plan{ProjectID: plan.ProjectID}
	} else if err != nil {
		return topologyv1.Plan{}, false, err
	}
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return topologyv1.Plan{}, false, Error{Code: "TOPOLOGY_STATE_CONFLICT", Status: 409, Message: "topology state changed; refresh diff and retry"}
	}
	if current.ID == "" {
		plan.ID = newID("topo")
	} else {
		plan.ID = current.ID
		plan.CreatedAt = current.CreatedAt
		plan.CreatedBy = current.CreatedBy
	}
	plan.Revision = current.Revision + 1
	plan.StateHash = stateHash(plan.ID, plan.Revision, plan.PlanHash)
	assignments, _ := json.Marshal(plan.Assignments)
	if _, err = tx.ExecContext(ctx, `INSERT INTO topology_plan_revisions(id,revision,project_id,schema_version,plan_hash,state_hash,assignments_json,created_by,applied_by,created_at,applied_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, plan.ID, plan.Revision, plan.ProjectID, plan.SchemaVersion, plan.PlanHash, plan.StateHash, assignments, plan.CreatedBy, plan.AppliedBy, plan.CreatedAt, plan.AppliedAt); err != nil {
		return topologyv1.Plan{}, false, unavailable()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO topology_plan_heads(project_id,plan_id,current_revision,state_hash,updated_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(project_id) DO UPDATE SET plan_id=EXCLUDED.plan_id,current_revision=EXCLUDED.current_revision,state_hash=EXCLUDED.state_hash,updated_at=EXCLUDED.updated_at`, plan.ProjectID, plan.ID, plan.Revision, plan.StateHash, plan.AppliedAt); err != nil {
		return topologyv1.Plan{}, false, unavailable()
	}
	if err = insertReplay(ctx, tx, plan.ProjectID, "topology_apply", key, payloadHash, plan, plan.AppliedAt); err != nil {
		return topologyv1.Plan{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return topologyv1.Plan{}, false, unavailable()
	}
	return plan, false, nil
}

func (s PostgresStore) GetOperatorCapacity(ctx context.Context, projectID, runtimeID string) (topologyv1.OperatorCapacity, error) {
	if s.DB == nil {
		return topologyv1.OperatorCapacity{}, unavailable()
	}
	return scanCapacity(s.DB.QueryRowContext(ctx, `SELECT r.id,r.project_id,r.runtime_id,r.revision,r.source,r.cpu_millicores,r.memory_bytes,r.reserved_cpu_millicores,r.reserved_memory_bytes,r.declared_by,r.declared_at,r.state_hash FROM operator_capacity_heads h JOIN operator_capacity_revisions r ON r.id=h.capacity_id AND r.revision=h.current_revision WHERE h.project_id=$1 AND h.runtime_id=$2`, projectID, runtimeID))
}

func (s PostgresStore) ReplayOperatorCapacity(ctx context.Context, projectID, key, payloadHash string) (topologyv1.OperatorCapacity, bool, error) {
	if s.DB == nil {
		return topologyv1.OperatorCapacity{}, false, unavailable()
	}
	return replayCapacityRow(ctx, s.DB, projectID, "capacity_apply", key, payloadHash)
}

func (s PostgresStore) ApplyOperatorCapacity(ctx context.Context, _ string, key, payloadHash string, request topologyv1.OperatorCapacityApplyRequest, capacity topologyv1.OperatorCapacity) (topologyv1.OperatorCapacity, bool, error) {
	if s.DB == nil {
		return topologyv1.OperatorCapacity{}, false, unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return topologyv1.OperatorCapacity{}, false, unavailable()
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, capacity.ProjectID); err != nil {
		return topologyv1.OperatorCapacity{}, false, projectError(err)
	}
	if replay, found, err := replayCapacity(ctx, tx, capacity.ProjectID, "capacity_apply", key, payloadHash); err != nil || found {
		return replay, found, err
	}
	current, err := scanCapacity(tx.QueryRowContext(ctx, `SELECT r.id,r.project_id,r.runtime_id,r.revision,r.source,r.cpu_millicores,r.memory_bytes,r.reserved_cpu_millicores,r.reserved_memory_bytes,r.declared_by,r.declared_at,r.state_hash FROM operator_capacity_heads h JOIN operator_capacity_revisions r ON r.id=h.capacity_id AND r.revision=h.current_revision WHERE h.project_id=$1 AND h.runtime_id=$2`, capacity.ProjectID, capacity.RuntimeID))
	if errors.Is(err, ErrNotFound) {
		current = topologyv1.OperatorCapacity{}
	} else if err != nil {
		return topologyv1.OperatorCapacity{}, false, err
	}
	if current.Revision != request.ExpectedRevision || current.StateHash != request.ExpectedStateHash {
		return topologyv1.OperatorCapacity{}, false, Error{Code: "TOPOLOGY_CAPACITY_STATE_CONFLICT", Status: 409, Message: "capacity state changed; refresh and retry"}
	}
	if current.ID == "" {
		capacity.ID = newID("cap")
	} else {
		capacity.ID = current.ID
	}
	capacity.Revision = current.Revision + 1
	capacity.StateHash = stateHash(capacity.ID, capacity.Revision, hashMust(request.Draft))
	if _, err = tx.ExecContext(ctx, `INSERT INTO operator_capacity_revisions(id,revision,project_id,runtime_id,source,cpu_millicores,memory_bytes,reserved_cpu_millicores,reserved_memory_bytes,state_hash,declared_by,declared_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, capacity.ID, capacity.Revision, capacity.ProjectID, capacity.RuntimeID, capacity.Source, capacity.CPUMillicores, capacity.MemoryBytes, capacity.ReservedCPUMillicores, capacity.ReservedMemoryBytes, capacity.StateHash, capacity.DeclaredBy, capacity.DeclaredAt); err != nil {
		return topologyv1.OperatorCapacity{}, false, unavailable()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operator_capacity_heads(project_id,runtime_id,capacity_id,current_revision,state_hash,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(project_id,runtime_id) DO UPDATE SET capacity_id=EXCLUDED.capacity_id,current_revision=EXCLUDED.current_revision,state_hash=EXCLUDED.state_hash,updated_at=EXCLUDED.updated_at`, capacity.ProjectID, capacity.RuntimeID, capacity.ID, capacity.Revision, capacity.StateHash, capacity.DeclaredAt); err != nil {
		return topologyv1.OperatorCapacity{}, false, unavailable()
	}
	if err = insertReplay(ctx, tx, capacity.ProjectID, "capacity_apply", key, payloadHash, capacity, capacity.DeclaredAt); err != nil {
		return topologyv1.OperatorCapacity{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return topologyv1.OperatorCapacity{}, false, unavailable()
	}
	return capacity, false, nil
}

type scanner interface{ Scan(...any) error }

func scanPlan(row scanner) (topologyv1.Plan, error) {
	var p topologyv1.Plan
	var raw []byte
	if err := row.Scan(&p.ID, &p.Revision, &p.ProjectID, &p.SchemaVersion, &p.PlanHash, &p.StateHash, &raw, &p.CreatedBy, &p.AppliedBy, &p.CreatedAt, &p.AppliedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, unavailable()
	}
	if err := json.Unmarshal(raw, &p.Assignments); err != nil {
		return topologyv1.Plan{}, unavailable()
	}
	return p, nil
}
func scanCapacity(row scanner) (topologyv1.OperatorCapacity, error) {
	var c topologyv1.OperatorCapacity
	if err := row.Scan(&c.ID, &c.ProjectID, &c.RuntimeID, &c.Revision, &c.Source, &c.CPUMillicores, &c.MemoryBytes, &c.ReservedCPUMillicores, &c.ReservedMemoryBytes, &c.DeclaredBy, &c.DeclaredAt, &c.StateHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, unavailable()
	}
	return c, nil
}
func insertReplay(ctx context.Context, tx *sql.Tx, projectID, operation, key, payloadHash string, response any, createdAt any) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return unavailable()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO control_mutation_idempotency(project_id,operation,idempotency_key,payload_hash,response_json,created_at) VALUES($1,$2,$3,$4,$5,$6)`, projectID, operation, key, payloadHash, raw, createdAt)
	if err != nil {
		return unavailable()
	}
	return nil
}
func replayPlan(ctx context.Context, tx *sql.Tx, projectID, operation, key, payloadHash string) (topologyv1.Plan, bool, error) {
	return replayPlanRow(ctx, tx, projectID, operation, key, payloadHash)
}
func replayPlanRow(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, operation, key, payloadHash string) (topologyv1.Plan, bool, error) {
	var stored string
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT payload_hash,response_json FROM control_mutation_idempotency WHERE project_id=$1 AND operation=$2 AND idempotency_key=$3`, projectID, operation, key).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return topologyv1.Plan{}, false, nil
	}
	if err != nil {
		return topologyv1.Plan{}, false, unavailable()
	}
	if stored != payloadHash {
		return topologyv1.Plan{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
	}
	var p topologyv1.Plan
	if json.Unmarshal(raw, &p) != nil {
		return p, false, unavailable()
	}
	return p, true, nil
}
func replayCapacity(ctx context.Context, tx *sql.Tx, projectID, operation, key, payloadHash string) (topologyv1.OperatorCapacity, bool, error) {
	return replayCapacityRow(ctx, tx, projectID, operation, key, payloadHash)
}
func replayCapacityRow(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, operation, key, payloadHash string) (topologyv1.OperatorCapacity, bool, error) {
	var stored string
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT payload_hash,response_json FROM control_mutation_idempotency WHERE project_id=$1 AND operation=$2 AND idempotency_key=$3`, projectID, operation, key).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return topologyv1.OperatorCapacity{}, false, nil
	}
	if err != nil {
		return topologyv1.OperatorCapacity{}, false, unavailable()
	}
	if stored != payloadHash {
		return topologyv1.OperatorCapacity{}, false, Error{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Message: "idempotency key was already used with a different payload"}
	}
	var c topologyv1.OperatorCapacity
	if json.Unmarshal(raw, &c) != nil {
		return c, false, unavailable()
	}
	return c, true, nil
}
func projectError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return Error{Code: "TOPOLOGY_PROJECT_NOT_FOUND", Status: 404, Message: "project not found"}
	}
	return unavailable()
}
