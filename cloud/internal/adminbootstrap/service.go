package adminbootstrap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

type bootstrapState struct {
	UserID         string
	OrganizationID string
	ProjectID      string
	Email          string
	OrgSlug        string
	ProjectSlug    string
}

func (s Service) ProvisionBootstrapOwner(ctx context.Context, req Request) (Result, error) {
	if s.DB == nil {
		return Result{}, &Error{Code: CodeRequiresPostgres, Message: "bootstrap-owner requires database_url"}
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Result{}, fmt.Errorf("begin bootstrap owner transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('opsi:admin:first_owner'))`); err != nil {
		return Result{}, fmt.Errorf("lock bootstrap owner state: %w", err)
	}

	state, initialized, err := loadBootstrapState(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	if req.LinkExistingOwner {
		if !initialized {
			return Result{}, &Error{Code: CodeNotInitialized, Message: "first owner must be initialized before linking OAuth identity"}
		}
		return s.linkExistingOwner(ctx, tx, state, req)
	}
	if initialized && (state.Email != req.Email || state.OrgSlug != req.OrgSlug || state.ProjectSlug != req.ProjectSlug) {
		return Result{}, &Error{Code: CodeAlreadyInitialized, Message: "first owner is already initialized with a different identity tuple"}
	}

	now := s.clock()
	result := Result{MembershipRole: "Owner"}
	result.UserID, result.UserCreated, err = ensureUser(ctx, tx, req, now)
	if err != nil {
		return Result{}, err
	}
	result.OrganizationID, result.OrgCreated, err = ensureOrganization(ctx, tx, req, now)
	if err != nil {
		return Result{}, err
	}
	result.ProjectID, result.ProjectCreated, err = ensureProject(ctx, tx, result.OrganizationID, result.UserID, req, now)
	if err != nil {
		return Result{}, err
	}
	if initialized && (state.UserID != result.UserID || state.OrganizationID != result.OrganizationID || state.ProjectID != result.ProjectID) {
		return Result{}, &Error{Code: CodeConflict, Message: "bootstrap marker does not match the canonical identity records"}
	}

	orgMembershipCreated, orgMembershipChanged, err := ensureOrganizationOwner(ctx, tx, result.OrganizationID, result.UserID, now)
	if err != nil {
		return Result{}, err
	}
	projectMembershipCreated, projectMembershipChanged, err := ensureProjectOwner(ctx, tx, result.ProjectID, result.UserID, now)
	if err != nil {
		return Result{}, err
	}
	result.MembershipCreated = orgMembershipCreated || projectMembershipCreated

	oauthCreated := false
	if req.OAuthProvider != "" {
		oauthCreated, err = ensureOAuthIdentity(ctx, tx, result.UserID, req.OAuthProvider, req.OAuthSubject, now)
		if err != nil {
			return Result{}, err
		}
		result.OAuthLinked = true
	}
	if req.IssuePAT {
		result.PATCreated, result.InitialPATUnavailable, err = ensureInitialPAT(ctx, tx, result.UserID, req.PATTokenHash, now)
		if err != nil {
			return Result{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO cloud_admin_bootstrap_state(key,user_id,organization_id,project_id,normalized_email,org_slug,project_slug,completed_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)
		ON CONFLICT(key) DO UPDATE SET updated_at=EXCLUDED.updated_at`, bootstrapStateKey, result.UserID, result.OrganizationID, result.ProjectID, req.Email, req.OrgSlug, req.ProjectSlug, now); err != nil {
		return Result{}, fmt.Errorf("persist bootstrap owner state: %w", err)
	}

	changed := result.UserCreated || result.OrgCreated || result.ProjectCreated || result.MembershipCreated || orgMembershipChanged || projectMembershipChanged || oauthCreated || result.PATCreated || !initialized
	result.Reused = !changed
	if changed {
		metadata, _ := json.Marshal(map[string]any{
			"user_id": result.UserID, "organization_id": result.OrganizationID, "project_id": result.ProjectID,
			"email_hash": hashText(req.Email), "org_slug": req.OrgSlug, "project_slug": req.ProjectSlug,
			"oauth_provider": req.OAuthProvider, "pat_created": result.PATCreated, "reused": initialized,
		})
		if _, err := tx.ExecContext(ctx, `INSERT INTO cloud_audit_events(id,org_id,project_id,actor_user_id,actor_type,action,resource_type,resource_id,result,metadata_redacted,created_at)
			VALUES($1,$2,$3,NULL,'local-admin','ADMIN_BOOTSTRAP_OWNER_COMPLETED','admin_bootstrap',$4,'success',$5,$6)`, newID("aud"), result.OrganizationID, result.ProjectID, bootstrapStateKey, string(metadata), now); err != nil {
			return Result{}, fmt.Errorf("audit bootstrap owner: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit bootstrap owner: %w", err)
	}
	return result, nil
}

func (s Service) linkExistingOwner(ctx context.Context, tx *sql.Tx, state bootstrapState, req Request) (Result, error) {
	now := s.clock()
	result := Result{
		UserID: state.UserID, OrganizationID: state.OrganizationID, ProjectID: state.ProjectID,
		MembershipRole: "Owner", OAuthLinked: true,
	}
	orgCreated, orgChanged, err := ensureOrganizationOwner(ctx, tx, state.OrganizationID, state.UserID, now)
	if err != nil {
		return Result{}, err
	}
	projectCreated, projectChanged, err := ensureProjectOwner(ctx, tx, state.ProjectID, state.UserID, now)
	if err != nil {
		return Result{}, err
	}
	result.MembershipCreated = orgCreated || projectCreated
	oauthCreated, err := ensureOAuthIdentity(ctx, tx, state.UserID, req.OAuthProvider, req.OAuthSubject, now)
	if err != nil {
		return Result{}, err
	}
	changed := orgCreated || orgChanged || projectCreated || projectChanged || oauthCreated
	result.Reused = !changed
	if changed {
		metadata, _ := json.Marshal(map[string]any{
			"user_id": state.UserID, "organization_id": state.OrganizationID, "project_id": state.ProjectID,
			"oauth_provider": req.OAuthProvider, "reused": false,
		})
		if _, err := tx.ExecContext(ctx, `INSERT INTO cloud_audit_events(id,org_id,project_id,actor_user_id,actor_type,action,resource_type,resource_id,result,metadata_redacted,created_at)
			VALUES($1,$2,$3,NULL,'local-admin','ADMIN_BOOTSTRAP_OWNER_OAUTH_LINKED','admin_bootstrap',$4,'success',$5,$6)`, newID("aud"), state.OrganizationID, state.ProjectID, bootstrapStateKey, string(metadata), now); err != nil {
			return Result{}, fmt.Errorf("audit bootstrap owner OAuth link: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit bootstrap owner OAuth link: %w", err)
	}
	return result, nil
}

func loadBootstrapState(ctx context.Context, tx *sql.Tx) (bootstrapState, bool, error) {
	var state bootstrapState
	err := tx.QueryRowContext(ctx, `SELECT user_id,organization_id,project_id,normalized_email,org_slug,project_slug FROM cloud_admin_bootstrap_state WHERE key=$1 FOR UPDATE`, bootstrapStateKey).
		Scan(&state.UserID, &state.OrganizationID, &state.ProjectID, &state.Email, &state.OrgSlug, &state.ProjectSlug)
	if errors.Is(err, sql.ErrNoRows) {
		return bootstrapState{}, false, nil
	}
	return state, err == nil, err
}

func ensureUser(ctx context.Context, tx *sql.Tx, req Request, now time.Time) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE lower(email)=lower($1) FOR UPDATE`, req.Email)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(ids) > 1 {
		return "", false, &Error{Code: CodeConflict, Message: "multiple users have the normalized email"}
	}
	if len(ids) == 1 {
		return ids[0], false, nil
	}
	id := newID("user")
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,display_name,created_at) VALUES($1,$2,NULLIF($3,''),$4)`, id, req.Email, req.DisplayName, now); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func ensureOrganization(ctx context.Context, tx *sql.Tx, req Request, now time.Time) (string, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM organizations WHERE lower(slug)=lower($1) FOR UPDATE`, req.OrgSlug).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	id = newID("org")
	if _, err := tx.ExecContext(ctx, `INSERT INTO organizations(id,name,slug,status,created_at,updated_at) VALUES($1,$2,$3,'active',$4,$4)`, id, req.OrgName, req.OrgSlug, now); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func ensureProject(ctx context.Context, tx *sql.Tx, orgID, userID string, req Request, now time.Time) (string, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE org_id=$1 AND lower(slug)=lower($2) FOR UPDATE`, orgID, req.ProjectSlug).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	project, err := registry.CreateProjectInTx(ctx, tx, orgID, req.ProjectName, req.ProjectSlug, userID, now)
	if err != nil {
		return "", false, err
	}
	return project.ID, true, nil
}

func ensureOrganizationOwner(ctx context.Context, tx *sql.Tx, orgID, userID string, now time.Time) (bool, bool, error) {
	var conflicting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_memberships WHERE org_id=$1 AND lower(role)='owner' AND status='active' AND user_id<>$2`, orgID, userID).Scan(&conflicting); err != nil {
		return false, false, err
	}
	if conflicting > 0 {
		return false, false, &Error{Code: CodeConflict, Message: "organization already has a different owner"}
	}
	var role, status string
	err := tx.QueryRowContext(ctx, `SELECT role,status FROM user_memberships WHERE org_id=$1 AND user_id=$2 FOR UPDATE`, orgID, userID).Scan(&role, &status)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO user_memberships(id,org_id,user_id,role,status,created_at,updated_at) VALUES($1,$2,$3,'owner','active',$4,$4)`, newID("member"), orgID, userID, now)
		return err == nil, false, err
	}
	if err != nil {
		return false, false, err
	}
	if role == "owner" && status == "active" {
		return false, false, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE user_memberships SET role='owner',status='active',updated_at=$1 WHERE org_id=$2 AND user_id=$3`, now, orgID, userID)
	return false, err == nil, err
}

func ensureProjectOwner(ctx context.Context, tx *sql.Tx, projectID, userID string, now time.Time) (bool, bool, error) {
	var conflicting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_memberships WHERE project_id=$1 AND lower(role)='owner' AND user_id<>$2`, projectID, userID).Scan(&conflicting); err != nil {
		return false, false, err
	}
	if conflicting > 0 {
		return false, false, &Error{Code: CodeProjectOwnerConflict, Message: "project already has a different owner"}
	}
	var role string
	err := tx.QueryRowContext(ctx, `SELECT role FROM project_memberships WHERE project_id=$1 AND user_id=$2 FOR UPDATE`, projectID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO project_memberships(project_id,user_id,role,created_at) VALUES($1,$2,'Owner',$3)`, projectID, userID, now)
		return err == nil, false, err
	}
	if err != nil {
		return false, false, err
	}
	if role == "Owner" || role == "owner" {
		return false, false, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE project_memberships SET role='Owner' WHERE project_id=$1 AND user_id=$2`, projectID, userID)
	return false, err == nil, err
}

func ensureOAuthIdentity(ctx context.Context, tx *sql.Tx, userID, provider, subject string, now time.Time) (bool, error) {
	var linkedUser string
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM oauth_identities WHERE provider=$1 AND subject=$2 FOR UPDATE`, provider, subject).Scan(&linkedUser)
	if err == nil {
		if linkedUser != userID {
			return false, &Error{Code: CodeOAuthIdentityConflict, Message: "OAuth identity is linked to another user"}
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var linkedSubject string
	err = tx.QueryRowContext(ctx, `SELECT subject FROM oauth_identities WHERE user_id=$1 AND provider=$2 FOR UPDATE`, userID, provider).Scan(&linkedSubject)
	if err == nil && linkedSubject != subject {
		return false, &Error{Code: CodeOAuthIdentityConflict, Message: "user is linked to a different OAuth subject"}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO oauth_identities(id,user_id,provider,subject,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5)`, newID("oauth"), userID, provider, subject, now)
	return err == nil, err
}

func ensureInitialPAT(ctx context.Context, tx *sql.Tx, userID, tokenHash string, now time.Time) (bool, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM personal_access_tokens WHERE user_id=$1 AND purpose=$2 FOR UPDATE`, userID, bootstrapPATPurpose).Scan(&id)
	if err == nil {
		return false, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, false, err
	}
	if tokenHash == "" {
		return false, false, &Error{Code: CodePATOutputUnavailable, Message: "initial PAT output file must be available before issuing a token"}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO personal_access_tokens(id,user_id,token_hash,expires_at,revoked,purpose,created_at) VALUES($1,$2,$3,$4,false,$5,$6)`, newID("pat"), userID, tokenHash, now.Add(90*24*time.Hour), bootstrapPATPurpose, now)
	return err == nil, false, err
}

func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw[:])
}
