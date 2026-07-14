package repository

import (
	"bytes"
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
	if parsed.Name != "Opsi CD Bootstrap" || parsed.Permissions["contents"] != "read" {
		t.Fatalf("workflow=%+v", parsed)
	}
	text := string(first)
	for _, forbidden := range []string{"id-token", "packages:", "pull_request", "push:", "secrets.", "uses:", "http://", "https://", "docker build", "docker push", "actions/checkout"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("workflow contains forbidden value %q", forbidden)
		}
	}
	if strings.Count(text, "workflow_dispatch:") != 1 {
		t.Fatalf("unexpected triggers:\n%s", text)
	}
}
