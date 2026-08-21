package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

type memoryKeychain struct {
	pat string
}

func (m *memoryKeychain) GetPAT() (string, error) { return m.pat, nil }
func (m *memoryKeychain) SetPAT(val string) error  { m.pat = val; return nil }
func (m *memoryKeychain) DeletePAT() error        { m.pat = ""; return nil }

func TestMCPCommand_VersionAndHelp(t *testing.T) {
	configPath := "dummy.yaml"
	opts := Options{
		Version:  "1.2.3",
		Revision: "abc1234",
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: "test-pat"}, nil
		},
	}

	cmd := newMCPCommand(&configPath, opts)

	// 1. Test version flag
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("mcp --version failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "opsi-mcp 1.2.3") {
		t.Errorf("expected version output, got: %s", output)
	}

	// 2. Test serve subcommand help
	buf.Reset()
	cmd.SetArgs([]string{"serve", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mcp serve --help failed: %v", err)
	}
	if !strings.Contains(buf.String(), "Start the MCP server") {
		t.Errorf("expected help output, got: %s", buf.String())
	}
}

func TestMCPCommand_StdioExecution(t *testing.T) {
	configPath := "dummy.yaml"
	opts := Options{
		Version:  "1.2.3",
		Revision: "abc1234",
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: "test-pat"}, nil
		},
	}

	cmd := newMCPCommand(&configPath, opts)

	inBuf := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}

	cmd.SetIn(inBuf)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd.SetContext(ctx)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("mcp execute failed: %v", err)
	}

	if !strings.Contains(outBuf.String(), `"id":1`) {
		t.Errorf("expected JSON-RPC response with id 1, got: %s", outBuf.String())
	}
}
