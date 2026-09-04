package deploymentworkflow

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
)

func TestEffectiveRuntimeKeysAndReviewValidation(t *testing.T) {
	service, run, authority := fixture(t)
	run.Plan.Applications = []repositoryanalysis.Application{
		{SourceKey: "plain-src", Key: "plain-app", Name: "plain-app", Root: ".", Port: 8080, Environment: map[string]string{"APP_ENV": "prod"}, Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"}},
		{SourceKey: "secret-src", Key: "secret-app", Name: "secret-app", Root: ".", Port: 8081, Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"}},
		{SourceKey: "dep-src", Key: "dep-app", Name: "dep-app", Root: ".", Port: 8082, Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"}},
		{SourceKey: "empty-src", Key: "empty-app", Name: "empty-app", Root: ".", Port: 8083, Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"}},
	}
	run.Plan.Secrets = []repositoryanalysis.Secret{
		{Name: "api-secret", ApplicationKey: "secret-app", EnvironmentName: "API_SECRET_REF", Generated: true, SecretRef: "generated://secret", Display: "Generated and securely stored"},
	}
	run.Plan.Dependencies = []repositoryanalysis.Dependency{
		{From: "dep-app", To: "plain-app", Protocol: "http", Strategy: "internal_http", Path: "/api", ProxyPaths: []string{"/api"}, Required: false, Injections: []repositoryanalysis.Injection{
			{EnvironmentName: "BACKEND_URL", SymbolicSource: "application.internal_url"},
		}},
	}
	run.Plan.ApplicationEnvironmentReviews = []ApplicationEnvironmentReview{
		{ApplicationSourceKey: "empty-src", NoEnvironmentRequired: true},
	}
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePlan(run.Plan); err != nil {
		t.Fatalf("expected valid plan, got: %v", err)
	}

	stored, err := service.Store.Save(context.Background(), run, run.Revision, Event{})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.Approve(context.Background(), stored.ProjectID, stored.ID, "user-1", stored.Plan.Hash, authority)
	if err != nil {
		t.Fatalf("expected approval to succeed, got: %v", err)
	}
	if approved.State != StateProvisioning {
		t.Fatalf("expected StateProvisioning, got: %s", approved.State)
	}
}

func TestAppWithoutKeyCreatesBlockingIssueAndNoneRequiredTransitionsToAwaitingApproval(t *testing.T) {
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	service := Service{Store: NewMemoryStore(), Now: func() time.Time { return now }}
	source := Source{RepositoryID: 1, InstallationID: 2, Repository: "owner/repo", SelectedRef: "main"}
	run, _, err := service.Create(context.Background(), "project-1", "user-1", "create-review-test", source, Target{EnvironmentID: "env-1", RuntimeID: "runtime-1", Exposure: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	analysis := repositoryanalysis.Result{
		SchemaVersion: repositoryanalysis.SchemaVersion,
		RepositoryID:  1,
		Repository:    "owner/repo",
		SelectedRef:   "main",
		CommitSHA:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Applications: []repositoryanalysis.Application{{
			SourceKey: "web-src", Key: "web-app", Name: "web-app", Root: ".", Port: 3000,
			Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"},
		}},
	}
	authority := AuthorityRevisions{SourceCommitSHA: analysis.CommitSHA}
	run, err = service.SetAnalysis(context.Background(), run.ProjectID, run.ID, analysis, authority, run.Plan.Target)
	if err != nil {
		t.Fatal(err)
	}

	if run.State != StateAwaitingInput {
		t.Fatalf("expected StateAwaitingInput, got: %s", run.State)
	}
	hasReviewIssue := false
	for _, issue := range run.Plan.Issues {
		if issue.Code == "APPLICATION_ENVIRONMENT_REVIEW_REQUIRED" && issue.Blocking {
			hasReviewIssue = true
			break
		}
	}
	if !hasReviewIssue {
		t.Fatalf("expected blocking APPLICATION_ENVIRONMENT_REVIEW_REQUIRED issue, got: %+v", run.Plan.Issues)
	}

	draft := run.Plan
	draft.ApplicationEnvironmentReviews = []ApplicationEnvironmentReview{
		{ApplicationSourceKey: "web-src", NoEnvironmentRequired: true},
	}
	updated, err := service.UpdatePlan(context.Background(), run.ProjectID, run.ID, "user-1", run.Plan.Hash, draft)
	if err != nil {
		t.Fatalf("expected UpdatePlan to succeed, got: %v", err)
	}

	if updated.State != StateAwaitingApproval {
		t.Fatalf("expected StateAwaitingApproval after confirmation, got: %s", updated.State)
	}
	for _, issue := range updated.Plan.Issues {
		if issue.Code == "APPLICATION_ENVIRONMENT_REVIEW_REQUIRED" {
			t.Fatalf("issue APPLICATION_ENVIRONMENT_REVIEW_REQUIRED should be resolved, but found: %+v", issue)
		}
	}

	approved, err := service.Approve(context.Background(), updated.ProjectID, updated.ID, "user-1", updated.Plan.Hash, authority)
	if err != nil {
		t.Fatalf("expected Approve to succeed, got: %v", err)
	}
	if approved.State != StateProvisioning {
		t.Fatalf("expected StateProvisioning, got: %s", approved.State)
	}
}

func TestRuntimeEnvironmentValidationRejections(t *testing.T) {
	service, run, _ := fixture(t)
	_ = service

	baseApp := repositoryanalysis.Application{
		SourceKey: "api-src", Key: "api", Name: "api", Root: ".", Port: 8080,
		Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"},
	}

	for _, badName := range []string{"PASSWORD", "DB_PASSWORD", "API_KEY", "SECRET_TOKEN", "AUTH_CREDENTIAL"} {
		plan := run.Plan
		app := baseApp
		app.Environment = map[string]string{badName: "secret-val"}
		plan.Applications = []repositoryanalysis.Application{app}
		_ = refreshHash(&plan)
		if err := ValidatePlan(plan); err == nil || !strings.Contains(err.Error(), "secret-like") {
			t.Fatalf("expected secret-like error for %s, got: %v", badName, err)
		}
	}

	{
		plan := run.Plan
		app := baseApp
		app.Environment = map[string]string{"DATABASE_URL": "postgres://localhost"}
		plan.Applications = []repositoryanalysis.Application{app}
		plan.Secrets = []repositoryanalysis.Secret{
			{Name: "db-secret", ApplicationKey: "api", EnvironmentName: "DATABASE_URL", Generated: true, SecretRef: "generated://db", Display: "Generated and securely stored"},
		}
		_ = refreshHash(&plan)
		if err := ValidatePlan(plan); err == nil || !strings.Contains(err.Error(), "duplicate runtime key") {
			t.Fatalf("expected duplicate runtime key error, got: %v", err)
		}
	}

	{
		plan := run.Plan
		app := baseApp
		target := baseApp
		target.SourceKey, target.Key, target.Name = "target-src", "target", "target"
		target.Environment = map[string]string{"PORT": "8080"}
		plan.Applications = []repositoryanalysis.Application{app, target}
		plan.Secrets = []repositoryanalysis.Secret{
			{Name: "dep-secret", ApplicationKey: "api", EnvironmentName: "BACKEND_URL", Generated: true, SecretRef: "generated://backend", Display: "Generated and securely stored"},
		}
		plan.Dependencies = []repositoryanalysis.Dependency{
			{From: "api", To: "target", Protocol: "http", Strategy: "internal_http", Injections: []repositoryanalysis.Injection{
				{EnvironmentName: "BACKEND_URL", SymbolicSource: "application.internal_url"},
			}},
		}
		_ = refreshHash(&plan)
		if err := ValidatePlan(plan); err == nil {
			t.Fatal("expected error for duplicate key between secret and dependency injection")
		}
	}

	{
		plan := run.Plan
		app := baseApp
		app.Environment = map[string]string{"PORT": "8080"}
		plan.Applications = []repositoryanalysis.Application{app}
		plan.ApplicationEnvironmentReviews = []ApplicationEnvironmentReview{
			{ApplicationSourceKey: "api-src", NoEnvironmentRequired: true},
		}
		_ = refreshHash(&plan)
		if err := ValidatePlan(plan); err == nil || !strings.Contains(err.Error(), "cannot declare") {
			t.Fatalf("expected conflicting review error, got: %v", err)
		}
	}

	{
		plan := run.Plan
		app := baseApp
		app.Environment = map[string]string{"BIG_VAL": strings.Repeat("x", 4097)}
		plan.Applications = []repositoryanalysis.Application{app}
		_ = refreshHash(&plan)
		if err := ValidatePlan(plan); err == nil || !strings.Contains(err.Error(), "4096") {
			t.Fatalf("expected value length error, got: %v", err)
		}
	}

	{
		plan := run.Plan
		app := baseApp
		app.Environment = map[string]string{}
		for i := range 65 {
			app.Environment[fmt.Sprintf("VAR_%d", i)] = "val"
		}
		plan.Applications = []repositoryanalysis.Application{app}
		_ = refreshHash(&plan)
		if err := ValidatePlan(plan); err == nil || !strings.Contains(err.Error(), "64") {
			t.Fatalf("expected count > 64 error, got: %v", err)
		}
	}
}

func TestRuntimeEnvironmentRejectsAmbiguousOrUnboundedKeys(t *testing.T) {
	_, run, _ := fixture(t)
	base := repositoryanalysis.Application{SourceKey: "api-src", Key: "api", Name: "api", Root: ".", Port: 8080, Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"}}
	target := repositoryanalysis.Application{SourceKey: "target-src", Key: "target", Name: "target", Root: ".", Port: 8081, Environment: map[string]string{"PORT": "8081"}, Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"}}

	tests := []struct {
		name string
		edit func(*Plan)
		want string
	}{
		{name: "duplicate source key", edit: func(plan *Plan) {
			second := target
			second.SourceKey = base.SourceKey
			plan.Applications = []repositoryanalysis.Application{base, second}
		}, want: "source keys must be unique"},
		{name: "invalid generated key", edit: func(plan *Plan) {
			plan.Applications = []repositoryanalysis.Application{base, target}
			plan.Dependencies = []repositoryanalysis.Dependency{{From: "api", To: "target", Protocol: "http", Strategy: "internal_http", Injections: []repositoryanalysis.Injection{{EnvironmentName: "BAD-NAME", SymbolicSource: "application.internal_url"}}}}
		}, want: "runtime environment name"},
		{name: "too many generated keys", edit: func(plan *Plan) {
			plan.Applications = []repositoryanalysis.Application{base, target}
			injections := make([]repositoryanalysis.Injection, 65)
			for i := range injections {
				injections[i] = repositoryanalysis.Injection{EnvironmentName: fmt.Sprintf("GENERATED_%d", i), SymbolicSource: "application.internal_url"}
			}
			plan.Dependencies = []repositoryanalysis.Dependency{{From: "api", To: "target", Protocol: "http", Strategy: "internal_http", Injections: injections}}
		}, want: "effective environment variable count exceeds 64"},
		{name: "too many secrets", edit: func(plan *Plan) {
			plan.Applications = []repositoryanalysis.Application{base}
			for i := range 33 {
				plan.Secrets = append(plan.Secrets, repositoryanalysis.Secret{Name: fmt.Sprintf("secret-%d", i), ApplicationKey: "api", EnvironmentName: fmt.Sprintf("SECRET_REF_%d", i), Generated: true, SecretRef: fmt.Sprintf("generated://secret-%d", i), Display: "Generated and securely stored"})
			}
		}, want: "secret reference count exceeds 32"},
		{name: "duplicate secret logical name", edit: func(plan *Plan) {
			plan.Applications = []repositoryanalysis.Application{base}
			plan.Secrets = []repositoryanalysis.Secret{
				{Name: "shared", ApplicationKey: "api", EnvironmentName: "FIRST_SECRET", Generated: true, SecretRef: "generated://first", Display: "Generated and securely stored"},
				{Name: "shared", ApplicationKey: "api", EnvironmentName: "SECOND_SECRET", Generated: true, SecretRef: "generated://second", Display: "Generated and securely stored"},
			}
		}, want: "duplicate workload secret logical name"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := run.Plan
			plan.Applications, plan.Dependencies, plan.Secrets, plan.ApplicationEnvironmentReviews = nil, nil, nil, nil
			test.edit(&plan)
			if err := refreshHash(&plan); err != nil {
				t.Fatal(err)
			}
			if err := ValidatePlan(plan); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestPlanHashChangesOnConfirmationAndSecretRevision(t *testing.T) {
	_, run, _ := fixture(t)

	appWithoutKey := repositoryanalysis.Application{
		SourceKey: "empty-src", Key: "empty-app", Name: "empty-app", Root: ".", Port: 8080,
		Build: repositoryanalysis.Build{Context: ".", Strategy: "dockerfile", Platform: "linux/amd64"},
	}
	run.Plan.Applications = []repositoryanalysis.Application{appWithoutKey}
	run.Plan.ApplicationEnvironmentReviews = []ApplicationEnvironmentReview{
		{ApplicationSourceKey: "empty-src", NoEnvironmentRequired: true},
	}
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	hash1 := run.Plan.Hash

	run.Plan.ApplicationEnvironmentReviews[0].NoEnvironmentRequired = false
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	hash2 := run.Plan.Hash
	if hash1 == hash2 {
		t.Fatal("expected hash to change when review confirmation changes")
	}

	run.Plan.ApplicationEnvironmentReviews = nil
	run.Plan.Applications[0].Environment = nil
	run.Plan.Secrets = []repositoryanalysis.Secret{
		{Name: "api-secret", ApplicationKey: "empty-app", EnvironmentName: "API_SECRET", Revision: 1, SecretRef: "workload-secret://sec-1", Display: "Stored securely"},
	}
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	hashWithRev1 := run.Plan.Hash

	run.Plan.Secrets[0].Revision = 2
	if err := refreshHash(&run.Plan); err != nil {
		t.Fatal(err)
	}
	hashWithRev2 := run.Plan.Hash
	if hashWithRev1 == hashWithRev2 {
		t.Fatal("expected hash to change when secret revision changes")
	}
}

func TestSchemaMigrationV1V2ToV3(t *testing.T) {
	_, run, _ := fixture(t)

	runV1 := run
	runV1.SchemaVersion = legacyRunV1SchemaVersion
	runV1.Plan.SchemaVersion = legacyPlanV1SchemaVersion
	runV1.State = StateAwaitingApproval
	runV1.Approval = &Approval{Actor: "user-1", PlanHash: runV1.Plan.Hash}
	normalizedV1 := normalizeStoredRun(runV1)
	if normalizedV1.State != StateStale || normalizedV1.Approval != nil || normalizedV1.Failure == nil || normalizedV1.Failure.Code != "DEPLOYMENT_PLAN_V1_STALE" {
		t.Fatalf("expected stale v1 run, got: %+v", normalizedV1)
	}
	if normalizedV1.SchemaVersion != RunSchemaVersion || normalizedV1.Plan.SchemaVersion != PlanSchemaVersion {
		t.Fatalf("expected v3 schema, got run=%s plan=%s", normalizedV1.SchemaVersion, normalizedV1.Plan.SchemaVersion)
	}

	runV2 := run
	runV2.SchemaVersion = legacyRunV2SchemaVersion
	runV2.Plan.SchemaVersion = legacyPlanV2SchemaVersion
	runV2.State = StateAwaitingApproval
	runV2.Approval = &Approval{Actor: "user-1", PlanHash: runV2.Plan.Hash}
	normalizedV2 := normalizeStoredRun(runV2)
	if normalizedV2.State != StateStale || normalizedV2.Approval != nil || normalizedV2.Failure == nil || normalizedV2.Failure.Code != "DEPLOYMENT_PLAN_V2_STALE" {
		t.Fatalf("expected stale v2 run, got: %+v", normalizedV2)
	}
	if normalizedV2.SchemaVersion != RunSchemaVersion || normalizedV2.Plan.SchemaVersion != PlanSchemaVersion {
		t.Fatalf("expected v3 schema, got run=%s plan=%s", normalizedV2.SchemaVersion, normalizedV2.Plan.SchemaVersion)
	}

	terminalV2 := run
	terminalV2.SchemaVersion = legacyRunV2SchemaVersion
	terminalV2.Plan.SchemaVersion = legacyPlanV2SchemaVersion
	terminalV2.State = StateSucceeded
	terminalNormalized := normalizeStoredRun(terminalV2)
	if terminalNormalized.State != StateSucceeded {
		t.Fatalf("expected terminal state preserved, got: %s", terminalNormalized.State)
	}
	if terminalNormalized.SchemaVersion != RunSchemaVersion || terminalNormalized.Plan.SchemaVersion != PlanSchemaVersion {
		t.Fatalf("expected v3 schema, got run=%s plan=%s", terminalNormalized.SchemaVersion, terminalNormalized.Plan.SchemaVersion)
	}
	if len(terminalNormalized.Plan.ApplicationEnvironmentReviews) != 0 {
		t.Fatalf("terminal history should not simulate confirmation, got: %+v", terminalNormalized.Plan.ApplicationEnvironmentReviews)
	}

	future := run
	future.SchemaVersion = "opsi.deployment_run/v4"
	future.Plan.SchemaVersion = "opsi.deployment_plan/v4"
	if normalized := normalizeStoredRun(future); normalized.SchemaVersion != future.SchemaVersion || normalized.Plan.SchemaVersion != future.Plan.SchemaVersion {
		t.Fatalf("unknown future schema must not be projected: %+v", normalized)
	}
}
