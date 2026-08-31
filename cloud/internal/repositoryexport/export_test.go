package repositoryexport

import (
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	"gopkg.in/yaml.v3"
)

func exportRun(t *testing.T) deploymentworkflow.Run {
	t.Helper()
	plan := deploymentworkflow.Plan{
		SchemaVersion: deploymentworkflow.PlanSchemaVersion,
		Source:        deploymentworkflow.Source{RepositoryID: 7, InstallationID: 8, Repository: "owner/repo", SelectedRef: "main", CommitSHA: strings.Repeat("a", 40)},
		Applications: []repositoryanalysis.Application{{
			SourceKey: "api", Key: "repo-api", Root: "api", Port: 8080,
			Build:       repositoryanalysis.Build{Context: "api", DockerfilePath: "api/Dockerfile", Strategy: "dockerfile", Platform: "linux/amd64"},
			Environment: map[string]string{"LOG_LEVEL": "info", "API_TOKEN": "must-not-export"},
		}},
		Resources: []repositoryanalysis.Resource{{LogicalName: "database", Type: "postgres", Managed: true, Required: true, Settings: map[string]string{"region": "local", "password": "must-not-export"}}},
		Secrets: []repositoryanalysis.Secret{
			{Name: "database-password", ApplicationKey: "repo-api", EnvironmentName: "DB_PASSWORD", SecretRef: "cloud-secret-id"},
			{Name: "jwt-signing-key", ApplicationKey: "repo-api", EnvironmentName: "JWT_KEY", Generated: true, SecretRef: "generated://material"},
		},
	}
	hash, err := deploymentworkflow.HashPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Hash = hash
	return deploymentworkflow.Run{ID: "run-1", Revision: 4, Plan: plan}
}

func TestRenderRedactsCloudAndSecretMaterial(t *testing.T) {
	run := exportRun(t)
	data, err := Render(run.Plan)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"must-not-export", "cloud-secret-id", "generated://material", "repository_id", "installation_id", "plan_hash"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("export leaked %q:\n%s", forbidden, text)
		}
	}
	for _, expected := range []string{"LOG_LEVEL: info", "reference: secret://database-password", "source: generated", "dockerfile: api/Dockerfile"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("export missing %q:\n%s", expected, text)
		}
	}
}

func TestPreviewHashBindsRunPlanBranchAndDiff(t *testing.T) {
	run := exportRun(t)
	first, err := NewPreview(run, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := NewPreview(run, "main", nil)
	if err != nil || repeated.PreviewHash != first.PreviewHash || repeated.YAML != first.YAML {
		t.Fatalf("preview was not deterministic: first=%+v repeated=%+v err=%v", first, repeated, err)
	}
	changedBranch, _ := NewPreview(run, "dev", nil)
	changedCurrent, _ := NewPreview(run, "main", []byte("old\n"))
	if first.PreviewHash == changedBranch.PreviewHash || first.PreviewHash == changedCurrent.PreviewHash || first.Diff == "" {
		t.Fatalf("preview hash did not bind reviewed inputs")
	}
}

func TestRenderRejectsNonDockerfilePlanInsteadOfCreatingParallelBuildIntent(t *testing.T) {
	run := exportRun(t)
	run.Plan.Applications[0].Build = repositoryanalysis.Build{Context: ".", Strategy: "buildpack", Platform: "linux/amd64"}
	run.Plan.Hash, _ = deploymentworkflow.HashPlan(run.Plan)
	if _, err := Render(run.Plan); err == nil {
		t.Fatal("expected non-Dockerfile export to be rejected")
	}
}

func TestRenderCanonicalizesLegacyURIWithoutExportingCredentialMaterial(t *testing.T) {
	run := exportRun(t)
	run.Plan.Dependencies = []repositoryanalysis.Dependency{{From: "repo-api", To: "database", Protocol: "postgres", Required: true, Injections: []repositoryanalysis.Injection{{EnvironmentName: "DATABASE_URL", SymbolicSource: "resource.database.connection_string"}}}}
	run.Plan.Hash, _ = deploymentworkflow.HashPlan(run.Plan)
	data, err := Render(run.Plan)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "symbolicSource: connection.postgres.uri") || strings.Contains(text, "connection_string") {
		t.Fatalf("legacy alias was not canonicalized:\n%s", text)
	}
}

func TestRenderRejectsUnsafeConnectionTemplate(t *testing.T) {
	run := exportRun(t)
	run.Plan.Dependencies = []repositoryanalysis.Dependency{{From: "repo-api", To: "database", Protocol: "postgres", Required: true, Injections: []repositoryanalysis.Injection{{EnvironmentName: "DATABASE_DSN", SymbolicSource: "connection.template", Template: "password=must-not-export"}}}}
	run.Plan.Hash, _ = deploymentworkflow.HashPlan(run.Plan)
	if data, err := Render(run.Plan); err == nil || strings.Contains(string(data), "must-not-export") {
		t.Fatalf("unsafe template data=%q err=%v", data, err)
	}
}

func TestRenderPreservesConnectionTemplateWhitespace(t *testing.T) {
	run := exportRun(t)
	template := " host={{host}}\nport={{port}} "
	run.Plan.Dependencies = []repositoryanalysis.Dependency{{From: "repo-api", To: "database", Protocol: "postgres", Required: true, Injections: []repositoryanalysis.Injection{{EnvironmentName: "DATABASE_DSN", SymbolicSource: "connection.template", Template: template}}}}
	run.Plan.Hash, _ = deploymentworkflow.HashPlan(run.Plan)
	data, err := Render(run.Plan)
	if err != nil {
		t.Fatal(err)
	}
	var decoded config
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Services[0].Runtime.Dependencies[0].Injections[0].Template
	if got != template {
		t.Fatalf("exported template=%q want=%q", got, template)
	}
}

func TestRenderRejectsUnsupportedManagedProtocolWithoutMappings(t *testing.T) {
	run := exportRun(t)
	run.Plan.Dependencies = []repositoryanalysis.Dependency{{From: "repo-api", To: "database", Protocol: "kafka", Required: false}}
	run.Plan.Hash, _ = deploymentworkflow.HashPlan(run.Plan)
	if _, err := Render(run.Plan); err == nil {
		t.Fatal("unsupported managed protocol was exported")
	}
}
