package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	githubInstallationSelect = `SELECT installation_id, account_id, account_login, account_type, status, suspended, created_at, updated_at FROM github_installations`
	githubRepositorySelect   = `SELECT repository_id, installation_id, owner_id, owner_login, name, full_name, private, archived, disabled, default_branch, status, created_at, updated_at FROM github_repositories`
	githubLinkSelect         = `SELECT installation_id, project_id, claimed_by, status, claimed_at, revoked_at FROM github_installation_project_links`
	githubClaimSelect        = `SELECT repository_id, installation_id, project_id, claimed_by, status, claimed_at, released_at FROM github_repository_claims`
	githubBindingSelect      = `SELECT id, project_id, service_id, repository_id, installation_id, service_key, config_path, status, created_by, created_at, updated_at, removed_at FROM github_service_bindings`
)

type githubDBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s PostgresService) UpsertGitHubInstallation(installation GitHubInstallation) (GitHubInstallation, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return GitHubInstallation{}, githubPostgresError(err)
	}
	defer tx.Rollback()
	installation, err = upsertGitHubInstallationTx(ctx, tx, installation, s.clock())
	if err != nil {
		return GitHubInstallation{}, githubPostgresError(err)
	}
	if err := tx.Commit(); err != nil {
		return GitHubInstallation{}, githubPostgresError(err)
	}
	return installation, nil
}

func upsertGitHubInstallationTx(ctx context.Context, tx githubDBTX, installation GitHubInstallation, now time.Time) (GitHubInstallation, error) {
	if err := validateGitHubInstallation(installation); err != nil {
		return GitHubInstallation{}, err
	}
	if installation.CreatedAt.IsZero() {
		installation.CreatedAt = now
	}
	if installation.UpdatedAt.IsZero() {
		installation.UpdatedAt = now
	}
	var accountID int64
	var createdAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT account_id, created_at FROM github_installations WHERE installation_id=$1 FOR UPDATE`, installation.InstallationID).Scan(&accountID, &createdAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO github_installations(installation_id, account_id, account_login, account_type, status, suspended, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, installation.InstallationID, installation.AccountID, installation.AccountLogin, installation.AccountType, installation.Status, installation.Suspended, installation.CreatedAt, installation.UpdatedAt)
	case err != nil:
		return GitHubInstallation{}, err
	case accountID != installation.AccountID:
		return GitHubInstallation{}, ErrGitHubEventConflict
	default:
		installation.CreatedAt = createdAt
		_, err = tx.ExecContext(ctx, `UPDATE github_installations SET account_login=$2, account_type=$3, status=$4, suspended=$5, updated_at=$6 WHERE installation_id=$1`, installation.InstallationID, installation.AccountLogin, installation.AccountType, installation.Status, installation.Suspended, installation.UpdatedAt)
	}
	return installation, err
}

func (s PostgresService) UpsertGitHubRepository(repository GitHubRepository) (GitHubRepository, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return GitHubRepository{}, githubPostgresError(err)
	}
	defer tx.Rollback()
	repository, err = upsertGitHubRepositoryTx(ctx, tx, repository, s.clock())
	if err != nil {
		return GitHubRepository{}, githubPostgresError(err)
	}
	if err := tx.Commit(); err != nil {
		return GitHubRepository{}, githubPostgresError(err)
	}
	return repository, nil
}

func upsertGitHubRepositoryTx(ctx context.Context, tx githubDBTX, repository GitHubRepository, now time.Time) (GitHubRepository, error) {
	if err := validateGitHubRepository(repository); err != nil {
		return GitHubRepository{}, err
	}
	var installationExists bool
	if err := tx.QueryRowContext(ctx, `SELECT true FROM github_installations WHERE installation_id=$1`, repository.InstallationID).Scan(&installationExists); errors.Is(err, sql.ErrNoRows) {
		return GitHubRepository{}, ErrNotFound
	} else if err != nil {
		return GitHubRepository{}, err
	}
	if repository.CreatedAt.IsZero() {
		repository.CreatedAt = now
	}
	if repository.UpdatedAt.IsZero() {
		repository.UpdatedAt = now
	}
	var installationID int64
	var createdAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT installation_id, created_at FROM github_repositories WHERE repository_id=$1 FOR UPDATE`, repository.RepositoryID).Scan(&installationID, &createdAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO github_repositories(repository_id, installation_id, owner_id, owner_login, name, full_name, private, archived, disabled, default_branch, status, created_at, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, repository.RepositoryID, repository.InstallationID, repository.OwnerID, repository.OwnerLogin, repository.Name, repository.FullName, repository.Private, repository.Archived, repository.Disabled, repository.DefaultBranch, repository.Status, repository.CreatedAt, repository.UpdatedAt)
	case err != nil:
		return GitHubRepository{}, err
	case installationID != repository.InstallationID:
		return GitHubRepository{}, ErrGitHubEventConflict
	default:
		repository.CreatedAt = createdAt
		_, err = tx.ExecContext(ctx, `UPDATE github_repositories SET owner_id=$2, owner_login=$3, name=$4, full_name=$5, private=$6, archived=$7, disabled=$8, default_branch=$9, status=$10, updated_at=$11 WHERE repository_id=$1`, repository.RepositoryID, repository.OwnerID, repository.OwnerLogin, repository.Name, repository.FullName, repository.Private, repository.Archived, repository.Disabled, repository.DefaultBranch, repository.Status, repository.UpdatedAt)
	}
	return repository, err
}

func (s PostgresService) MarkGitHubInstallationStatus(installationID int64, status string, suspended bool) error {
	if installationID <= 0 || !validGitHubInstallationStatus(status) || (status == GitHubInstallationSuspended) != suspended {
		return githubInvalid("GITHUB_INSTALLATION_INVALID", "installation ID and status are invalid")
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return githubPostgresError(err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE github_installations SET status=$2, suspended=$3, updated_at=$4 WHERE installation_id=$1`, installationID, status, suspended, s.clock())
	if err != nil {
		return githubPostgresError(err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	if status == GitHubInstallationDeleted {
		if _, err := tx.ExecContext(ctx, `UPDATE github_repositories SET status='removed', updated_at=$2 WHERE installation_id=$1`, installationID, s.clock()); err != nil {
			return githubPostgresError(err)
		}
	}
	return githubPostgresError(tx.Commit())
}

func (s PostgresService) MarkGitHubRepositoryStatus(repositoryID int64, status string) error {
	if repositoryID <= 0 || !validGitHubRepositoryStatus(status) {
		return githubInvalid("GITHUB_REPOSITORY_INVALID", "repository ID and status are invalid")
	}
	result, err := s.DB.ExecContext(context.Background(), `UPDATE github_repositories SET status=$2, updated_at=$3 WHERE repository_id=$1`, repositoryID, status, s.clock())
	if err != nil {
		return githubPostgresError(err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s PostgresService) RecordGitHubWebhookEvent(ctx context.Context, event GitHubWebhookMutation) (bool, error) {
	if event.DeliveryID == "" || event.InstallationID <= 0 {
		return false, githubInvalid("GITHUB_WEBHOOK_INVALID", "webhook delivery identity is invalid")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, githubPostgresError(err)
	}
	defer tx.Rollback()
	receivedAt := event.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = s.clock()
	}
	repositoryID := int64(0)
	if event.Repository != nil {
		repositoryID = event.Repository.RepositoryID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO github_webhook_deliveries(delivery_id,event,action,installation_id,repository_id,received_at,processed_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (delivery_id) DO NOTHING`, event.DeliveryID, event.Event, event.Action, event.InstallationID, nullableGitHubID(repositoryID), receivedAt, s.clock())
	if err != nil {
		return false, githubPostgresError(err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, githubPostgresError(err)
	}
	if inserted == 0 {
		if err := tx.Commit(); err != nil {
			return false, githubPostgresError(err)
		}
		return true, nil
	}
	if err := applyGitHubWebhookTx(ctx, tx, event, receivedAt); err != nil {
		return false, githubPostgresError(err)
	}
	if err := auditGitHubWebhookTx(ctx, tx, event, s.clock()); err != nil {
		return false, githubPostgresError(err)
	}
	if err := tx.Commit(); err != nil {
		return false, githubPostgresError(err)
	}
	return false, nil
}

func applyGitHubWebhookTx(ctx context.Context, tx githubDBTX, event GitHubWebhookMutation, at time.Time) error {
	switch event.Event {
	case "installation":
		status, suspended := GitHubInstallationActive, false
		if event.Action == "new_permissions_accepted" {
			var err error
			status, suspended, err = currentGitHubInstallationStatus(ctx, tx, event.InstallationID)
			if errors.Is(err, sql.ErrNoRows) {
				status, suspended, err = GitHubInstallationActive, false, nil
			}
			if err != nil {
				return err
			}
		} else {
			switch event.Action {
			case "created", "unsuspend":
			case "deleted":
				status = GitHubInstallationDeleted
			case "suspend":
				status, suspended = GitHubInstallationSuspended, true
			default:
				return githubInvalid("GITHUB_WEBHOOK_INVALID", "unsupported installation action")
			}
		}
		installation := GitHubInstallation{InstallationID: event.InstallationID, AccountID: event.AccountID, AccountLogin: event.AccountLogin, AccountType: event.AccountType, Status: status, Suspended: suspended, CreatedAt: at, UpdatedAt: at}
		if _, err := upsertGitHubInstallationTx(ctx, tx, installation, at); err != nil {
			return err
		}
		if status == GitHubInstallationDeleted {
			_, err := tx.ExecContext(ctx, `UPDATE github_repositories SET status='removed', updated_at=$2 WHERE installation_id=$1`, event.InstallationID, at)
			return err
		}
		return nil
	case "installation_repositories":
		if event.Action != "added" && event.Action != "removed" {
			return githubInvalid("GITHUB_WEBHOOK_INVALID", "unsupported installation repositories action")
		}
		seen := map[int64]struct{}{}
		for _, group := range []struct {
			repositories []GitHubRepository
			status       string
		}{{event.Added, GitHubRepositoryActive}, {event.Removed, GitHubRepositoryRemoved}} {
			for _, repository := range group.repositories {
				if _, ok := seen[repository.RepositoryID]; ok {
					return githubInvalid("GITHUB_WEBHOOK_INVALID", "webhook contains duplicate repository identity")
				}
				seen[repository.RepositoryID] = struct{}{}
				if group.status == GitHubRepositoryRemoved {
					if err := removeGitHubRepositoryTx(ctx, tx, event.InstallationID, repository, at); err != nil {
						return err
					}
				} else {
					if err := activateGitHubRepositoryTx(ctx, tx, event.InstallationID, repository.RepositoryID, at); err != nil {
						return err
					}
				}
			}
		}
		return nil
	case "repository":
		if event.Repository == nil {
			return githubInvalid("GITHUB_WEBHOOK_INVALID", "repository event is missing repository")
		}
		status := GitHubRepositoryActive
		switch event.Action {
		case "created", "renamed", "edited", "archived", "unarchived", "transferred":
		case "deleted":
			status = GitHubRepositoryDeleted
		default:
			return githubInvalid("GITHUB_WEBHOOK_INVALID", "unsupported repository action")
		}
		repository := *event.Repository
		repository.InstallationID = event.InstallationID
		repository.Status = status
		repository.CreatedAt, repository.UpdatedAt = at, at
		_, err := upsertGitHubRepositoryTx(ctx, tx, repository, at)
		return err
	default:
		return githubInvalid("GITHUB_WEBHOOK_INVALID", "unsupported GitHub event")
	}
}

func activateGitHubRepositoryTx(ctx context.Context, tx githubDBTX, installationID, repositoryID int64, at time.Time) error {
	current, err := scanGitHubRepository(tx.QueryRowContext(ctx, githubRepositorySelect+` WHERE repository_id=$1 FOR UPDATE`, repositoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current.InstallationID != installationID {
		return ErrGitHubEventConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE github_repositories SET status='active',updated_at=$2 WHERE repository_id=$1`, repositoryID, at)
	return err
}

func removeGitHubRepositoryTx(ctx context.Context, tx githubDBTX, installationID int64, repository GitHubRepository, at time.Time) error {
	current, err := scanGitHubRepository(tx.QueryRowContext(ctx, githubRepositorySelect+` WHERE repository_id=$1 FOR UPDATE`, repository.RepositoryID))
	if err == nil {
		if current.InstallationID != installationID {
			return ErrGitHubEventConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE github_repositories SET status='removed',updated_at=$2 WHERE repository_id=$1`, repository.RepositoryID, at)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func currentGitHubInstallationStatus(ctx context.Context, tx githubDBTX, installationID int64) (string, bool, error) {
	var status string
	var suspended bool
	err := tx.QueryRowContext(ctx, `SELECT status, suspended FROM github_installations WHERE installation_id=$1`, installationID).Scan(&status, &suspended)
	return status, suspended, err
}

func auditGitHubWebhookTx(ctx context.Context, tx githubDBTX, event GitHubWebhookMutation, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.org_id FROM github_installation_project_links l JOIN projects p ON p.id=l.project_id WHERE l.installation_id=$1 AND l.status='active'`, event.InstallationID)
	if err != nil {
		return err
	}
	var projects []Project
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.OrgID); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, project := range projects {
		metadata := map[string]any{"installation_id": event.InstallationID, "delivery_id": event.DeliveryID, "event": event.Event, "action": event.Action}
		if event.Repository != nil {
			metadata["repository_id"] = event.Repository.RepositoryID
		}
		if err := insertGitHubAuditTx(ctx, tx, project, "", "github.webhook.processed", "github_webhook_delivery", event.DeliveryID, metadata, at); err != nil {
			return err
		}
	}
	return nil
}

func (s PostgresService) ListGitHubInstallations(projectID string) ([]GitHubInstallation, error) {
	ctx := context.Background()
	if _, err := githubProject(ctx, s.DB, projectID); err != nil {
		return nil, githubPostgresError(err)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT i.installation_id, i.account_id, i.account_login, i.account_type, i.status, i.suspended, i.created_at, i.updated_at FROM github_installations i JOIN github_installation_project_links l ON l.installation_id=i.installation_id WHERE l.project_id=$1 AND l.status='active' ORDER BY i.installation_id`, projectID)
	if err != nil {
		return nil, githubPostgresError(err)
	}
	defer rows.Close()
	var installations []GitHubInstallation
	for rows.Next() {
		installation, err := scanGitHubInstallation(rows)
		if err != nil {
			return nil, githubPostgresError(err)
		}
		installations = append(installations, installation)
	}
	return installations, githubPostgresError(rows.Err())
}

func (s PostgresService) ListGitHubRepositories(projectID string) ([]GitHubRepository, error) {
	ctx := context.Background()
	if _, err := githubProject(ctx, s.DB, projectID); err != nil {
		return nil, githubPostgresError(err)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.repository_id, r.installation_id, r.owner_id, r.owner_login, r.name, r.full_name, r.private, r.archived, r.disabled, r.default_branch, r.status, r.created_at, r.updated_at, c.project_id, c.status FROM github_repositories r JOIN github_installation_project_links l ON l.installation_id=r.installation_id LEFT JOIN github_repository_claims c ON c.repository_id=r.repository_id WHERE l.project_id=$1 AND l.status='active' ORDER BY r.repository_id`, projectID)
	if err != nil {
		return nil, githubPostgresError(err)
	}
	defer rows.Close()
	var repositories []GitHubRepository
	for rows.Next() {
		var repository GitHubRepository
		var claimProjectID, claimStatus sql.NullString
		if err := rows.Scan(&repository.RepositoryID, &repository.InstallationID, &repository.OwnerID, &repository.OwnerLogin, &repository.Name, &repository.FullName, &repository.Private, &repository.Archived, &repository.Disabled, &repository.DefaultBranch, &repository.Status, &repository.CreatedAt, &repository.UpdatedAt, &claimProjectID, &claimStatus); err != nil {
			return nil, githubPostgresError(err)
		}
		repository.ClaimStatus = "available"
		if claimStatus.String == GitHubLinkActive {
			if claimProjectID.String == projectID {
				repository.ClaimStatus = GitHubLinkActive
				repository.ClaimedProjectID = projectID
			} else {
				repository.ClaimStatus = "conflict"
			}
		}
		repositories = append(repositories, repository)
	}
	return repositories, githubPostgresError(rows.Err())
}

func (s PostgresService) ClaimGitHubInstallation(projectID string, installationID int64, userID string) (GitHubInstallationProjectLink, error) {
	if installationID <= 0 || userID == "" {
		return GitHubInstallationProjectLink{}, githubInvalid("GITHUB_INSTALLATION_CLAIM_INVALID", "installation and claiming user are required")
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return GitHubInstallationProjectLink{}, githubPostgresError(err)
	}
	defer tx.Rollback()
	project, err := githubProject(ctx, tx, projectID)
	if err != nil {
		return GitHubInstallationProjectLink{}, githubPostgresError(err)
	}
	installation, err := scanGitHubInstallation(tx.QueryRowContext(ctx, githubInstallationSelect+` WHERE installation_id=$1 FOR UPDATE`, installationID))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubInstallationProjectLink{}, ErrNotFound
	}
	if err != nil {
		return GitHubInstallationProjectLink{}, githubPostgresError(err)
	}
	if installation.Status != GitHubInstallationActive || installation.Suspended {
		return GitHubInstallationProjectLink{}, githubConflict("GITHUB_INSTALLATION_UNAVAILABLE", "GitHub installation is not active")
	}
	link, err := scanGitHubLink(tx.QueryRowContext(ctx, githubLinkSelect+` WHERE installation_id=$1 AND project_id=$2`, installationID, projectID))
	if err == nil && link.Status == GitHubLinkActive {
		if err := tx.Commit(); err != nil {
			return GitHubInstallationProjectLink{}, githubPostgresError(err)
		}
		return link, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GitHubInstallationProjectLink{}, githubPostgresError(err)
	}
	now := s.clock()
	link = GitHubInstallationProjectLink{InstallationID: installationID, ProjectID: projectID, ClaimedBy: userID, Status: GitHubLinkActive, ClaimedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO github_installation_project_links(installation_id,project_id,claimed_by,status,claimed_at,revoked_at) VALUES($1,$2,$3,'active',$4,NULL) ON CONFLICT (installation_id,project_id) DO UPDATE SET claimed_by=EXCLUDED.claimed_by,status='active',claimed_at=EXCLUDED.claimed_at,revoked_at=NULL`, installationID, projectID, userID, now)
	if err != nil {
		return GitHubInstallationProjectLink{}, githubPostgresError(err)
	}
	if err := insertGitHubAuditTx(ctx, tx, project, userID, "github.installation.claimed", "github_installation", strconv.FormatInt(installationID, 10), map[string]any{"installation_id": installationID}, now); err != nil {
		return GitHubInstallationProjectLink{}, githubPostgresError(err)
	}
	if err := tx.Commit(); err != nil {
		return GitHubInstallationProjectLink{}, githubPostgresError(err)
	}
	return link, nil
}

func (s PostgresService) ClaimGitHubRepository(projectID string, repositoryID int64, userID string) (GitHubRepositoryClaim, error) {
	if repositoryID <= 0 || userID == "" {
		return GitHubRepositoryClaim{}, githubInvalid("GITHUB_REPOSITORY_CLAIM_INVALID", "repository and claiming user are required")
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return GitHubRepositoryClaim{}, githubPostgresError(err)
	}
	defer tx.Rollback()
	project, err := githubProject(ctx, tx, projectID)
	if err != nil {
		return GitHubRepositoryClaim{}, githubPostgresError(err)
	}
	repository, installation, err := claimableGitHubRepositoryTx(ctx, tx, projectID, repositoryID)
	if err != nil {
		return GitHubRepositoryClaim{}, githubPostgresError(err)
	}
	claim, err := scanGitHubClaim(tx.QueryRowContext(ctx, githubClaimSelect+` WHERE repository_id=$1`, repositoryID))
	if err == nil && claim.Status == GitHubLinkActive {
		if claim.ProjectID != projectID {
			return GitHubRepositoryClaim{}, githubConflict("GITHUB_REPOSITORY_ALREADY_CLAIMED", "repository is already claimed by another project")
		}
		if err := tx.Commit(); err != nil {
			return GitHubRepositoryClaim{}, githubPostgresError(err)
		}
		return claim, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GitHubRepositoryClaim{}, githubPostgresError(err)
	}
	now := s.clock()
	claim = GitHubRepositoryClaim{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, ProjectID: projectID, ClaimedBy: userID, Status: GitHubLinkActive, ClaimedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO github_repository_claims(repository_id,installation_id,project_id,claimed_by,status,claimed_at,released_at) VALUES($1,$2,$3,$4,'active',$5,NULL) ON CONFLICT (repository_id) DO UPDATE SET installation_id=EXCLUDED.installation_id,project_id=EXCLUDED.project_id,claimed_by=EXCLUDED.claimed_by,status='active',claimed_at=EXCLUDED.claimed_at,released_at=NULL`, repository.RepositoryID, installation.InstallationID, projectID, userID, now)
	if err != nil {
		return GitHubRepositoryClaim{}, githubPostgresError(err)
	}
	if err := insertGitHubAuditTx(ctx, tx, project, userID, "github.repository.claimed", "github_repository", strconv.FormatInt(repositoryID, 10), map[string]any{"installation_id": installation.InstallationID, "repository_id": repositoryID}, now); err != nil {
		return GitHubRepositoryClaim{}, githubPostgresError(err)
	}
	if err := tx.Commit(); err != nil {
		return GitHubRepositoryClaim{}, githubPostgresError(err)
	}
	return claim, nil
}

func (s PostgresService) ReleaseGitHubRepository(projectID string, repositoryID int64, userID string) error {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return githubPostgresError(err)
	}
	defer tx.Rollback()
	project, err := githubProject(ctx, tx, projectID)
	if err != nil {
		return githubPostgresError(err)
	}
	if _, err := scanGitHubRepository(tx.QueryRowContext(ctx, githubRepositorySelect+` WHERE repository_id=$1 FOR UPDATE`, repositoryID)); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return githubPostgresError(err)
	}
	claim, err := scanGitHubClaim(tx.QueryRowContext(ctx, githubClaimSelect+` WHERE repository_id=$1`, repositoryID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && claim.ProjectID != projectID {
		return ErrNotFound
	}
	if err != nil {
		return githubPostgresError(err)
	}
	if claim.Status == GitHubLinkRevoked {
		return githubPostgresError(tx.Commit())
	}
	var activeBindings int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM github_service_bindings WHERE repository_id=$1 AND status='active'`, repositoryID).Scan(&activeBindings); err != nil {
		return githubPostgresError(err)
	}
	if activeBindings != 0 {
		return githubConflict("GITHUB_REPOSITORY_HAS_ACTIVE_BINDINGS", "remove active service bindings before releasing the repository")
	}
	now := s.clock()
	if _, err := tx.ExecContext(ctx, `UPDATE github_repository_claims SET status='revoked',released_at=$2 WHERE repository_id=$1`, repositoryID, now); err != nil {
		return githubPostgresError(err)
	}
	if err := insertGitHubAuditTx(ctx, tx, project, userID, "github.repository.released", "github_repository", strconv.FormatInt(repositoryID, 10), map[string]any{"installation_id": claim.InstallationID, "repository_id": repositoryID}, now); err != nil {
		return githubPostgresError(err)
	}
	return githubPostgresError(tx.Commit())
}

func (s PostgresService) CreateGitHubServiceBinding(projectID string, draft GitHubServiceBindingDraft) (GitHubServiceBinding, error) {
	if err := normalizeGitHubBindingDraft(&draft); err != nil {
		return GitHubServiceBinding{}, err
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	defer tx.Rollback()
	project, err := githubProject(ctx, tx, projectID)
	if err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	var serviceProject, serviceStatus string
	err = tx.QueryRowContext(ctx, `SELECT project_id,status FROM control_services WHERE id=$1 FOR UPDATE`, draft.ServiceID).Scan(&serviceProject, &serviceStatus)
	if errors.Is(err, sql.ErrNoRows) || err == nil && serviceProject != projectID {
		return GitHubServiceBinding{}, ErrNotFound
	}
	if err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	if serviceStatus == "deleted" {
		return GitHubServiceBinding{}, githubConflict("GITHUB_SERVICE_UNAVAILABLE", "service is deleted")
	}
	repository, installation, err := claimableGitHubRepositoryTx(ctx, tx, projectID, draft.RepositoryID)
	if err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	claim, err := scanGitHubClaim(tx.QueryRowContext(ctx, githubClaimSelect+` WHERE repository_id=$1`, draft.RepositoryID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && (claim.ProjectID != projectID || claim.Status != GitHubLinkActive) {
		return GitHubServiceBinding{}, githubConflict("GITHUB_REPOSITORY_NOT_CLAIMED", "repository must be claimed by the project")
	}
	if err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	existing, err := scanGitHubBinding(tx.QueryRowContext(ctx, githubBindingSelect+` WHERE service_id=$1 AND status='active'`, draft.ServiceID))
	if err == nil {
		if existing.RepositoryID == draft.RepositoryID && existing.ServiceKey == draft.ServiceKey && existing.ConfigPath == draft.ConfigPath {
			if err := tx.Commit(); err != nil {
				return GitHubServiceBinding{}, githubPostgresError(err)
			}
			return existing, nil
		}
		return GitHubServiceBinding{}, githubConflict("GITHUB_SERVICE_ALREADY_BOUND", "service already has an active GitHub binding")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	if _, err := scanGitHubBinding(tx.QueryRowContext(ctx, githubBindingSelect+` WHERE repository_id=$1 AND service_key=$2 AND status='active'`, draft.RepositoryID, draft.ServiceKey)); err == nil {
		return GitHubServiceBinding{}, githubConflict("GITHUB_SERVICE_KEY_ALREADY_BOUND", "repository service key already has an active binding")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	now := s.clock()
	binding := GitHubServiceBinding{ID: newID("ghbind"), ProjectID: projectID, ServiceID: draft.ServiceID, RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, ServiceKey: draft.ServiceKey, ConfigPath: draft.ConfigPath, Status: GitHubLinkActive, CreatedBy: draft.CreatedBy, CreatedAt: now, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO github_service_bindings(id,project_id,service_id,repository_id,installation_id,service_key,config_path,status,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'active',$8,$9,$10)`, binding.ID, binding.ProjectID, binding.ServiceID, binding.RepositoryID, binding.InstallationID, binding.ServiceKey, binding.ConfigPath, binding.CreatedBy, binding.CreatedAt, binding.UpdatedAt)
	if err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	if err := insertGitHubAuditTx(ctx, tx, project, draft.CreatedBy, "github.service_binding.created", "github_service_binding", binding.ID, map[string]any{"installation_id": binding.InstallationID, "repository_id": binding.RepositoryID, "service_id": binding.ServiceID, "service_key": binding.ServiceKey}, now); err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	if err := tx.Commit(); err != nil {
		return GitHubServiceBinding{}, githubPostgresError(err)
	}
	return binding, nil
}

func (s PostgresService) RemoveGitHubServiceBinding(projectID, bindingID, userID string) error {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return githubPostgresError(err)
	}
	defer tx.Rollback()
	project, err := githubProject(ctx, tx, projectID)
	if err != nil {
		return githubPostgresError(err)
	}
	binding, err := scanGitHubBinding(tx.QueryRowContext(ctx, githubBindingSelect+` WHERE id=$1 FOR UPDATE`, bindingID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && binding.ProjectID != projectID {
		return ErrNotFound
	}
	if err != nil {
		return githubPostgresError(err)
	}
	if binding.Status == GitHubLinkRevoked {
		return githubPostgresError(tx.Commit())
	}
	now := s.clock()
	if _, err := tx.ExecContext(ctx, `UPDATE github_service_bindings SET status='revoked',updated_at=$2,removed_at=$2 WHERE id=$1`, bindingID, now); err != nil {
		return githubPostgresError(err)
	}
	if err := insertGitHubAuditTx(ctx, tx, project, userID, "github.service_binding.removed", "github_service_binding", binding.ID, map[string]any{"installation_id": binding.InstallationID, "repository_id": binding.RepositoryID, "service_id": binding.ServiceID, "service_key": binding.ServiceKey}, now); err != nil {
		return githubPostgresError(err)
	}
	return githubPostgresError(tx.Commit())
}

func (s PostgresService) ListGitHubServiceBindings(projectID string) ([]GitHubServiceBinding, error) {
	ctx := context.Background()
	if _, err := githubProject(ctx, s.DB, projectID); err != nil {
		return nil, githubPostgresError(err)
	}
	rows, err := s.DB.QueryContext(ctx, githubBindingSelect+` WHERE project_id=$1 ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, githubPostgresError(err)
	}
	defer rows.Close()
	var bindings []GitHubServiceBinding
	for rows.Next() {
		binding, err := scanGitHubBinding(rows)
		if err != nil {
			return nil, githubPostgresError(err)
		}
		bindings = append(bindings, binding)
	}
	return bindings, githubPostgresError(rows.Err())
}

func claimableGitHubRepositoryTx(ctx context.Context, tx githubDBTX, projectID string, repositoryID int64) (GitHubRepository, GitHubInstallation, error) {
	repository, err := scanGitHubRepository(tx.QueryRowContext(ctx, githubRepositorySelect+` WHERE repository_id=$1 FOR UPDATE`, repositoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubRepository{}, GitHubInstallation{}, ErrNotFound
	}
	if err != nil {
		return GitHubRepository{}, GitHubInstallation{}, err
	}
	installation, err := scanGitHubInstallation(tx.QueryRowContext(ctx, githubInstallationSelect+` WHERE installation_id=$1`, repository.InstallationID))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubRepository{}, GitHubInstallation{}, ErrNotFound
	}
	if err != nil {
		return GitHubRepository{}, GitHubInstallation{}, err
	}
	var linked bool
	err = tx.QueryRowContext(ctx, `SELECT true FROM github_installation_project_links WHERE installation_id=$1 AND project_id=$2 AND status='active'`, repository.InstallationID, projectID).Scan(&linked)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubRepository{}, GitHubInstallation{}, githubConflict("GITHUB_INSTALLATION_NOT_LINKED", "repository installation is not linked to the project")
	}
	if err != nil {
		return GitHubRepository{}, GitHubInstallation{}, err
	}
	if installation.Status != GitHubInstallationActive || installation.Suspended {
		return GitHubRepository{}, GitHubInstallation{}, githubConflict("GITHUB_INSTALLATION_UNAVAILABLE", "GitHub installation is not active")
	}
	if repository.Status != GitHubRepositoryActive {
		return GitHubRepository{}, GitHubInstallation{}, githubConflict("GITHUB_REPOSITORY_INACTIVE", "GitHub repository is not active")
	}
	if repository.Archived || repository.Disabled {
		return GitHubRepository{}, GitHubInstallation{}, githubConflict("GITHUB_REPOSITORY_UNAVAILABLE", "archived or disabled repository cannot be used")
	}
	return repository, installation, nil
}

func githubProject(ctx context.Context, db githubDBTX, projectID string) (Project, error) {
	var project Project
	err := db.QueryRowContext(ctx, `SELECT id,org_id FROM projects WHERE id=$1`, projectID).Scan(&project.ID, &project.OrgID)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return project, err
}

func insertGitHubAuditTx(ctx context.Context, tx githubDBTX, project Project, actorUserID, action, resourceType, resourceID string, metadata map[string]any, at time.Time) error {
	actorType := "user"
	if actorUserID == "" {
		actorType = "system"
	}
	data, err := json.Marshal(RedactMap(metadata))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cloud_audit_events(id,org_id,project_id,actor_user_id,actor_type,action,resource_type,resource_id,result,metadata_redacted,created_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,'success',$9,$10)`, newID("aud"), project.OrgID, project.ID, actorUserID, actorType, action, resourceType, resourceID, string(data), at)
	return err
}

func scanGitHubInstallation(row rowScanner) (GitHubInstallation, error) {
	var installation GitHubInstallation
	err := row.Scan(&installation.InstallationID, &installation.AccountID, &installation.AccountLogin, &installation.AccountType, &installation.Status, &installation.Suspended, &installation.CreatedAt, &installation.UpdatedAt)
	return installation, err
}

func scanGitHubRepository(row rowScanner) (GitHubRepository, error) {
	var repository GitHubRepository
	err := row.Scan(&repository.RepositoryID, &repository.InstallationID, &repository.OwnerID, &repository.OwnerLogin, &repository.Name, &repository.FullName, &repository.Private, &repository.Archived, &repository.Disabled, &repository.DefaultBranch, &repository.Status, &repository.CreatedAt, &repository.UpdatedAt)
	return repository, err
}

func scanGitHubLink(row rowScanner) (GitHubInstallationProjectLink, error) {
	var link GitHubInstallationProjectLink
	var revokedAt sql.NullTime
	err := row.Scan(&link.InstallationID, &link.ProjectID, &link.ClaimedBy, &link.Status, &link.ClaimedAt, &revokedAt)
	link.RevokedAt = nullTimePtr(revokedAt)
	return link, err
}

func scanGitHubClaim(row rowScanner) (GitHubRepositoryClaim, error) {
	var claim GitHubRepositoryClaim
	var releasedAt sql.NullTime
	err := row.Scan(&claim.RepositoryID, &claim.InstallationID, &claim.ProjectID, &claim.ClaimedBy, &claim.Status, &claim.ClaimedAt, &releasedAt)
	claim.ReleasedAt = nullTimePtr(releasedAt)
	return claim, err
}

func scanGitHubBinding(row rowScanner) (GitHubServiceBinding, error) {
	var binding GitHubServiceBinding
	var removedAt sql.NullTime
	err := row.Scan(&binding.ID, &binding.ProjectID, &binding.ServiceID, &binding.RepositoryID, &binding.InstallationID, &binding.ServiceKey, &binding.ConfigPath, &binding.Status, &binding.CreatedBy, &binding.CreatedAt, &binding.UpdatedAt, &removedAt)
	binding.RemovedAt = nullTimePtr(removedAt)
	return binding, err
}

func nullableGitHubID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func githubPostgresError(err error) error {
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrGitHubEventConflict) {
		return err
	}
	var apiError APIError
	if errors.As(err, &apiError) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		switch postgresError.ConstraintName {
		case "github_service_bindings_active_service_uidx":
			return githubConflict("GITHUB_SERVICE_ALREADY_BOUND", "service already has an active GitHub binding")
		case "github_service_bindings_active_repository_key_uidx":
			return githubConflict("GITHUB_SERVICE_KEY_ALREADY_BOUND", "repository service key already has an active binding")
		case "github_repository_claims_active_repository_uidx", "github_repository_claims_pkey":
			return githubConflict("GITHUB_REPOSITORY_ALREADY_CLAIMED", "repository is already claimed by another project")
		}
	}
	return APIError{Status: 503, Code: "GITHUB_STORAGE_UNAVAILABLE", Message: "GitHub inventory storage is unavailable"}
}
