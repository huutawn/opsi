package auth

import (
	"context"
	"database/sql"
)

type PostgresStore struct {
	DB *sql.DB
}

func (s PostgresStore) PATCandidates(ctx context.Context, projectID string) ([]Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT p.user_id, m.project_id, m.role, p.token_hash, p.expires_at, p.revoked
FROM personal_access_tokens p
JOIN project_memberships m ON m.user_id = p.user_id
WHERE m.project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var candidate Candidate
		if err := rows.Scan(&candidate.UserID, &candidate.ProjectID, &candidate.Role, &candidate.Hash, &candidate.ExpiresAt, &candidate.Revoked); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}
