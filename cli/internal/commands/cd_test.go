package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/repository"
)

type cdCommandRunner struct {
	root string
	step int
}

func (r *cdCommandRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.step++
	switch r.step {
	case 1:
		return []byte(r.root + "\n"), nil
	case 2:
		return []byte("false\n"), nil
	case 3, 4:
		return nil, nil
	case 5:
		return []byte("M\x00Dockerfile\x00"), nil
	default:
		return nil, errors.New("unexpected git command")
	}
}

func TestCDPlanHumanAndJSONParity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configBytes, err := repository.RenderConfig(repository.ConfigOptions{ServiceKey: "api", Context: ".", Dockerfile: "Dockerfile", Platform: "linux/amd64", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".opsi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, defaultConfigPath), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	jsonOutput := &bytes.Buffer{}
	jsonCommand := NewRootCommand(Options{Version: "test", GitRunner: &cdCommandRunner{root: root}})
	jsonCommand.SetOut(jsonOutput)
	jsonCommand.SetArgs([]string{"cd", "plan", "--repo-dir", root, "--base", base, "--head", head, "--json"})
	if err := jsonCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	var plan repository.ChangedServicePlan
	if err := json.Unmarshal(jsonOutput.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	humanOutput := &bytes.Buffer{}
	humanCommand := NewRootCommand(Options{Version: "test", GitRunner: &cdCommandRunner{root: root}})
	humanCommand.SetOut(humanOutput)
	humanCommand.SetArgs([]string{"cd", "plan", "--repo-dir", root, "--base", base, "--head", head})
	if err := humanCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(humanOutput.String(), plan.PlanHash) || !strings.Contains(humanOutput.String(), "api [service_path_changed]") {
		t.Fatalf("human=%s plan=%+v", humanOutput.String(), plan)
	}
}
