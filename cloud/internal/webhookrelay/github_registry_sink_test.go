package webhookrelay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

func TestRegistryGitHubAppEventSinkPersistsAndDeduplicatesAcrossServerRecreation(t *testing.T) {
	secret := strings.Repeat("d", 32)
	sharedRegistry := registry.NewService()
	firstServer := newGitHubAppWebhookServer(secret)
	firstServer.Registry = sharedRegistry
	firstServer.SetGitHubAppEventSink(RegistryGitHubAppEventSink{Registry: sharedRegistry})
	request := githubAppWebhookRequest(http.MethodPost, "installation", "durable-delivery", installationPayload("created"), secret)
	response := serveGitHubAppWebhook(firstServer, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}

	secondServer := newGitHubAppWebhookServer(secret)
	secondServer.Registry = sharedRegistry
	secondServer.SetGitHubAppEventSink(RegistryGitHubAppEventSink{Registry: sharedRegistry})
	response = serveGitHubAppWebhook(secondServer, githubAppWebhookRequest(http.MethodPost, "installation", "durable-delivery", installationPayload("created"), secret))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate status=%d body=%s", response.Code, response.Body.String())
	}

	project, err := sharedRegistry.CreateProject("org", "project", "project", "user", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedRegistry.ClaimGitHubInstallation(project.ID, 101, "user"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		delivery string
		action   string
	}{{"sink-suspend", "suspend"}, {"sink-unsuspend", "unsuspend"}} {
		response = serveGitHubAppWebhook(secondServer, githubAppWebhookRequest(http.MethodPost, "installation", change.delivery, installationPayload(change.action), secret))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", change.action, response.Code, response.Body.String())
		}
	}
	if _, err := sharedRegistry.UpsertGitHubRepository(registry.GitHubRepository{RepositoryID: 301, InstallationID: 101, OwnerID: 202, OwnerLogin: "example", Name: "api", FullName: "example/api", DefaultBranch: "main", Status: registry.GitHubRepositoryRemoved}); err != nil {
		t.Fatal(err)
	}
	addedBody := []byte(`{"action":"added","installation":{"id":101},"repositories_added":[{"id":301,"node_id":"R_kgDOExample","name":"api","full_name":"example/api","private":false}],"repositories_removed":[]}`)
	response = serveGitHubAppWebhook(secondServer, githubAppWebhookRequest(http.MethodPost, "installation_repositories", "sink-added", addedBody, secret))
	if response.Code != http.StatusOK {
		t.Fatalf("added status=%d body=%s", response.Code, response.Body.String())
	}
	removedBody := []byte(`{"action":"removed","installation":{"id":101},"repositories_added":[],"repositories_removed":[{"id":301,"name":"api","full_name":"example/api"}]}`)
	response = serveGitHubAppWebhook(secondServer, githubAppWebhookRequest(http.MethodPost, "installation_repositories", "sink-removed", removedBody, secret))
	if response.Code != http.StatusOK {
		t.Fatalf("removed status=%d body=%s", response.Code, response.Body.String())
	}
	installations, err := sharedRegistry.ListGitHubInstallations(project.ID)
	if err != nil || len(installations) != 1 || installations[0].InstallationID != 101 {
		t.Fatalf("installations=%+v err=%v", installations, err)
	}
	repositories, err := sharedRegistry.ListGitHubRepositories(project.ID)
	if err != nil || len(repositories) != 1 || repositories[0].Status != registry.GitHubRepositoryRemoved {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
}

func TestRegistryGitHubAppEventSinkMapsIdentityConflictAndUnavailableStorage(t *testing.T) {
	service := registry.NewService()
	sink := RegistryGitHubAppEventSink{Registry: service}
	created := GitHubAppEvent{DeliveryID: "install-1", Event: "installation", Action: "created", InstallationID: 11, AccountID: 12, AccountLogin: "octo", AccountType: "Organization"}
	if err := sink.HandleGitHubAppEvent(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	repository := GitHubRepository{ID: 21, OwnerID: 12, OwnerLogin: "octo", Name: "repo", FullName: "octo/repo", DefaultBranch: "main"}
	if err := sink.HandleGitHubAppEvent(context.Background(), GitHubAppEvent{DeliveryID: "repo-1", Event: "repository", Action: "created", InstallationID: 11, Repository: &repository}); err != nil {
		t.Fatal(err)
	}
	otherInstallation := created
	otherInstallation.DeliveryID, otherInstallation.InstallationID, otherInstallation.AccountID = "install-2", 13, 14
	if err := sink.HandleGitHubAppEvent(context.Background(), otherInstallation); err != nil {
		t.Fatal(err)
	}
	if err := sink.HandleGitHubAppEvent(context.Background(), GitHubAppEvent{DeliveryID: "repo-conflict", Event: "repository", Action: "edited", InstallationID: 13, Repository: &repository}); !errors.Is(err, ErrGitHubEventConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	if err := (RegistryGitHubAppEventSink{}).HandleGitHubAppEvent(context.Background(), created); !errors.Is(err, ErrGitHubEventSinkUnavailable) {
		t.Fatalf("unavailable err=%v", err)
	}
}

func TestPostgresRegistryGitHubAppEventSinkDeduplicatesAfterServerRecreation(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run durable GitHub sink test")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run durable GitHub sink test")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("p09_sink_%d", time.Now().UnixNano())
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
	t.Cleanup(func() { _ = db.Close() })
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("p", 32)
	first := newGitHubAppWebhookServer(secret)
	first.Registry = registry.PostgresService{DB: db}
	first.SetGitHubAppEventSink(RegistryGitHubAppEventSink{Registry: first.Registry})
	response := serveGitHubAppWebhook(first, githubAppWebhookRequest(http.MethodPost, "installation", "postgres-server-restart", installationPayload("created"), secret))
	if response.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	second := newGitHubAppWebhookServer(secret)
	second.Registry = registry.PostgresService{DB: db}
	second.SetGitHubAppEventSink(RegistryGitHubAppEventSink{Registry: second.Registry})
	response = serveGitHubAppWebhook(second, githubAppWebhookRequest(http.MethodPost, "installation", "postgres-server-restart", installationPayload("created"), secret))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"duplicate":true`) {
		t.Fatalf("second status=%d body=%s", response.Code, response.Body.String())
	}
	var deliveries int
	if err := db.QueryRow(`SELECT count(*) FROM github_webhook_deliveries WHERE delivery_id='postgres-server-restart'`).Scan(&deliveries); err != nil || deliveries != 1 {
		t.Fatalf("deliveries=%d err=%v", deliveries, err)
	}
}
