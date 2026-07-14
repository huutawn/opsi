package webhookrelay

import (
	"context"
	"errors"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type RegistryGitHubAppEventSink struct {
	Registry registry.API
}

func (s RegistryGitHubAppEventSink) HandleGitHubAppEvent(ctx context.Context, event GitHubAppEvent) error {
	if s.Registry == nil {
		return ErrGitHubEventSinkUnavailable
	}
	mutation := registry.GitHubWebhookMutation{
		DeliveryID:     event.DeliveryID,
		Event:          event.Event,
		Action:         event.Action,
		InstallationID: event.InstallationID,
		AccountID:      event.AccountID,
		AccountLogin:   event.AccountLogin,
		AccountType:    event.AccountType,
		ReceivedAt:     event.ReceivedAt,
	}
	if event.Repository != nil {
		repository := registryRepository(event.InstallationID, *event.Repository)
		mutation.Repository = &repository
	}
	mutation.Added = make([]registry.GitHubRepository, 0, len(event.Added))
	for _, repository := range event.Added {
		mutation.Added = append(mutation.Added, registryRepository(event.InstallationID, repository))
	}
	mutation.Removed = make([]registry.GitHubRepository, 0, len(event.Removed))
	for _, repository := range event.Removed {
		mutation.Removed = append(mutation.Removed, registryRepository(event.InstallationID, repository))
	}
	duplicate, err := s.Registry.RecordGitHubWebhookEvent(ctx, mutation)
	switch {
	case errors.Is(err, registry.ErrGitHubEventConflict):
		return ErrGitHubEventConflict
	case err == nil && duplicate:
		return ErrGitHubEventDuplicate
	case err == nil:
		return nil
	}
	var apiError registry.APIError
	if errors.As(err, &apiError) && apiError.Status == 503 {
		return ErrGitHubEventSinkUnavailable
	}
	return err
}

func registryRepository(installationID int64, repository GitHubRepository) registry.GitHubRepository {
	return registry.GitHubRepository{
		RepositoryID:   repository.ID,
		InstallationID: installationID,
		OwnerID:        repository.OwnerID,
		OwnerLogin:     repository.OwnerLogin,
		Name:           repository.Name,
		FullName:       repository.FullName,
		Private:        repository.Private,
		Archived:       repository.Archived,
		Disabled:       repository.Disabled,
		DefaultBranch:  repository.DefaultBranch,
		Status:         registry.GitHubRepositoryActive,
	}
}
