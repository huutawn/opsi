package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestIncidentHelpContainsOnlyActiveCommands(t *testing.T) {
	cmd := NewRootCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"incident", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, command := range []string{"list", "get", "resolve"} {
		if !strings.Contains(help, command) {
			t.Fatalf("incident help missing %q: %s", command, help)
		}
	}
	for _, removed := range []string{"analyze", "approve", "RCA", "recommended action"} {
		if strings.Contains(help, removed) {
			t.Fatalf("incident help contains removed surface %q: %s", removed, help)
		}
	}
}
