package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionHumanAndJSONIncludeExactRevision(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"version", "--json"}} {
		command := NewRootCommand(Options{Version: "r5-013-test", Revision: "deadbeef"})
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "r5-013-test") || !strings.Contains(output.String(), "deadbeef") {
			t.Fatalf("args=%v output=%s", args, output.String())
		}
	}
}

func TestManualHelpTreeHasSupportedCommandsAndNoBackendGapCommands(t *testing.T) {
	root := NewRootCommand(Options{})
	commands := map[string]bool{}
	for _, command := range root.Commands() {
		commands[command.Name()] = true
	}
	for _, name := range []string{"auth", "project", "runtime", "node", "server", "github", "service", "topology", "policy", "build-record", "deploy", "exposure", "telemetry", "incident", "audit", "version"} {
		if !commands[name] {
			t.Fatalf("supported command %q missing from help tree", name)
		}
	}
	for _, name := range []string{"member", "organization", "mcp", "ai"} {
		if commands[name] {
			t.Fatalf("backend-gap command %q must not exist", name)
		}
	}
}
