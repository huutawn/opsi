package registry

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func proposalReviewFixture(t *testing.T) (*Service, Project, ServiceRecord) {
	t.Helper()
	service := NewService()
	project, err := service.CreateProject("org-review", "Review", "review", "human-1", "project")
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateService(project.ID, ServiceDraft{Name: "api", ContainerPort: 8080}, "application")
	if err != nil {
		t.Fatal(err)
	}
	return service, project, application
}

func createDependencyReview(t *testing.T, service *Service, project Project, application ServiceRecord) ProposalReview {
	t.Helper()
	value, err := service.CreateProposalReview(project.ID, application.ID, "human-1", ProposalReviewCreateRequest{EnvironmentID: application.EnvironmentID, Kind: ProposalReviewServiceConfiguration, AnalysisInputsHash: strings.Repeat("a", 64), ConfigurationDraft: &ServiceConfigurationDraft{}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestProposalReviewLifecycleBindsCanonicalPayloadAndReplays(t *testing.T) {
	service, project, application := proposalReviewFixture(t)
	created := createDependencyReview(t, service, project, application)
	if created.Status != ReviewRequired || created.ReviewedPayloadHash == "" {
		t.Fatalf("created=%+v", created)
	}
	approved, err := service.ApproveProposalReview(project.ID, created.ID, "human-2")
	if err != nil || approved.Status != ReviewApproved || approved.ApprovedBy != "human-2" {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	applied, result, err := service.ApplyProposalReview(project.ID, created.ID, "human-2")
	if err != nil || applied.Status != ReviewApplied || result.Configuration.Revision != 1 {
		t.Fatalf("applied=%+v result=%+v err=%v", applied, result, err)
	}
	replayed, replay, err := service.ApplyProposalReview(project.ID, created.ID, "human-2")
	if err != nil || replayed.Status != ReviewApplied || !replay.Reused {
		t.Fatalf("replayed=%+v result=%+v err=%v", replayed, replay, err)
	}
	configuration, err := service.GetServiceConfiguration(project.ID, application.ID)
	if err != nil || configuration.Revision != 1 {
		t.Fatalf("configuration=%+v err=%v", configuration, err)
	}
}

func TestProposalReviewRejectAndStaleCannotApply(t *testing.T) {
	service, project, application := proposalReviewFixture(t)
	rejected := createDependencyReview(t, service, project, application)
	if _, err := service.RejectProposalReview(project.ID, rejected.ID, "human-2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ApplyProposalReview(project.ID, rejected.ID, "human-2"); err == nil {
		t.Fatal("rejected review applied")
	}
	stale := createDependencyReview(t, service, project, application)
	if _, err := service.ApproveProposalReview(project.ID, stale.ID, "human-2"); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetServiceConfiguration(project.ID, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyServiceConfiguration(project.ID, application.ID, "manual", "manual-change", ServiceConfigurationApplyRequest{Draft: ServiceConfigurationDraft{}, ExpectedRevision: current.Revision, ExpectedStateHash: current.StateHash}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ApplyProposalReview(project.ID, stale.ID, "human-2"); err == nil {
		t.Fatal("stale review applied")
	}
	value, err := service.GetProposalReview(project.ID, stale.ID)
	if err != nil || value.Status != ReviewStale {
		t.Fatalf("review=%+v err=%v", value, err)
	}
}

func TestProposalReviewTamperedStoredPayloadFailsClosed(t *testing.T) {
	service, project, application := proposalReviewFixture(t)
	value := createDependencyReview(t, service, project, application)
	if _, err := service.ApproveProposalReview(project.ID, value.ID, "human-2"); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	altered := service.proposalReviews[value.ID]
	altered.NormalizedPayload = []byte(`{"draft":{"schema_version":"opsi.service_configuration/v1","environment":[{"name":"TAMPER","value":"1"}]}}`)
	service.proposalReviews[value.ID] = altered
	service.mu.Unlock()
	if _, _, err := service.ApplyProposalReview(project.ID, value.ID, "human-2"); err == nil {
		t.Fatal("tampered review applied")
	}
	configuration, err := service.GetServiceConfiguration(project.ID, application.ID)
	if err != nil || configuration.Revision != 0 {
		t.Fatalf("configuration=%+v err=%v", configuration, err)
	}
}

func TestProposalReviewConcurrentApplyMutatesCanonicalConfigurationOnce(t *testing.T) {
	service, project, application := proposalReviewFixture(t)
	first := createDependencyReview(t, service, project, application)
	second := createDependencyReview(t, service, project, application)
	for _, review := range []ProposalReview{first, second} {
		if _, err := service.ApproveProposalReview(project.ID, review.ID, "human-2"); err != nil {
			t.Fatal(err)
		}
	}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, review := range []ProposalReview{first, second} {
		group.Add(1)
		go func(reviewID string) {
			defer group.Done()
			_, _, err := service.ApplyProposalReview(project.ID, reviewID, "human-2")
			errs <- err
		}(review.ID)
	}
	group.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent applies succeeded %d times", successes)
	}
	configuration, err := service.GetServiceConfiguration(project.ID, application.ID)
	if err != nil || configuration.Revision != 1 {
		t.Fatalf("configuration=%+v err=%v", configuration, err)
	}
	for _, reviewID := range []string{first.ID, second.ID} {
		review, err := service.GetProposalReview(project.ID, reviewID)
		if err != nil || (review.Status != ReviewApplied && review.Status != ReviewStale) {
			t.Fatalf("review=%+v err=%v", review, err)
		}
	}
}

func TestProposalReviewExpiryAndSourceReviewCannotWrite(t *testing.T) {
	service, project, application := proposalReviewFixture(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	dependency := createDependencyReview(t, service, project, application)
	now = now.Add(proposalReviewLifetime + time.Second)
	if _, err := service.ApproveProposalReview(project.ID, dependency.ID, "human-2"); err == nil {
		t.Fatal("expired review was approved")
	}
	expired, err := service.GetProposalReview(project.ID, dependency.ID)
	if err != nil || expired.Status != ReviewExpired {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
	if _, err := service.CreateProposalReview(project.ID, application.ID, "human-1", ProposalReviewCreateRequest{EnvironmentID: application.EnvironmentID, Kind: ProposalReviewSourcePatch, AnalysisInputsHash: strings.Repeat("b", 64)}); err == nil {
		t.Fatal("source patch review must be local-only")
	}
	configuration, err := service.GetServiceConfiguration(project.ID, application.ID)
	if err != nil || configuration.Revision != 0 {
		t.Fatalf("source review changed configuration=%+v err=%v", configuration, err)
	}
}

func TestSourceProposalReviewIsRejectedBeforePersistence(t *testing.T) {
	service, project, application := proposalReviewFixture(t)
	secrets := []string{"github_pat_abcdefghijklmnopqrstuvwxyz123456", "agent_token=agent-secret", "postgresql://opsi:postgres-secret@db.example/opsi", "valkey_password=valkey-secret", "registry_credential=registry-secret"}
	if len(secrets) == 0 {
		t.Fatal("test fixture is empty")
	}
	if _, err := service.CreateProposalReview(project.ID, application.ID, "human-1", ProposalReviewCreateRequest{EnvironmentID: application.EnvironmentID, Kind: ProposalReviewSourcePatch, AnalysisInputsHash: strings.Repeat("c", 64)}); err == nil {
		t.Fatal("source patch must not be persisted in Cloud")
	}
}
