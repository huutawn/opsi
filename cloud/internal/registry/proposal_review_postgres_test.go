package registry

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresProposalReviewSurvivesRestartAndAppliesOnce(t *testing.T) {
	db, err := sql.Open("pgx", requirePostgresTestDSN(t, "proposal review durability"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("reviewpg"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Proposal Review", "proposal-review-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	project, err := fresh().CreateProject(orgID, "Proposal Review", "proposal-review-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	application, err := fresh().CreateService(project.ID, ServiceDraft{Name: "api", ContainerPort: 8080}, "service-key")
	if err != nil {
		t.Fatal(err)
	}
	created, err := fresh().CreateProposalReview(project.ID, application.ID, userID, ProposalReviewCreateRequest{
		EnvironmentID: application.EnvironmentID, Kind: ProposalReviewServiceConfiguration, AnalysisInputsHash: strings.Repeat("a", 64), ConfigurationDraft: &ServiceConfigurationDraft{},
	})
	if err != nil || created.Status != ReviewRequired {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if reloaded, err := fresh().GetProposalReview(project.ID, created.ID); err != nil || reloaded.Status != ReviewRequired || reloaded.ReviewedPayloadHash != created.ReviewedPayloadHash {
		t.Fatalf("review required did not survive restart: review=%+v err=%v", reloaded, err)
	}
	approved, err := fresh().ApproveProposalReview(project.ID, created.ID, "human-approver")
	if err != nil || approved.Status != ReviewApproved || approved.ApprovedBy != "human-approver" {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	applied, result, err := fresh().ApplyProposalReview(project.ID, created.ID, "human-approver")
	if err != nil || applied.Status != ReviewApplied || result.Configuration.Revision != 1 {
		t.Fatalf("applied=%+v result=%+v err=%v", applied, result, err)
	}
	replayed, replay, err := fresh().ApplyProposalReview(project.ID, created.ID, "human-approver")
	if err != nil || replayed.Status != ReviewApplied || !replay.Reused {
		t.Fatalf("replayed=%+v result=%+v err=%v", replayed, replay, err)
	}
	if configuration, err := fresh().GetServiceConfiguration(project.ID, application.ID); err != nil || configuration.Revision != 1 {
		t.Fatalf("configuration=%+v err=%v", configuration, err)
	}
	if deployments, err := fresh().ListDeployments(project.ID); err != nil || len(deployments) != 0 {
		t.Fatalf("proposal review created deployments=%+v err=%v", deployments, err)
	}
}
