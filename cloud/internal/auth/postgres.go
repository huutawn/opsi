package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type PostgresStore struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s PostgresStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s PostgresStore) OAuthUser(ctx context.Context, provider, subject string) (string, error) {
	var userID string
	err := s.DB.QueryRowContext(ctx, `SELECT user_id FROM oauth_identities WHERE provider=$1 AND subject=$2`, provider, subject).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", ErrOAuthIdentity
	}
	return userID, err
}

// ProvisionOAuthUser creates an isolated personal organization and its default
// project for a first-time OAuth identity. A browser callback cannot use this
// path to attach the user to a client-selected project.
func (s PostgresStore) ProvisionOAuthUser(ctx context.Context, provider, subject string) (string, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	subject = strings.TrimSpace(subject)
	if s.DB == nil || provider == "" || subject == "" {
		return "", ErrOAuthIdentity
	}

	hash := sha256.Sum256([]byte(provider + "\x00" + subject))
	hashText := hex.EncodeToString(hash[:])
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, hashText); err != nil {
		return "", err
	}
	var existingUserID string
	err = tx.QueryRowContext(ctx, `
		SELECT user_id
		FROM oauth_identities
		WHERE provider = $1 AND subject = $2
		FOR UPDATE`, provider, subject).Scan(&existingUserID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return existingUserID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	now := s.now()
	userID := newID("user")
	organizationID := newID("org")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users (id, email, created_at)
		VALUES ($1, $2, $3)`,
		userID, "github-"+hashText[:24]+"@users.noreply.opsi.invalid", now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO organizations (id, name, slug, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', $4, $4)`,
		organizationID, "Personal workspace", "personal-"+hashText[:24], now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_memberships (id, org_id, user_id, role, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'owner', 'active', $4, $4)`,
		newID("member"), organizationID, userID, now); err != nil {
		return "", err
	}
	if _, err := registry.CreateProjectInTx(ctx, tx, organizationID, "Default", "default", userID, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_identities (id, user_id, provider, subject, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		newID("oauth"), userID, provider, subject, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return userID, nil
}

func (s PostgresStore) PATCandidates(ctx context.Context, projectID string) ([]Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT p.id, p.user_id, u.email, COALESCE(pr.org_id, ''), m.project_id, m.role, p.token_hash, p.expires_at, p.revoked
FROM personal_access_tokens p
JOIN users u ON u.id = p.user_id
JOIN project_memberships m ON m.user_id = p.user_id
JOIN projects pr ON pr.id = m.project_id
WHERE ($1 = '' OR m.project_id = $1)
ORDER BY p.created_at DESC`, projectID)
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
WHERE m.org_id = $1 AND m.status = 'active'
ORDER BY p.created_at DESC`, orgID)
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

func (s PostgresStore) UserProjectCandidates(ctx context.Context, userID string) ([]OAuthProjectCandidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT pr.id, pr.name, COALESCE(pr.slug, ''), m.role
FROM project_memberships m
JOIN projects pr ON pr.id = m.project_id
WHERE m.user_id = $1
ORDER BY m.created_at, pr.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []OAuthProjectCandidate
	for rows.Next() {
		var p OAuthProjectCandidate
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Role); err != nil {
			return nil, err
		}
		p.Role = normalizeRole(p.Role)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s PostgresStore) IssuePATForUser(ctx context.Context, userID, projectID, tokenHash string, expiresAt time.Time) (Candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT u.id, u.email, COALESCE(pr.org_id, ''), m.project_id, m.role
FROM users u
JOIN project_memberships m ON m.user_id = u.id
JOIN projects pr ON pr.id = m.project_id
WHERE u.id = $1 AND ($2 = '' OR m.project_id = $2)
ORDER BY m.created_at
LIMIT 2`, userID, projectID)
	if err != nil {
		return Candidate{}, err
	}
	defer rows.Close()
	return s.issueFromRows(ctx, rows, tokenHash, expiresAt, projectID == "")
}

func (s PostgresStore) issueFromRows(ctx context.Context, rows *sql.Rows, tokenHash string, expiresAt time.Time, requireUnique bool) (Candidate, error) {
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
	if requireUnique && rows.Next() {
		return Candidate{}, ErrProjectChoice
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
