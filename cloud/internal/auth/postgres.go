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
SELECT p.user_id, COALESCE(pr.org_id, ''), m.project_id, m.role, p.token_hash, p.expires_at, p.revoked
FROM personal_access_tokens p
JOIN project_memberships m ON m.user_id = p.user_id
JOIN projects pr ON pr.id = m.project_id
WHERE m.project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var candidate Candidate
		if err := rows.Scan(&candidate.UserID, &candidate.OrgID, &candidate.ProjectID, &candidate.Role, &candidate.Hash, &candidate.ExpiresAt, &candidate.Revoked); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (s PostgresStore) OrgPATCandidates(ctx context.Context, orgID string) ([]Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT p.user_id, m.org_id, m.role, p.token_hash, p.expires_at, p.revoked
FROM personal_access_tokens p
JOIN user_memberships m ON m.user_id = p.user_id
WHERE m.org_id = $1 AND m.status = 'active'`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var candidate Candidate
		if err := rows.Scan(&candidate.UserID, &candidate.OrgID, &candidate.Role, &candidate.Hash, &candidate.ExpiresAt, &candidate.Revoked); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}
