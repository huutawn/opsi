package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresGitHubInventoryClaimsBindingsAndDurableDeliveries(t *testing.T) {
	db, service, firstProject, secondProject, firstService, secondService := newGitHubPostgresFixture(t)
	installation := testGitHubInstallation(11001)
	storedInstallation, err := service.UpsertGitHubInstallation(installation)
	if err != nil {
		t.Fatal(err)
	}
	installation.AccountLogin = "renamed-owner"
	updatedInstallation, err := service.UpsertGitHubInstallation(installation)
	if err != nil || !updatedInstallation.CreatedAt.Equal(storedInstallation.CreatedAt) {
		t.Fatalf("installation=%+v err=%v", updatedInstallation, err)
	}
	for _, project := range []Project{firstProject, secondProject} {
		if _, err := service.ClaimGitHubInstallation(project.ID, installation.InstallationID, project.CreatedBy); err != nil {
			t.Fatal(err)
		}
	}
	repository := testGitHubRepository(12001, installation.InstallationID)
	storedRepository, err := service.UpsertGitHubRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	repository.Name, repository.FullName = "renamed", "renamed-owner/renamed"
	renamedRepository, err := service.UpsertGitHubRepository(repository)
	if err != nil || renamedRepository.RepositoryID != storedRepository.RepositoryID || !renamedRepository.CreatedAt.Equal(storedRepository.CreatedAt) {
		t.Fatalf("repository=%+v err=%v", renamedRepository, err)
	}
	claim, err := service.ClaimGitHubRepository(firstProject.ID, repository.RepositoryID, firstProject.CreatedBy)
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := service.ClaimGitHubRepository(firstProject.ID, repository.RepositoryID, firstProject.CreatedBy); err != nil || repeated.RepositoryID != claim.RepositoryID || repeated.ProjectID != claim.ProjectID || !repeated.ClaimedAt.Equal(claim.ClaimedAt) {
		t.Fatalf("repeated claim=%+v err=%v", repeated, err)
	}
	if _, err := service.ClaimGitHubRepository(secondProject.ID, repository.RepositoryID, secondProject.CreatedBy); !hasGitHubCode(err, "GITHUB_REPOSITORY_ALREADY_CLAIMED") {
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
	binding, err := service.CreateGitHubServiceBinding(firstProject.ID, GitHubServiceBindingDraft{ServiceID: firstService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", CreatedBy: firstProject.CreatedBy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateGitHubServiceBinding(firstProject.ID, GitHubServiceBindingDraft{ServiceID: secondService.ID, RepositoryID: repository.RepositoryID, ServiceKey: "api", CreatedBy: firstProject.CreatedBy}); !hasGitHubCode(err, "GITHUB_SERVICE_KEY_ALREADY_BOUND") {
		t.Fatalf("repository key uniqueness err=%v", err)
	}
	secondRepository := testGitHubRepository(12002, installation.InstallationID)
	if _, err := service.UpsertGitHubRepository(secondRepository); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimGitHubRepository(firstProject.ID, secondRepository.RepositoryID, firstProject.CreatedBy); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateGitHubServiceBinding(firstProject.ID, GitHubServiceBindingDraft{ServiceID: firstService.ID, RepositoryID: secondRepository.RepositoryID, ServiceKey: "worker", CreatedBy: firstProject.CreatedBy}); !hasGitHubCode(err, "GITHUB_SERVICE_ALREADY_BOUND") {
		t.Fatalf("service uniqueness err=%v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE github_service_bindings SET config_path='/absolute' WHERE id=$1`, binding.ID); err == nil {
		t.Fatal("absolute config path passed database constraint")
	}
	if err := service.MarkGitHubRepositoryStatus(repository.RepositoryID, GitHubRepositoryRemoved); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkGitHubInstallationStatus(installation.InstallationID, GitHubInstallationDeleted, false); err != nil {
		t.Fatal(err)
	}
	var bindingCount, claimCount int
	if err := db.QueryRow(`SELECT count(*) FROM github_service_bindings WHERE id=$1`, binding.ID).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM github_repository_claims WHERE repository_id=$1`, repository.RepositoryID).Scan(&claimCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 || claimCount != 1 {
		t.Fatalf("history binding=%d claim=%d", bindingCount, claimCount)
	}
	if err := service.RemoveGitHubServiceBinding(firstProject.ID, binding.ID, firstProject.CreatedBy); err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseGitHubRepository(firstProject.ID, repository.RepositoryID, firstProject.CreatedBy); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkGitHubInstallationStatus(installation.InstallationID, GitHubInstallationActive, false); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkGitHubRepositoryStatus(repository.RepositoryID, GitHubRepositoryActive); err != nil {
		t.Fatal(err)
	}
	if reclaimed, err := service.ClaimGitHubRepository(secondProject.ID, repository.RepositoryID, secondProject.CreatedBy); err != nil || reclaimed.ProjectID != secondProject.ID {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}

	concurrentInstallation := testGitHubInstallation(11002)
	if _, err := service.UpsertGitHubInstallation(concurrentInstallation); err != nil {
		t.Fatal(err)
	}
	for _, project := range []Project{firstProject, secondProject} {
		if _, err := service.ClaimGitHubInstallation(project.ID, concurrentInstallation.InstallationID, project.CreatedBy); err != nil {
			t.Fatal(err)
		}
	}
	concurrentRepository := testGitHubRepository(12003, concurrentInstallation.InstallationID)
	if _, err := service.UpsertGitHubRepository(concurrentRepository); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	claimErrors := make(chan error, 2)
	for _, project := range []Project{firstProject, secondProject} {
		wait.Add(1)
		go func(project Project) {
			defer wait.Done()
			_, err := service.ClaimGitHubRepository(project.ID, concurrentRepository.RepositoryID, project.CreatedBy)
			claimErrors <- err
		}(project)
	}
	wait.Wait()
	close(claimErrors)
	winners, conflicts := 0, 0
	for err := range claimErrors {
		if err == nil {
			winners++
		} else if hasGitHubCode(err, "GITHUB_REPOSITORY_ALREADY_CLAIMED") {
			conflicts++
		} else {
			t.Fatalf("concurrent claim err=%v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}

	durableEvent := GitHubWebhookMutation{DeliveryID: "postgres-durable", Event: "installation", Action: "created", InstallationID: 13001, AccountID: 14001, AccountLogin: "durable", AccountType: "User", ReceivedAt: time.Now().UTC()}
	if duplicate, err := service.RecordGitHubWebhookEvent(context.Background(), durableEvent); err != nil || duplicate {
		t.Fatalf("first durable duplicate=%v err=%v", duplicate, err)
	}
	recreated := PostgresService{DB: db}
	if duplicate, err := recreated.RecordGitHubWebhookEvent(context.Background(), durableEvent); err != nil || !duplicate {
		t.Fatalf("recreated duplicate=%v err=%v", duplicate, err)
	}
	partialInstallation := GitHubWebhookMutation{DeliveryID: "postgres-partial-install", Event: "installation", Action: "created", InstallationID: 13003, AccountID: 14003, AccountLogin: "partial", AccountType: "Organization", ReceivedAt: time.Now().UTC()}
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), partialInstallation); err != nil {
		t.Fatal(err)
	}
	partialRepository := testGitHubRepository(15003, partialInstallation.InstallationID)
	partialRepository.Status = GitHubRepositoryRemoved
	if _, err := service.UpsertGitHubRepository(partialRepository); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), GitHubWebhookMutation{DeliveryID: "postgres-partial-added", Event: "installation_repositories", Action: "added", InstallationID: partialInstallation.InstallationID, Added: []GitHubRepository{{RepositoryID: partialRepository.RepositoryID}}, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var partialName, partialBranch, partialAddedStatus string
	if err := db.QueryRow(`SELECT name,default_branch,status FROM github_repositories WHERE repository_id=$1`, partialRepository.RepositoryID).Scan(&partialName, &partialBranch, &partialAddedStatus); err != nil || partialName != partialRepository.Name || partialBranch != partialRepository.DefaultBranch || partialAddedStatus != GitHubRepositoryActive {
		t.Fatalf("partial added name=%q branch=%q status=%q err=%v", partialName, partialBranch, partialAddedStatus, err)
	}
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), GitHubWebhookMutation{DeliveryID: "postgres-partial-removed", Event: "installation_repositories", Action: "removed", InstallationID: partialInstallation.InstallationID, Removed: []GitHubRepository{{RepositoryID: partialRepository.RepositoryID}}, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var partialStatus string
	if err := db.QueryRow(`SELECT status FROM github_repositories WHERE repository_id=$1`, partialRepository.RepositoryID).Scan(&partialStatus); err != nil || partialStatus != GitHubRepositoryRemoved {
		t.Fatalf("partial removed status=%q err=%v", partialStatus, err)
	}
	failed := GitHubWebhookMutation{DeliveryID: "postgres-failed", Event: "repository", Action: "created", InstallationID: 99999, Repository: &GitHubRepository{RepositoryID: 15001, InstallationID: 99999, OwnerID: 1, OwnerLogin: "x", Name: "x", FullName: "x/x", DefaultBranch: "main", Status: GitHubRepositoryActive}, ReceivedAt: time.Now().UTC()}
	if _, err := service.RecordGitHubWebhookEvent(context.Background(), failed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed mutation err=%v", err)
	}
	var failedDeliveryCount int
	if err := db.QueryRow(`SELECT count(*) FROM github_webhook_deliveries WHERE delivery_id=$1`, failed.DeliveryID).Scan(&failedDeliveryCount); err != nil || failedDeliveryCount != 0 {
		t.Fatalf("failed delivery count=%d err=%v", failedDeliveryCount, err)
	}
	concurrentEvent := GitHubWebhookMutation{DeliveryID: "postgres-concurrent-delivery", Event: "installation", Action: "created", InstallationID: 13002, AccountID: 14002, AccountLogin: "concurrent", AccountType: "User", ReceivedAt: time.Now().UTC()}
	deliveryResults := make(chan bool, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			duplicate, err := service.RecordGitHubWebhookEvent(context.Background(), concurrentEvent)
			if err != nil {
				t.Errorf("concurrent delivery: %v", err)
			}
			deliveryResults <- duplicate
		}()
	}
	wait.Wait()
	close(deliveryResults)
	duplicates := 0
	for duplicate := range deliveryResults {
		if duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("durable duplicate results=%d", duplicates)
	}

	var sensitiveColumns int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='github_webhook_deliveries' AND column_name ~ '(body|secret|token|signature)'`).Scan(&sensitiveColumns); err != nil || sensitiveColumns != 0 {
		t.Fatalf("sensitive delivery columns=%d err=%v", sensitiveColumns, err)
	}
	if _, err := db.Exec(`INSERT INTO github_repositories(repository_id,installation_id,owner_id,owner_login,name,full_name,private,archived,disabled,default_branch,status,created_at,updated_at) VALUES(0,$1,1,'x','x','x/x',false,false,false,'main','active',now(),now())`, concurrentInstallation.InstallationID); err == nil {
		t.Fatal("non-positive repository ID passed database constraint")
	}
	otherInstallation := testGitHubInstallation(11003)
	if _, err := service.UpsertGitHubInstallation(otherInstallation); err != nil {
		t.Fatal(err)
	}
	foreignKeyRepository := testGitHubRepository(12004, installation.InstallationID)
	if _, err := service.UpsertGitHubRepository(foreignKeyRepository); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO github_repository_claims(repository_id,installation_id,project_id,claimed_by,status,claimed_at) VALUES($1,$2,$3,$4,'active',now())`, foreignKeyRepository.RepositoryID, otherInstallation.InstallationID, firstProject.ID, firstProject.CreatedBy); err == nil {
		t.Fatal("repository claim installation mismatch passed foreign key")
	}
	crossProjectService, err := service.CreateService(secondProject.ID, ServiceDraft{Name: "cross-project"}, "cross-service")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO github_service_bindings(id,project_id,service_id,repository_id,installation_id,service_key,config_path,status,created_by,created_at,updated_at) VALUES('mismatch',$1,$2,$3,$4,'mismatch','.opsi/opsi-cd.yaml','active',$5,now(),now())`, firstProject.ID, crossProjectService.ID, secondRepository.RepositoryID, installation.InstallationID, firstProject.CreatedBy); err == nil {
		t.Fatal("service project mismatch passed foreign key")
	}
	installations, err := service.ListGitHubInstallations(firstProject.ID)
	if err != nil || len(installations) < 2 {
		t.Fatalf("installation round trip=%+v err=%v", installations, err)
	}
	bindings, err := service.ListGitHubServiceBindings(firstProject.ID)
	if err != nil || len(bindings) != 1 || bindings[0].ID != binding.ID {
		t.Fatalf("binding round trip=%+v err=%v", bindings, err)
	}
}

func newGitHubPostgresFixture(t *testing.T) (*sql.DB, PostgresService, Project, Project, ServiceRecord, ServiceRecord) {
	t.Helper()
	dsn := requirePostgresTestDSN(t, "GitHub inventory")
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("p09_registry_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = admin.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email) VALUES('user-1','user-1@example.test'),('user-2','user-2@example.test')`,
		`INSERT INTO organizations(id,name,slug) VALUES('org-1','Org','org')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service := PostgresService{DB: db, Now: func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) }}
	firstProject, err := service.CreateProject("org-1", "First", "first", "user-1", "first-project")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, err := service.CreateProject("org-1", "Second", "second", "user-2", "second-project")
	if err != nil {
		t.Fatal(err)
	}
	firstService, err := service.CreateService(firstProject.ID, ServiceDraft{Name: "api"}, "first-service")
	if err != nil {
		t.Fatal(err)
	}
	secondService, err := service.CreateService(firstProject.ID, ServiceDraft{Name: "worker"}, "second-service")
	if err != nil {
		t.Fatal(err)
	}
	return db, service, firstProject, secondProject, firstService, secondService
}
