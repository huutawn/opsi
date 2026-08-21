package verificationstore

import (
	"context"
	"testing"
	"time"

	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

func TestMemoryStore(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	run := verificationv1.VerificationRun{
		ProjectID:             "proj-1",
		EnvironmentID:         "env-1",
		ConsumerApplicationID: "app-api",
		DependencyLogicalName: "database",
		DeploymentJobID:       "dep-job-100",
		ConfigRevision:        1,
		StalenessHash:         "stale-hash-1",
		ProviderHealth: verificationv1.ProviderHealthLayer{
			Status:       verificationv1.LayerStatusHealthy,
			ProviderKind: "postgres",
			ProviderID:   "res-pg",
		},
		ContractResolution: verificationv1.ContractResolutionLayer{
			Status:            verificationv1.LayerStatusResolved,
			BindingID:         "bind-1",
			Protocol:          "postgres",
			InjectionComplete: true,
		},
		Connection: verificationv1.ConnectionLayer{
			Status:    verificationv1.LayerStatusVerified,
			Protocol:  "postgres",
			LatencyMs: 12,
		},
		ConsumerHealth: verificationv1.ConsumerHealthLayer{
			Status:    verificationv1.LayerStatusHealthy,
			ReadyPods: 1,
			TotalPods: 1,
		},
		ConsumerAssertion: verificationv1.ConsumerAssertionLayer{
			Status:        verificationv1.LayerStatusVerified,
			AssertionPath: "/health/dependencies/database",
			StatusCode:    200,
			ExpectedCode:  200,
		},
		OverallStatus: verificationv1.RunStatusVerified,
		TriggeredBy:   "user-1",
		StartedAt:     time.Now().UTC(),
	}

	created, err := store.Create(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("expected generated ID")
	}

	// Get by ID
	fetched, err := store.Get(ctx, "proj-1", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.OverallStatus != verificationv1.RunStatusVerified {
		t.Fatalf("expected VERIFIED, got %s", fetched.OverallStatus)
	}

	// Get Latest
	latest, err := store.GetLatest(ctx, "proj-1", "env-1", "app-api", "database")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != created.ID {
		t.Fatalf("expected %s, got %s", created.ID, latest.ID)
	}

	// List for deployment
	runs, err := store.ListForDeployment(ctx, "proj-1", "dep-job-100")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != created.ID {
		t.Fatalf("expected 1 run, got %+v", runs)
	}
}
