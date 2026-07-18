package repository

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkflowBootstrapOnlyAndDeterministic(t *testing.T) {
	first := RenderWorkflow()
	second := RenderWorkflow()
	if !bytes.Equal(first, second) || len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatal("workflow is not deterministic with a final newline")
	}
	var parsed struct {
		Name string `yaml:"name"`
		On   struct {
			WorkflowDispatch any `yaml:"workflow_dispatch"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := yaml.Unmarshal(first, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "Opsi CD" || parsed.Permissions["contents"] != "read" {
		t.Fatalf("workflow=%+v", parsed)
	}
	text := string(first)
	for _, forbidden := range []string{"id-token", "packages:", "pull_request_target", "secrets.", "docker push", "ghcr.io", "deploy", "cloud"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("workflow contains forbidden value %q", forbidden)
		}
	}
	if strings.Count(text, "workflow_dispatch:") != 1 || !strings.Contains(text, "timeout-minutes:") || !strings.Contains(text, "concurrency:") {
		t.Fatalf("unexpected triggers:\n%s", text)
	}
	if !strings.Contains(text, "actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5") || !strings.Contains(text, "actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff") {
		t.Fatal("checkout is not pinned to a full commit SHA")
	}
	if !strings.Contains(text, "ref: 4f9962c1fa527a308d95b4919164b032e47dd9f3") {
		t.Fatal("Opsi planner source is not pinned to the reviewed R5-006 revision")
	}
	pin := regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+@[0-9a-f]{40}$`)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "uses: ") && !pin.MatchString(strings.TrimPrefix(line, "uses: ")) {
			t.Fatalf("mutable action reference: %s", line)
		}
	}
}
