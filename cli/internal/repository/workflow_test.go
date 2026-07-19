package repository

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkflowAuthorityIsSplitAndDeterministic(t *testing.T) {
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{{Key: "api", Deploy: DeployV2{Production: ProductionV2{Enabled: true, Branches: []string{"main"}}}}}}
	first := RenderWorkflow(cfg)
	second := RenderWorkflow(cfg)
	if !bytes.Equal(first, second) || len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatal("workflow is not deterministic with a final newline")
	}
	var parsed struct {
		Name        string            `yaml:"name"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]any    `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(first, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "Opsi CD" || len(parsed.Permissions) != 1 || parsed.Permissions["contents"] != "read" {
		t.Fatalf("workflow permissions=%+v", parsed.Permissions)
	}
	for _, name := range []string{"plan", "build", "publish-and-record"} {
		if parsed.Jobs[name] == nil {
			t.Fatalf("missing job %q", name)
		}
	}
	text := string(first)
	for _, forbidden := range []string{"pull_request_target", "gh auth", "OPSI_CLOUD_TOKEN", "OPSI_PAT", "private_key", "webhook_secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("workflow contains forbidden value %q", forbidden)
		}
	}
	plan := workflowJobSection(t, text, "  plan:", "  build:")
	build := workflowJobSection(t, text, "  build:", "  publish-and-record:")
	publish := workflowJobSection(t, text, "  publish-and-record:", "")
	for name, section := range map[string]string{"plan": plan, "build": build} {
		for _, forbidden := range []string{"id-token:", "packages:", "secrets.GITHUB_TOKEN", "submit-from-github-actions", "push: true"} {
			if strings.Contains(section, forbidden) {
				t.Fatalf("%s job has trusted authority %q", name, forbidden)
			}
		}
	}
	for _, required := range []string{
		"github.event_name == 'push'",
		"github.event.repository.fork == false",
		"startsWith(github.ref, 'refs/heads/')",
		"needs.plan.outputs.has-publish-services == 'true'",
		"fromJSON(needs.plan.outputs.publish-matrix)",
		"packages: write", "id-token: write", "contents: read",
		"secrets.GITHUB_TOKEN", "vars.OPSI_CLOUD_URL",
		"opsi internal build-record submit-from-github-actions",
		"steps.publish.outputs.digest", "max-parallel: 4", "timeout-minutes: 30",
	} {
		if !strings.Contains(publish, required) {
			t.Fatalf("publish job missing %q", required)
		}
	}
	if strings.Index(publish, "push: true") > strings.Index(publish, "submit-from-github-actions") {
		t.Fatal("BuildRecord submission appears before successful image push")
	}
}

func TestWorkflowPinsActionsAndOpsiSource(t *testing.T) {
	text := string(RenderWorkflow())
	pin := regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+@[0-9a-f]{40}$`)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "uses: ") && !pin.MatchString(strings.TrimPrefix(line, "uses: ")) {
			t.Fatalf("mutable action reference: %s", line)
		}
	}
	if len(opsiSourceRevision) != 40 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(opsiSourceRevision) || strings.Count(text, "ref: "+opsiSourceRevision) != 2 {
		t.Fatalf("Opsi source pin is not exact: %q", opsiSourceRevision)
	}
	if strings.Count(text, "persist-credentials: false") != 5 {
		t.Fatal("every repository checkout must disable credential persistence")
	}
}

func TestPlanMatrixCarriesExactServiceProductionRefs(t *testing.T) {
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{
		{Key: "api", Build: BuildV2{Context: "services/api", Dockerfile: "services/api/Dockerfile", Platform: "linux/amd64"}, Deploy: DeployV2{Production: ProductionV2{Enabled: true, Branches: []string{"release", "main"}}}},
		{Key: "worker", Build: BuildV2{Context: "services/worker", Dockerfile: "services/worker/Dockerfile", Platform: "linux/amd64"}, Deploy: DeployV2{Production: ProductionV2{Enabled: false, Branches: []string{}}}},
	}}
	plan, err := (CDService{}).Plan(t.Context(), PlanRequest{Event: EventInitial, Head: strings.Repeat("a", 40), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Matrix) != 2 || strings.Join(plan.Matrix[0].ProductionRefs, ",") != "refs/heads/main,refs/heads/release" || len(plan.Matrix[1].ProductionRefs) != 0 {
		t.Fatalf("matrix=%+v", plan.Matrix)
	}
}

func workflowJobSection(t *testing.T, text, start, end string) string {
	t.Helper()
	from := strings.Index(text, start)
	if from < 0 {
		t.Fatalf("missing section %q", start)
	}
	if end == "" {
		return text[from:]
	}
	to := strings.Index(text[from+len(start):], end)
	if to < 0 {
		t.Fatalf("missing section end %q", end)
	}
	return text[from : from+len(start)+to]
}
