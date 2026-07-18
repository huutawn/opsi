package registry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGitHubInventoryClaimAndBindingParityBehavior(t *testing.T) {
	service, firstProject, secondProject, firstService, secondService := newGitHubMemoryFixture(t)
	installation := testGitHubInstallation(101)
	storedInstallation, err := service.UpsertGitHubInstallation(installation)
	if err != nil {
		t.Fatal(err)
	}
	installation.AccountLogin = "renamed-owner"
	updatedInstallation, err := service.UpsertGitHubInstallation(installation)
	if err != nil || updatedInstallation.CreatedAt != storedInstallation.CreatedAt || updatedInstallation.AccountLogin != "renamed-owner" {
		t.Fatalf("installation upsert=%+v err=%v", updatedInstallation, err)
	}
	repository := testGitHubRepository(201, installation.InstallationID)
	storedRepository, err := service.UpsertGitHubRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	repository.Name, repository.FullName = "renamed", "renamed-owner/renamed"
	renamedRepository, err := service.UpsertGitHubRepository(repository)
	if err != nil || renamedRepository.RepositoryID != storedRepository.RepositoryID || renamedRepository.CreatedAt != storedRepository.CreatedAt {
		t.Fatalf("repository rename=%+v err=%v", renamedRepository, err)
	}
	if _, err := service.ClaimGitHubInstallation(firstProject.ID, installation.InstallationID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimGitHubInstallation(secondProject.ID, installation.InstallationID, "user-2"); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimGitHubRepository(firstProject.ID, repository.RepositoryID, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := service.ClaimGitHubRepository(firstProject.ID, repository.RepositoryID, "user-1"); err != nil || repeated != claim {
		t.Fatalf("idempotent claim=%+v err=%v", repeated, err)
	}
	if _, err := service.ClaimGitHubRepository(secondProject.ID, repository.RepositoryID, "user-2"); !hasGitHubCode(err, "GITHUB_REPOSITORY_ALREADY_CLAIMED") {
		t.Fatalf("cross-project claim err=%v", err)
	}
	firstInventory, err := service.ListGitHubRepositories(firstProject.ID)
	if err != nil || len(firstInventory) != 1 || firstInventory[0].ClaimStatus != GitHubLinkActive || firstInventory[0].ClaimedProjectID != firstProject.ID {
		t.Fatalf("first project claim inventory=%+v err=%v", firstInventory, err)
	}
	secondInventory, err := service.ListGitHubRepositories(secondProject.ID)
	if err != nil || len(secondInventory) != 1 || secondInventory[0].ClaimStatus != "conflict" || secondInventory[0].ClaimedProjectID != "" {
		t.Fatalf("cross-project conflict inventory=%+v err=%v", secondInventory, err)
	}
	if _, err := service.CreateGitHubServiceBinding(firstProject.ID, GitHubServiceBindingDraft{ServiceID: secondService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", CreatedBy: "user-1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project service err=%v", err)
	}
	binding, err := service.CreateGitHubServiceBinding(firstProject.ID, GitHubServiceBindingDraft{ServiceID: firstService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", CreatedBy: "user-1"})
	if err != nil || binding.ConfigPath != DefaultGitHubConfigPath {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if repeated, err := service.CreateGitHubServiceBinding(firstProject.ID, GitHubServiceBindingDraft{ServiceID: firstService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", CreatedBy: "user-1"}); err != nil || repeated.ID != binding.ID {
		t.Fatalf("idempotent binding=%+v err=%v", repeated, err)
	}
	if err := service.ReleaseGitHubRepository(firstProject.ID, repository.RepositoryID, "user-1"); !hasGitHubCode(err, "GITHUB_REPOSITORY_HAS_ACTIVE_BINDINGS") {
		t.Fatalf("release with binding err=%v", err)
	}
	if err := service.MarkGitHubRepositoryStatus(repository.RepositoryID, GitHubRepositoryRemoved); err != nil {
		t.Fatal(err)
	}
	bindings, err := service.ListGitHubServiceBindings(firstProject.ID)
	if err != nil || len(bindings) != 1 || bindings[0].Status != GitHubLinkActive {
		t.Fatalf("removed repository lost binding: %+v err=%v", bindings, err)
	}
	if err := service.MarkGitHubInstallationStatus(installation.InstallationID, GitHubInstallationDeleted, false); err != nil {
		t.Fatal(err)
	}
	bindings, _ = service.ListGitHubServiceBindings(firstProject.ID)
	if len(bindings) != 1 || service.githubRepositoryClaims[repository.RepositoryID].Status != GitHubLinkActive {
		t.Fatalf("deleted installation lost history: bindings=%+v claim=%+v", bindings, service.githubRepositoryClaims[repository.RepositoryID])
	}
	if err := service.RemoveGitHubServiceBinding(firstProject.ID, binding.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveGitHubServiceBinding(firstProject.ID, binding.ID, "user-1"); err != nil {
		t.Fatalf("idempotent binding removal: %v", err)
	}
	if err := service.ReleaseGitHubRepository(firstProject.ID, repository.RepositoryID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if service.githubRepositoryClaims[repository.RepositoryID].Status != GitHubLinkRevoked {
		t.Fatalf("claim=%+v", service.githubRepositoryClaims[repository.RepositoryID])
	}
	firstInventory, err = service.ListGitHubRepositories(firstProject.ID)
	if err != nil || len(firstInventory) != 1 || firstInventory[0].ClaimStatus != "available" || firstInventory[0].ClaimedProjectID != "" {
		t.Fatalf("released inventory=%+v err=%v", firstInventory, err)
	}
	if err := service.MarkGitHubInstallationStatus(installation.InstallationID, GitHubInstallationActive, false); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkGitHubRepositoryStatus(repository.RepositoryID, GitHubRepositoryActive); err != nil {
		t.Fatal(err)
	}
	if claim, err := service.ClaimGitHubRepository(secondProject.ID, repository.RepositoryID, "user-2"); err != nil || claim.ProjectID != secondProject.ID {
		t.Fatalf("reclaim=%+v err=%v", claim, err)
	}
	bindings, _ = service.ListGitHubServiceBindings(firstProject.ID)
	if len(bindings) != 1 || bindings[0].Status != GitHubLinkRevoked {
		t.Fatalf("reclaim lost binding history: %+v", bindings)
	}
	audits, err := service.ListAudit(firstProject.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"github.installation.claimed", "github.repository.claimed", "github.repository.released", "github.service_binding.created", "github.service_binding.removed"} {
		if !hasGitHubAuditAction(audits, action) {
			t.Fatalf("missing audit action %s: %+v", action, audits)
		}
	}
}

func TestGitHubBindingValidationAndUniqueness(t *testing.T) {
	service, project, _, firstService, _ := newGitHubMemoryFixture(t)
	secondService, err := service.CreateService(project.ID, ServiceDraft{Name: "worker"}, "service-same-project")
	if err != nil {
		t.Fatal(err)
	}
	installation := testGitHubInstallation(301)
	repository := testGitHubRepository(401, installation.InstallationID)
	if _, err := service.UpsertGitHubInstallation(installation); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpsertGitHubRepository(repository); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimGitHubInstallation(project.ID, installation.InstallationID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimGitHubRepository(project.ID, repository.RepositoryID, "user-1"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "Upper", "-api", "api-", "api.key", "api key"} {
		if _, err := service.CreateGitHubServiceBinding(project.ID, GitHubServiceBindingDraft{ServiceID: firstService.ID, RepositoryID: repository.RepositoryID, ServiceKey: key, CreatedBy: "user-1"}); !hasGitHubCode(err, "GITHUB_BINDING_INVALID") {
			t.Fatalf("service key %q err=%v", key, err)
		}
	}
	for _, configPath := range []string{"/absolute", "../opsi.yaml", "a//b", "a\\b", "a/./b", "a\x00b"} {
		if _, err := service.CreateGitHubServiceBinding(project.ID, GitHubServiceBindingDraft{ServiceID: firstService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", ConfigPath: configPath, CreatedBy: "user-1"}); !hasGitHubCode(err, "GITHUB_CONFIG_PATH_INVALID") {
			t.Fatalf("config path %q err=%v", configPath, err)
		}
	}
	if _, err := service.CreateGitHubServiceBinding(project.ID, GitHubServiceBindingDraft{ServiceID: firstService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", ConfigPath: "services/api/opsi.yaml", CreatedBy: "user-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateGitHubServiceBinding(project.ID, GitHubServiceBindingDraft{ServiceID: secondService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", CreatedBy: "user-1"}); !hasGitHubCode(err, "GITHUB_SERVICE_KEY_ALREADY_BOUND") {
		t.Fatalf("duplicate repository key err=%v", err)
	}
	archived := testGitHubRepository(402, installation.InstallationID)
	archived.Archived = true
	if _, err := service.UpsertGitHubRepository(archived); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimGitHubRepository(project.ID, archived.RepositoryID, "user-1"); !hasGitHubCode(err, "GITHUB_REPOSITORY_UNAVAILABLE") {
		t.Fatalf("archived claim err=%v", err)
	}
}

func TestGitHubWebhookMutationIsAtomicDurableAndIdentitySafeInMemory(t *testing.T) {
	service := NewService()
	created := GitHubWebhookMutation{DeliveryID: "delivery-created", Event: "installation", Action: "created", InstallationID: 501, AccountID: 601, AccountLogin: "octo", AccountType: "Organization", ReceivedAt: time.Now().UTC()}
	duplicate, err := service.RecordGitHubWebhookEvent(context.Background(), created)
	if err != nil || duplicate {
		t.Fatalf("created duplicate=%v err=%v", duplicate, err)
	}
	project, err := service.CreateProject("org", "project", "project", "user", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimGitHubInstallation(project.ID, 501, "user"); err != nil {
		t.Fatal(err)
	}
	created.AccountLogin = "must-not-mutate"
	duplicate, err = service.RecordGitHubWebhookEvent(context.Background(), created)
	if err != nil || !duplicate || service.githubInstallations[501].AccountLogin != "octo" {
		t.Fatalf("duplicate=%v installation=%+v err=%v", duplicate, service.githubInstallations[501], err)
	}
	added := testGitHubRepository(701, 501)
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), GitHubWebhookMutation{DeliveryID: "delivery-added", Event: "installation_repositories", Action: "added", InstallationID: 501, Added: []GitHubRepository{added}, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	renamed := added
	renamed.Name, renamed.FullName = "renamed", "octo/renamed"
	renamed.OwnerID, renamed.OwnerLogin = 602, "new-owner"
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), GitHubWebhookMutation{DeliveryID: "delivery-renamed", Event: "repository", Action: "transferred", InstallationID: 501, Repository: &renamed, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if got := service.githubRepositories[701]; got.Name != "renamed" || got.OwnerID != 602 || got.RepositoryID != 701 {
		t.Fatalf("renamed repository=%+v", got)
	}
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), GitHubWebhookMutation{DeliveryID: "delivery-removed", Event: "installation_repositories", Action: "removed", InstallationID: 501, Removed: []GitHubRepository{renamed}, ReceivedAt: time.Now().UTC()}); err != nil || service.githubRepositories[701].Status != GitHubRepositoryRemoved {
		t.Fatalf("remove err=%v repository=%+v", err, service.githubRepositories[701])
	}
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), GitHubWebhookMutation{DeliveryID: "delivery-deleted-repo", Event: "repository", Action: "deleted", InstallationID: 501, Repository: &renamed, ReceivedAt: time.Now().UTC()}); err != nil || service.githubRepositories[701].Status != GitHubRepositoryDeleted {
		t.Fatalf("delete repo err=%v repository=%+v", err, service.githubRepositories[701])
	}
	for _, change := range []struct {
		delivery  string
		action    string
		status    string
		suspended bool
	}{{"delivery-suspend", "suspend", GitHubInstallationSuspended, true}, {"delivery-permissions", "new_permissions_accepted", GitHubInstallationSuspended, true}, {"delivery-unsuspend", "unsuspend", GitHubInstallationActive, false}, {"delivery-delete-install", "deleted", GitHubInstallationDeleted, false}} {
		mutation := created
		mutation.DeliveryID, mutation.Action, mutation.AccountLogin = change.delivery, change.action, "octo"
		if _, err := service.RecordGitHubWebhookEvent(context.Background(), mutation); err != nil {
			t.Fatal(err)
		}
		installation := service.githubInstallations[501]
		if installation.Status != change.status || installation.Suspended != change.suspended {
			t.Fatalf("installation=%+v", installation)
		}
	}
	conflictingInstallation := testGitHubInstallation(999)
	if _, err := service.UpsertGitHubInstallation(conflictingInstallation); err != nil {
		t.Fatal(err)
	}
	conflictRepository := testGitHubRepository(701, 999)
	failed := GitHubWebhookMutation{DeliveryID: "delivery-conflict", Event: "repository", Action: "edited", InstallationID: 999, Repository: &conflictRepository, ReceivedAt: time.Now().UTC()}
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), failed); !errors.Is(err, ErrGitHubEventConflict) {
		t.Fatal("conflicting repository installation was accepted")
	}
	if _, ok := service.githubWebhookDeliveries[failed.DeliveryID]; ok {
		t.Fatal("failed transaction recorded delivery")
	}

	concurrent := GitHubWebhookMutation{DeliveryID: "delivery-concurrent", Event: "installation", Action: "created", InstallationID: 502, AccountID: 603, AccountLogin: "concurrent", AccountType: "User", ReceivedAt: time.Now().UTC()}
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			duplicate, err := service.RecordGitHubWebhookEvent(context.Background(), concurrent)
			if err != nil {
				t.Errorf("concurrent delivery: %v", err)
			}
			results <- duplicate
		}()
	}
	wait.Wait()
	close(results)
	duplicates := 0
	for duplicate := range results {
		if duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicate results=%d", duplicates)
	}
	audits, err := service.ListAudit(project.ID)
	if err != nil || !hasGitHubAuditAction(audits, "github.webhook.processed") {
		t.Fatalf("webhook audit=%+v err=%v", audits, err)
	}
}

func TestGitHubConcurrentRepositoryClaimHasOneWinner(t *testing.T) {
	service, firstProject, secondProject, _, _ := newGitHubMemoryFixture(t)
	installation := testGitHubInstallation(801)
	repository := testGitHubRepository(901, installation.InstallationID)
	_, _ = service.UpsertGitHubInstallation(installation)
	_, _ = service.UpsertGitHubRepository(repository)
	_, _ = service.ClaimGitHubInstallation(firstProject.ID, installation.InstallationID, "user-1")
	_, _ = service.ClaimGitHubInstallation(secondProject.ID, installation.InstallationID, "user-2")
	projects := []string{firstProject.ID, secondProject.ID}
	var wait sync.WaitGroup
	errorsByProject := make(chan error, 2)
	for index, projectID := range projects {
		wait.Add(1)
		go func(projectID, userID string) {
			defer wait.Done()
			_, err := service.ClaimGitHubRepository(projectID, repository.RepositoryID, userID)
			errorsByProject <- err
		}(projectID, "user-"+string(rune('1'+index)))
	}
	wait.Wait()
	close(errorsByProject)
	winners, conflicts := 0, 0
	for err := range errorsByProject {
		switch {
		case err == nil:
			winners++
		case hasGitHubCode(err, "GITHUB_REPOSITORY_ALREADY_CLAIMED"):
			conflicts++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
}

func newGitHubMemoryFixture(t *testing.T) (*Service, Project, Project, ServiceRecord, ServiceRecord) {
	t.Helper()
	service := NewService()
	service.now = func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) }
	firstProject, err := service.CreateProject("org-1", "first", "first", "user-1", "project-1")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, err := service.CreateProject("org-1", "second", "second", "user-2", "project-2")
	if err != nil {
		t.Fatal(err)
	}
	firstService, err := service.CreateService(firstProject.ID, ServiceDraft{Name: "api"}, "service-1")
	if err != nil {
		t.Fatal(err)
	}
	secondService, err := service.CreateService(secondProject.ID, ServiceDraft{Name: "worker"}, "service-2")
	if err != nil {
		t.Fatal(err)
	}
	return service, firstProject, secondProject, firstService, secondService
}

func testGitHubInstallation(id int64) GitHubInstallation {
	now := time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)
	return GitHubInstallation{InstallationID: id, AccountID: id + 1000, AccountLogin: "octo", AccountType: "Organization", Status: GitHubInstallationActive, CreatedAt: now, UpdatedAt: now}
}

func testGitHubRepository(id, installationID int64) GitHubRepository {
	now := time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)
	return GitHubRepository{RepositoryID: id, InstallationID: installationID, OwnerID: 42, OwnerLogin: "octo", Name: "repo", FullName: "octo/repo", DefaultBranch: "main", Status: GitHubRepositoryActive, CreatedAt: now, UpdatedAt: now}
}

func hasGitHubCode(err error, code string) bool {
	var apiError APIError
	return errors.As(err, &apiError) && apiError.Code == code
}

func hasGitHubAuditAction(events []AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}
