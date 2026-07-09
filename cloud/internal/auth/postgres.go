package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"
)

type PostgresStore struct {
	DB *sql.DB
}

func (s PostgresStore) PATCandidates(ctx context.Context, projectID string) ([]Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT p.id, p.user_id, u.email, COALESCE(pr.org_id, ''), m.project_id, m.role, p.token_hash, p.expires_at, p.revoked
FROM personal_access_tokens p
JOIN users u ON u.id = p.user_id
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
		if err := rows.Scan(&candidate.ID, &candidate.UserID, &candidate.Email, &candidate.OrgID, &candidate.ProjectID, &candidate.Role, &candidate.Hash, &candidate.ExpiresAt, &candidate.Revoked); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (s PostgresStore) OrgPATCandidates(ctx context.Context, orgID string) ([]Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT p.id, p.user_id, u.email, m.org_id, m.role, p.token_hash, p.expires_at, p.revoked
FROM personal_access_tokens p
JOIN users u ON u.id = p.user_id
JOIN user_memberships m ON m.user_id = p.user_id
WHERE m.org_id = $1 AND m.status = 'active'`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var candidate Candidate
		if err := rows.Scan(&candidate.ID, &candidate.UserID, &candidate.Email, &candidate.OrgID, &candidate.Role, &candidate.Hash, &candidate.ExpiresAt, &candidate.Revoked); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (s PostgresStore) IssuePATForEmail(ctx context.Context, email, projectID, tokenHash string, expiresAt time.Time) (Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT u.id, u.email, COALESCE(pr.org_id, ''), m.project_id, m.role
FROM users u
JOIN project_memberships m ON m.user_id = u.id
JOIN projects pr ON pr.id = m.project_id
WHERE lower(u.email) = lower($1) AND ($2 = '' OR m.project_id = $2)
ORDER BY m.created_at
LIMIT 1`, email, projectID)
	if err != nil {
		return Candidate{}, err
	}
	defer rows.Close()
	return s.issueFromRows(ctx, rows, tokenHash, expiresAt)
}

func (s PostgresStore) IssuePATForUser(ctx context.Context, userID, projectID, tokenHash string, expiresAt time.Time) (Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT u.id, u.email, COALESCE(pr.org_id, ''), m.project_id, m.role
FROM users u
JOIN project_memberships m ON m.user_id = u.id
JOIN projects pr ON pr.id = m.project_id
WHERE u.id = $1 AND ($2 = '' OR m.project_id = $2)
ORDER BY m.created_at
LIMIT 1`, userID, projectID)
	if err != nil {
		return Candidate{}, err
	}
	defer rows.Close()
	return s.issueFromRows(ctx, rows, tokenHash, expiresAt)
}

func (s PostgresStore) issueFromRows(ctx context.Context, rows *sql.Rows, tokenHash string, expiresAt time.Time) (Candidate, error) {
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Candidate{}, err
		}
		return Candidate{}, ErrNoMembership
	}
	candidate := Candidate{ID: newID("pat"), Hash: tokenHash, ExpiresAt: expiresAt}
	if err := rows.Scan(&candidate.UserID, &candidate.Email, &candidate.OrgID, &candidate.ProjectID, &candidate.Role); err != nil {
		return Candidate{}, err
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO personal_access_tokens(id, user_id, token_hash, expires_at, revoked) VALUES($1,$2,$3,$4,false)`, candidate.ID, candidate.UserID, tokenHash, expiresAt); err != nil {
		return Candidate{}, err
	}
	return candidate, rows.Err()
}

func (s PostgresStore) RevokePAT(ctx context.Context, tokenID string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE personal_access_tokens SET revoked = true WHERE id = $1`, tokenID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvalidToken
	}
	return nil
}

func newID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw[:])
}
