package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type EvidenceProjection struct {
	RolloutID      string
	State          string
	FailureCode    string
	ReadinessHash  string
	DesiredDigest  string
	PreviousDigest string
	RestoredDigest string
	Events         []EvidenceEvent
	TotalEvents    int
}

type EvidenceEvent struct {
	Version   uint64
	State     string
	StateHash string
	CreatedAt time.Time
}

func (s *SQLiteStore) ReadIncidentEvidence(ctx context.Context, projectID, serviceID string, since, until time.Time) (*EvidenceProjection, error) {
	var rolloutID string
	err := s.db.QueryRowContext(ctx, `
SELECT rollout_id
FROM rollouts
WHERE project_id = ? AND service_key = ? AND created_at_unix_nano >= ? AND created_at_unix_nano <= ?
ORDER BY updated_at_unix_nano DESC, rollout_id DESC
LIMIT 1
`, projectID, serviceID, since.UnixNano(), until.UnixNano()).Scan(&rolloutID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rollout evidence: %w", err)
	}
	record, err := s.GetRollout(ctx, rolloutID)
	if err != nil || record == nil {
		return nil, err
	}
	out := &EvidenceProjection{
		RolloutID:      rolloutID,
		State:          record.State,
		DesiredDigest:  record.Intent.Desired.Image.Digest,
		PreviousDigest: record.Intent.PreviousDigest,
	}
	if record.Error != nil {
		out.FailureCode = record.Error.Code
	}
	if record.Evidence != nil {
		out.ReadinessHash, err = record.Evidence.Hash()
		if err != nil {
			return nil, fmt.Errorf("hash rollout readiness evidence: %w", err)
		}
	}
	if record.State == deploymentv1.RolloutStateRolledBack {
		out.RestoredDigest = record.Intent.PreviousDigest
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rollout_events WHERE rollout_id = ? AND created_at_unix_nano >= ? AND created_at_unix_nano <= ?`, rolloutID, since.UnixNano(), until.UnixNano()).Scan(&out.TotalEvents); err != nil {
		return nil, fmt.Errorf("count rollout event evidence: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT event_json
FROM rollout_events
WHERE rollout_id = ? AND created_at_unix_nano >= ? AND created_at_unix_nano <= ?
ORDER BY created_at_unix_nano, version
LIMIT 129
`, rolloutID, since.UnixNano(), until.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("read rollout event evidence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event deploymentv1.RolloutEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, errors.New("stored rollout event evidence is invalid")
		}
		out.Events = append(out.Events, EvidenceEvent{Version: event.Version, State: event.State, StateHash: event.StateHash, CreatedAt: event.CreatedAt.UTC()})
	}
	return out, rows.Err()
}
