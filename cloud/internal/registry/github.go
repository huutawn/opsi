package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
)

const (
	GitHubInstallationActive    = "active"
	GitHubInstallationSuspended = "suspended"
	GitHubInstallationDeleted   = "deleted"

	GitHubRepositoryActive  = "active"
	GitHubRepositoryRemoved = "removed"
	GitHubRepositoryDeleted = "deleted"

	GitHubLinkActive  = "active"
	GitHubLinkRevoked = "revoked"

	DefaultGitHubConfigPath = ".opsi/opsi-cd.yaml"
)

var ErrGitHubEventConflict = errors.New("github event identity conflict")

type GitHubInstallation struct {
	InstallationID int64     `json:"installation_id"`
	AccountID      int64     `json:"account_id"`
	AccountLogin   string    `json:"account_login"`
	AccountType    string    `json:"account_type"`
	Status         string    `json:"status"`
	Suspended      bool      `json:"suspended"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type GitHubRepository struct {
	RepositoryID     int64     `json:"repository_id"`
	InstallationID   int64     `json:"installation_id"`
	OwnerID          int64     `json:"owner_id"`
	OwnerLogin       string    `json:"owner_login"`
	Name             string    `json:"name"`
	FullName         string    `json:"full_name"`
	Private          bool      `json:"private"`
	Archived         bool      `json:"archived"`
	Disabled         bool      `json:"disabled"`
	DefaultBranch    string    `json:"default_branch"`
	Status           string    `json:"status"`
	ClaimStatus      string    `json:"claim_status"`
	ClaimedProjectID string    `json:"claimed_project_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type GitHubInstallationProjectLink struct {
	InstallationID int64      `json:"installation_id"`
	ProjectID      string     `json:"project_id"`
	ClaimedBy      string     `json:"claimed_by"`
	Status         string     `json:"status"`
	ClaimedAt      time.Time  `json:"claimed_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

type GitHubRepositoryClaim struct {
	RepositoryID   int64      `json:"repository_id"`
	InstallationID int64      `json:"installation_id"`
	ProjectID      string     `json:"project_id"`
	ClaimedBy      string     `json:"claimed_by"`
	Status         string     `json:"status"`
	ClaimedAt      time.Time  `json:"claimed_at"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
}

type GitHubServiceBinding struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"project_id"`
	ServiceID      string     `json:"service_id"`
	RepositoryID   int64      `json:"repository_id"`
	InstallationID int64      `json:"installation_id"`
	ServiceKey     string     `json:"service_key"`
	ConfigPath     string     `json:"config_path"`
	Status         string     `json:"status"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	RemovedAt      *time.Time `json:"removed_at,omitempty"`
}

type GitHubServiceBindingDraft struct {
	ServiceID    string `json:"service_id"`
	RepositoryID int64  `json:"repository_id"`
	ServiceKey   string `json:"service_key"`
	ConfigPath   string `json:"config_path"`
	CreatedBy    string `json:"-"`
}

type GitHubWebhookMutation struct {
	DeliveryID     string
	Event          string
	Action         string
	InstallationID int64
	AccountID      int64
	AccountLogin   string
	AccountType    string
	Repository     *GitHubRepository
	Added          []GitHubRepository
	Removed        []GitHubRepository
	ReceivedAt     time.Time
}

type GitHubWebhookDelivery struct {
	DeliveryID     string
	Event          string
	Action         string
	InstallationID int64
	RepositoryID   int64
	ReceivedAt     time.Time
	ProcessedAt    time.Time
}

func (s *Service) UpsertGitHubInstallation(installation GitHubInstallation) (GitHubInstallation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertGitHubInstallationLocked(installation)
}

func (s *Service) upsertGitHubInstallationLocked(installation GitHubInstallation) (GitHubInstallation, error) {
	if err := validateGitHubInstallation(installation); err != nil {
		return GitHubInstallation{}, err
	}
	now := s.clock()
	if installation.CreatedAt.IsZero() {
		installation.CreatedAt = now
	}
	if installation.UpdatedAt.IsZero() {
		installation.UpdatedAt = now
	}
	if current, ok := s.githubInstallations[installation.InstallationID]; ok {
		if current.AccountID != installation.AccountID {
			return GitHubInstallation{}, ErrGitHubEventConflict
		}
		installation.CreatedAt = current.CreatedAt
	}
	s.githubInstallations[installation.InstallationID] = installation
	return installation, nil
}

func (s *Service) UpsertGitHubRepository(repository GitHubRepository) (GitHubRepository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertGitHubRepositoryLocked(repository)
}

func (s *Service) upsertGitHubRepositoryLocked(repository GitHubRepository) (GitHubRepository, error) {
	if err := validateGitHubRepository(repository); err != nil {
		return GitHubRepository{}, err
	}
	if _, ok := s.githubInstallations[repository.InstallationID]; !ok {
		return GitHubRepository{}, ErrNotFound
	}
	now := s.clock()
	if repository.CreatedAt.IsZero() {
		repository.CreatedAt = now
	}
	if repository.UpdatedAt.IsZero() {
		repository.UpdatedAt = now
	}
	if current, ok := s.githubRepositories[repository.RepositoryID]; ok {
		if current.InstallationID != repository.InstallationID {
			return GitHubRepository{}, ErrGitHubEventConflict
		}
		repository.CreatedAt = current.CreatedAt
	}
	s.githubRepositories[repository.RepositoryID] = repository
	return repository, nil
}

func (s *Service) MarkGitHubInstallationStatus(installationID int64, status string, suspended bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if installationID <= 0 || !validGitHubInstallationStatus(status) || (status == GitHubInstallationSuspended) != suspended {
		return githubInvalid("GITHUB_INSTALLATION_INVALID", "installation ID and status are invalid")
	}
	installation, ok := s.githubInstallations[installationID]
	if !ok {
		return ErrNotFound
	}
	installation.Status = status
	installation.Suspended = suspended
	installation.UpdatedAt = s.clock()
	s.githubInstallations[installationID] = installation
	if status == GitHubInstallationDeleted {
		for id, repository := range s.githubRepositories {
			if repository.InstallationID == installationID {
				repository.Status = GitHubRepositoryRemoved
				repository.UpdatedAt = installation.UpdatedAt
				s.githubRepositories[id] = repository
			}
		}
	}
	return nil
}

func (s *Service) MarkGitHubRepositoryStatus(repositoryID int64, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if repositoryID <= 0 || !validGitHubRepositoryStatus(status) {
		return githubInvalid("GITHUB_REPOSITORY_INVALID", "repository ID and status are invalid")
	}
	repository, ok := s.githubRepositories[repositoryID]
	if !ok {
		return ErrNotFound
	}
	repository.Status = status
	repository.UpdatedAt = s.clock()
	s.githubRepositories[repositoryID] = repository
	return nil
}

func (s *Service) RecordGitHubWebhookEvent(_ context.Context, event GitHubWebhookMutation) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.githubWebhookDeliveries[event.DeliveryID]; ok {
		return true, nil
	}
	if err := s.validateGitHubWebhookMutationLocked(event); err != nil {
		return false, err
	}
	processedAt := s.clock()
	receivedAt := event.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = processedAt
	}
	repositoryID := int64(0)
	if event.Repository != nil {
		repositoryID = event.Repository.RepositoryID
	}
	s.githubWebhookDeliveries[event.DeliveryID] = GitHubWebhookDelivery{
		DeliveryID: event.DeliveryID, Event: event.Event, Action: event.Action,
		InstallationID: event.InstallationID, RepositoryID: repositoryID,
		ReceivedAt: receivedAt, ProcessedAt: processedAt,
	}

	s.applyGitHubWebhookMutationLocked(event, receivedAt)
	s.auditGitHubWebhookLocked(event, processedAt)
	return false, nil
}

func (s *Service) validateGitHubWebhookMutationLocked(event GitHubWebhookMutation) error {
	if strings.TrimSpace(event.DeliveryID) == "" || event.InstallationID <= 0 {
		return githubInvalid("GITHUB_WEBHOOK_INVALID", "webhook delivery identity is invalid")
	}
	if event.Event == "installation" {
		installation := GitHubInstallation{InstallationID: event.InstallationID, AccountID: event.AccountID, AccountLogin: event.AccountLogin, AccountType: event.AccountType, Status: GitHubInstallationActive}
		if err := validateGitHubInstallation(installation); err != nil {
			return err
		}
		if current, ok := s.githubInstallations[event.InstallationID]; ok && current.AccountID != event.AccountID {
			return ErrGitHubEventConflict
		}
		return nil
	}
	if _, ok := s.githubInstallations[event.InstallationID]; !ok {
		return ErrNotFound
	}
	seen := map[int64]struct{}{}
	validateRepository := func(repository GitHubRepository) error {
		repository.InstallationID = event.InstallationID
		if err := validateGitHubRepository(repository); err != nil {
			return err
		}
		if _, duplicate := seen[repository.RepositoryID]; duplicate {
			return githubInvalid("GITHUB_WEBHOOK_INVALID", "webhook contains duplicate repository identity")
		}
		seen[repository.RepositoryID] = struct{}{}
		if current, ok := s.githubRepositories[repository.RepositoryID]; ok && current.InstallationID != event.InstallationID {
			return ErrGitHubEventConflict
		}
		return nil
	}
	if event.Repository != nil {
		if err := validateRepository(*event.Repository); err != nil {
			return err
		}
	}
	for _, repository := range event.Added {
		if repository.RepositoryID <= 0 {
			return githubInvalid("GITHUB_REPOSITORY_INVALID", "GitHub repository identity is invalid")
		}
		if _, duplicate := seen[repository.RepositoryID]; duplicate {
			return githubInvalid("GITHUB_WEBHOOK_INVALID", "webhook contains duplicate repository identity")
		}
		seen[repository.RepositoryID] = struct{}{}
		current, ok := s.githubRepositories[repository.RepositoryID]
		if !ok {
			return ErrNotFound
		}
		if current.InstallationID != event.InstallationID {
			return ErrGitHubEventConflict
		}
	}
	for _, repository := range event.Removed {
		if repository.RepositoryID <= 0 {
			return githubInvalid("GITHUB_REPOSITORY_INVALID", "GitHub repository identity is invalid")
		}
		if _, duplicate := seen[repository.RepositoryID]; duplicate {
			return githubInvalid("GITHUB_WEBHOOK_INVALID", "webhook contains duplicate repository identity")
		}
		seen[repository.RepositoryID] = struct{}{}
		if current, ok := s.githubRepositories[repository.RepositoryID]; ok {
			if current.InstallationID != event.InstallationID {
				return ErrGitHubEventConflict
			}
			continue
		}
		repository.InstallationID = event.InstallationID
		repository.Status = GitHubRepositoryRemoved
		if err := validateGitHubRepository(repository); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyGitHubWebhookMutationLocked(event GitHubWebhookMutation, at time.Time) {
	switch event.Event {
	case "installation":
		status, suspended := GitHubInstallationActive, false
		if event.Action == "new_permissions_accepted" {
			if current, ok := s.githubInstallations[event.InstallationID]; ok {
				status, suspended = current.Status, current.Suspended
			}
		} else {
			switch event.Action {
			case "created", "unsuspend":
			case "deleted":
				status = GitHubInstallationDeleted
			case "suspend":
				status, suspended = GitHubInstallationSuspended, true
			}
		}
		installation := GitHubInstallation{InstallationID: event.InstallationID, AccountID: event.AccountID, AccountLogin: event.AccountLogin, AccountType: event.AccountType, Status: status, Suspended: suspended, CreatedAt: at, UpdatedAt: at}
		if current, ok := s.githubInstallations[event.InstallationID]; ok {
			installation.CreatedAt = current.CreatedAt
		}
		s.githubInstallations[event.InstallationID] = installation
		if status == GitHubInstallationDeleted {
			for id, repository := range s.githubRepositories {
				if repository.InstallationID == event.InstallationID {
					repository.Status = GitHubRepositoryRemoved
					repository.UpdatedAt = at
					s.githubRepositories[id] = repository
				}
			}
		}
	case "installation_repositories":
		for _, repository := range event.Added {
			current := s.githubRepositories[repository.RepositoryID]
			current.Status = GitHubRepositoryActive
			current.UpdatedAt = at
			s.githubRepositories[repository.RepositoryID] = current
		}
		for _, repository := range event.Removed {
			if current, ok := s.githubRepositories[repository.RepositoryID]; ok {
				current.Status = GitHubRepositoryRemoved
				current.UpdatedAt = at
				s.githubRepositories[repository.RepositoryID] = current
			}
		}
	case "repository":
		status := GitHubRepositoryActive
		if event.Action == "deleted" {
			status = GitHubRepositoryDeleted
		}
		s.upsertWebhookRepositoryLocked(*event.Repository, event.InstallationID, status, at)
	}
}

func (s *Service) upsertWebhookRepositoryLocked(repository GitHubRepository, installationID int64, status string, at time.Time) {
	repository.InstallationID = installationID
	repository.Status = status
	repository.CreatedAt = at
	repository.UpdatedAt = at
	if current, ok := s.githubRepositories[repository.RepositoryID]; ok {
		repository.CreatedAt = current.CreatedAt
	}
	s.githubRepositories[repository.RepositoryID] = repository
}

func (s *Service) ListGitHubInstallations(projectID string) ([]GitHubInstallation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	installations := make([]GitHubInstallation, 0)
	for _, link := range s.githubInstallationLinks {
		if link.ProjectID == projectID && link.Status == GitHubLinkActive {
			if installation, ok := s.githubInstallations[link.InstallationID]; ok {
				installations = append(installations, installation)
			}
		}
	}
	sort.Slice(installations, func(i, j int) bool { return installations[i].InstallationID < installations[j].InstallationID })
	return installations, nil
}

func (s *Service) ListGitHubRepositories(projectID string) ([]GitHubRepository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	linked := make(map[int64]struct{})
	for _, link := range s.githubInstallationLinks {
		if link.ProjectID == projectID && link.Status == GitHubLinkActive {
			linked[link.InstallationID] = struct{}{}
		}
	}
	repositories := make([]GitHubRepository, 0)
	for _, repository := range s.githubRepositories {
		if _, ok := linked[repository.InstallationID]; ok {
			repository.ClaimStatus = "available"
			if claim, exists := s.githubRepositoryClaims[repository.RepositoryID]; exists && claim.Status == GitHubLinkActive {
				if claim.ProjectID == projectID {
					repository.ClaimStatus = GitHubLinkActive
					repository.ClaimedProjectID = projectID
				} else {
					repository.ClaimStatus = "conflict"
				}
			}
			repositories = append(repositories, repository)
		}
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].RepositoryID < repositories[j].RepositoryID })
	return repositories, nil
}

func (s *Service) ClaimGitHubInstallation(projectID string, installationID int64, userID string) (GitHubInstallationProjectLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok {
		return GitHubInstallationProjectLink{}, ErrNotFound
	}
	installation, ok := s.githubInstallations[installationID]
	if !ok {
		return GitHubInstallationProjectLink{}, ErrNotFound
	}
	if userID == "" || installation.Status != GitHubInstallationActive || installation.Suspended {
		return GitHubInstallationProjectLink{}, githubConflict("GITHUB_INSTALLATION_UNAVAILABLE", "GitHub installation is not active")
	}
	key := githubInstallationLinkKey(installationID, projectID)
	if link, ok := s.githubInstallationLinks[key]; ok && link.Status == GitHubLinkActive {
		return link, nil
	}
	now := s.clock()
	link := GitHubInstallationProjectLink{InstallationID: installationID, ProjectID: projectID, ClaimedBy: userID, Status: GitHubLinkActive, ClaimedAt: now}
	s.githubInstallationLinks[key] = link
	s.appendGitHubAuditLocked(project, userID, "github.installation.claimed", "github_installation", strconv.FormatInt(installationID, 10), map[string]any{"installation_id": installationID}, now)
	return link, nil
}

func (s *Service) ClaimGitHubRepository(projectID string, repositoryID int64, userID string) (GitHubRepositoryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok {
		return GitHubRepositoryClaim{}, ErrNotFound
	}
	repository, installation, err := s.claimableRepositoryLocked(projectID, repositoryID)
	if err != nil {
		return GitHubRepositoryClaim{}, err
	}
	if userID == "" {
		return GitHubRepositoryClaim{}, githubInvalid("GITHUB_CLAIM_INVALID", "claiming user is required")
	}
	if current, ok := s.githubRepositoryClaims[repositoryID]; ok && current.Status == GitHubLinkActive {
		if current.ProjectID == projectID {
			return current, nil
		}
		return GitHubRepositoryClaim{}, githubConflict("GITHUB_REPOSITORY_ALREADY_CLAIMED", "repository is already claimed by another project")
	}
	now := s.clock()
	claim := GitHubRepositoryClaim{RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, ProjectID: projectID, ClaimedBy: userID, Status: GitHubLinkActive, ClaimedAt: now}
	s.githubRepositoryClaims[repositoryID] = claim
	s.appendGitHubAuditLocked(project, userID, "github.repository.claimed", "github_repository", strconv.FormatInt(repositoryID, 10), map[string]any{"installation_id": installation.InstallationID, "repository_id": repositoryID}, now)
	return claim, nil
}

func (s *Service) ReleaseGitHubRepository(projectID string, repositoryID int64, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok {
		return ErrNotFound
	}
	claim, ok := s.githubRepositoryClaims[repositoryID]
	if !ok || claim.ProjectID != projectID {
		return ErrNotFound
	}
	if claim.Status == GitHubLinkRevoked {
		return nil
	}
	for _, binding := range s.githubServiceBindings {
		if binding.RepositoryID == repositoryID && binding.Status == GitHubLinkActive {
			return githubConflict("GITHUB_REPOSITORY_HAS_ACTIVE_BINDINGS", "remove active service bindings before releasing the repository")
		}
	}
	now := s.clock()
	claim.Status = GitHubLinkRevoked
	claim.ReleasedAt = &now
	s.githubRepositoryClaims[repositoryID] = claim
	s.appendGitHubAuditLocked(project, userID, "github.repository.released", "github_repository", strconv.FormatInt(repositoryID, 10), map[string]any{"installation_id": claim.InstallationID, "repository_id": repositoryID}, now)
	return nil
}

func (s *Service) CreateGitHubServiceBinding(projectID string, draft GitHubServiceBindingDraft) (GitHubServiceBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok {
		return GitHubServiceBinding{}, ErrNotFound
	}
	service, ok := s.services[draft.ServiceID]
	if !ok || service.ProjectID != projectID {
		return GitHubServiceBinding{}, ErrNotFound
	}
	if service.Status == "deleted" {
		return GitHubServiceBinding{}, githubConflict("GITHUB_SERVICE_UNAVAILABLE", "service is deleted")
	}
	repository, installation, err := s.claimableRepositoryLocked(projectID, draft.RepositoryID)
	if err != nil {
		return GitHubServiceBinding{}, err
	}
	claim, ok := s.githubRepositoryClaims[draft.RepositoryID]
	if !ok || claim.Status != GitHubLinkActive || claim.ProjectID != projectID {
		return GitHubServiceBinding{}, githubConflict("GITHUB_REPOSITORY_NOT_CLAIMED", "repository must be claimed by the project")
	}
	if err := normalizeGitHubBindingDraft(&draft); err != nil {
		return GitHubServiceBinding{}, err
	}
	for _, binding := range s.githubServiceBindings {
		if binding.Status != GitHubLinkActive {
			continue
		}
		if binding.ServiceID == draft.ServiceID {
			if binding.RepositoryID == draft.RepositoryID && binding.ServiceKey == draft.ServiceKey && binding.ConfigPath == draft.ConfigPath {
				return binding, nil
			}
			return GitHubServiceBinding{}, githubConflict("GITHUB_SERVICE_ALREADY_BOUND", "service already has an active GitHub binding")
		}
		if binding.RepositoryID == draft.RepositoryID && binding.ServiceKey == draft.ServiceKey {
			return GitHubServiceBinding{}, githubConflict("GITHUB_SERVICE_KEY_ALREADY_BOUND", "repository service key already has an active binding")
		}
	}
	now := s.clock()
	binding := GitHubServiceBinding{ID: newID("ghbind"), ProjectID: projectID, ServiceID: service.ID, RepositoryID: repository.RepositoryID, InstallationID: installation.InstallationID, ServiceKey: draft.ServiceKey, ConfigPath: draft.ConfigPath, Status: GitHubLinkActive, CreatedBy: draft.CreatedBy, CreatedAt: now, UpdatedAt: now}
	s.githubServiceBindings[binding.ID] = binding
	s.appendGitHubAuditLocked(project, draft.CreatedBy, "github.service_binding.created", "github_service_binding", binding.ID, map[string]any{"installation_id": installation.InstallationID, "repository_id": repository.RepositoryID, "service_id": service.ID, "service_key": binding.ServiceKey}, now)
	return binding, nil
}

func (s *Service) RemoveGitHubServiceBinding(projectID, bindingID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok {
		return ErrNotFound
	}
	binding, ok := s.githubServiceBindings[bindingID]
	if !ok || binding.ProjectID != projectID {
		return ErrNotFound
	}
	if binding.Status == GitHubLinkRevoked {
		return nil
	}
	now := s.clock()
	binding.Status = GitHubLinkRevoked
	binding.UpdatedAt = now
	binding.RemovedAt = &now
	s.githubServiceBindings[bindingID] = binding
	s.appendGitHubAuditLocked(project, userID, "github.service_binding.removed", "github_service_binding", binding.ID, map[string]any{"installation_id": binding.InstallationID, "repository_id": binding.RepositoryID, "service_id": binding.ServiceID, "service_key": binding.ServiceKey}, now)
	return nil
}

func (s *Service) ListGitHubServiceBindings(projectID string) ([]GitHubServiceBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	bindings := make([]GitHubServiceBinding, 0)
	for _, binding := range s.githubServiceBindings {
		if binding.ProjectID == projectID {
			bindings = append(bindings, binding)
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].CreatedAt.Before(bindings[j].CreatedAt) })
	return bindings, nil
}

func (s *Service) ResolveBuildBinding(_ context.Context, repositoryID uint64, serviceKey string) (buildrecord.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if repositoryID == 0 || !validGitHubServiceKey(serviceKey) {
		return buildrecord.Binding{}, ErrNotFound
	}
	repository, ok := s.githubRepositories[int64(repositoryID)]
	if !ok || repository.Status != GitHubRepositoryActive || repository.Archived || repository.Disabled {
		return buildrecord.Binding{}, ErrNotFound
	}
	installation, ok := s.githubInstallations[repository.InstallationID]
	if !ok || installation.Status != GitHubInstallationActive || installation.Suspended {
		return buildrecord.Binding{}, ErrNotFound
	}
	claim, ok := s.githubRepositoryClaims[repository.RepositoryID]
	if !ok || claim.Status != GitHubLinkActive {
		return buildrecord.Binding{}, ErrNotFound
	}
	for _, binding := range s.githubServiceBindings {
		if binding.Status != GitHubLinkActive || binding.RepositoryID != repository.RepositoryID || binding.ServiceKey != serviceKey || binding.ProjectID != claim.ProjectID {
			continue
		}
		service, exists := s.services[binding.ServiceID]
		if !exists || service.ProjectID != binding.ProjectID || service.Status == "deleted" {
			return buildrecord.Binding{}, ErrNotFound
		}
		return buildrecord.Binding{ProjectID: binding.ProjectID, BindingID: binding.ID, ServiceID: binding.ServiceID, ServiceKey: binding.ServiceKey, RepositoryID: repositoryID, RepositoryOwnerID: uint64(repository.OwnerID), RepositoryFullName: repository.FullName}, nil
	}
	return buildrecord.Binding{}, ErrNotFound
}

func (s *Service) claimableRepositoryLocked(projectID string, repositoryID int64) (GitHubRepository, GitHubInstallation, error) {
	repository, ok := s.githubRepositories[repositoryID]
	if !ok {
		return GitHubRepository{}, GitHubInstallation{}, ErrNotFound
	}
	installation, ok := s.githubInstallations[repository.InstallationID]
	if !ok {
		return GitHubRepository{}, GitHubInstallation{}, ErrNotFound
	}
	link, ok := s.githubInstallationLinks[githubInstallationLinkKey(repository.InstallationID, projectID)]
	if !ok || link.Status != GitHubLinkActive {
		return GitHubRepository{}, GitHubInstallation{}, githubConflict("GITHUB_INSTALLATION_NOT_LINKED", "repository installation is not linked to the project")
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

func (s *Service) auditGitHubWebhookLocked(event GitHubWebhookMutation, at time.Time) {
	for _, link := range s.githubInstallationLinks {
		if link.InstallationID != event.InstallationID || link.Status != GitHubLinkActive {
			continue
		}
		project, ok := s.projects[link.ProjectID]
		if !ok {
			continue
		}
		metadata := map[string]any{"installation_id": event.InstallationID, "delivery_id": event.DeliveryID, "event": event.Event, "action": event.Action}
		if event.Repository != nil {
			metadata["repository_id"] = event.Repository.RepositoryID
		}
		s.appendGitHubAuditLocked(project, "", "github.webhook.processed", "github_webhook_delivery", event.DeliveryID, metadata, at)
	}
}

func (s *Service) appendGitHubAuditLocked(project Project, actorUserID, action, resourceType, resourceID string, metadata map[string]any, at time.Time) {
	actorType := "user"
	if actorUserID == "" {
		actorType = "system"
	}
	s.audit = append(s.audit, AuditEvent{ID: newID("aud"), OrgID: project.OrgID, ProjectID: project.ID, ActorUserID: actorUserID, ActorType: actorType, Action: action, ResourceType: resourceType, ResourceID: resourceID, Result: "success", MetadataRedacted: RedactMap(metadata), CreatedAt: at})
}

func validateGitHubInstallation(installation GitHubInstallation) error {
	if installation.InstallationID <= 0 || installation.AccountID <= 0 || !validGitHubMetadata(installation.AccountLogin, true) || !validGitHubMetadata(installation.AccountType, true) || !validGitHubInstallationStatus(installation.Status) {
		return githubInvalid("GITHUB_INSTALLATION_INVALID", "GitHub installation metadata is invalid")
	}
	if (installation.Status == GitHubInstallationSuspended) != installation.Suspended {
		return githubInvalid("GITHUB_INSTALLATION_INVALID", "installation status and suspended flag disagree")
	}
	return nil
}

func validateGitHubRepository(repository GitHubRepository) error {
	if repository.RepositoryID <= 0 || repository.InstallationID <= 0 || repository.OwnerID <= 0 || !validGitHubMetadata(repository.OwnerLogin, true) || !validGitHubMetadata(repository.Name, true) || !validGitHubMetadata(repository.FullName, true) || !validGitHubMetadata(repository.DefaultBranch, false) || !validGitHubRepositoryStatus(repository.Status) {
		return githubInvalid("GITHUB_REPOSITORY_INVALID", "GitHub repository metadata is invalid")
	}
	return nil
}

func normalizeGitHubBindingDraft(draft *GitHubServiceBindingDraft) error {
	if draft.CreatedBy == "" || draft.ServiceID == "" || draft.RepositoryID <= 0 || !validGitHubServiceKey(draft.ServiceKey) {
		return githubInvalid("GITHUB_BINDING_INVALID", "service, repository, creator, and valid service_key are required")
	}
	if draft.ConfigPath == "" {
		draft.ConfigPath = DefaultGitHubConfigPath
	}
	if !validGitHubConfigPath(draft.ConfigPath) {
		return githubInvalid("GITHUB_CONFIG_PATH_INVALID", "config_path must be a safe relative slash-separated path")
	}
	return nil
}

func validGitHubInstallationStatus(status string) bool {
	return status == GitHubInstallationActive || status == GitHubInstallationSuspended || status == GitHubInstallationDeleted
}

func validGitHubRepositoryStatus(status string) bool {
	return status == GitHubRepositoryActive || status == GitHubRepositoryRemoved || status == GitHubRepositoryDeleted
}

func validGitHubMetadata(value string, required bool) bool {
	if required && value == "" {
		return false
	}
	if len(value) > 1024 {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func validGitHubServiceKey(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		alphanumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if !alphanumeric && character != '-' && character != '_' {
			return false
		}
		if (index == 0 || index == len(value)-1) && !alphanumeric {
			return false
		}
	}
	return true
}

func validGitHubConfigPath(value string) bool {
	if value == "" || len(value) > 256 || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func githubInstallationLinkKey(installationID int64, projectID string) string {
	return fmt.Sprintf("%d:%s", installationID, projectID)
}

func githubInvalid(code, message string) APIError {
	return APIError{Status: 400, Code: code, Message: message}
}

func githubConflict(code, message string) APIError {
	return APIError{Status: 409, Code: code, Message: message}
}
