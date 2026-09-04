package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/opsi-dev/opsi/cli/internal/mcp"
)

type memoryKeychainStore struct {
	pat string
}

func (m *memoryKeychainStore) GetPAT() (string, error)    { return m.pat, nil }
func (m *memoryKeychainStore) SetPAT(val string) error    { m.pat = val; return nil }
func (m *memoryKeychainStore) DeletePAT() error           { m.pat = ""; return nil }
func (m *memoryKeychainStore) Get(key string) (string, error) {
	if key == "pat" {
		return m.pat, nil
	}
	return "", nil
}
func (m *memoryKeychainStore) Set(key, val string) error {
	if key == "pat" {
		m.pat = val
	}
	return nil
}
func (m *memoryKeychainStore) Delete(key string) error {
	if key == "pat" {
		m.pat = ""
	}
	return nil
}

// TestMCPDeploymentsListCollectorIntegration verifies the real MCP handler and
// collector boundary against a local fake Cloud authority. It deliberately does
// not claim to execute the external Codex CLI.
func TestMCPDeploymentsListCollectorIntegration(t *testing.T) {
	ctx := context.Background()

	// Case 1: Valid PAT -> deployments_list returns success
	t.Run("valid_pat_success", func(t *testing.T) {
		cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/auth/pat/verify" {
				_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "org_id": "org-1", "user_id": "u-1", "role": "owner"})
				return
			}
			if strings.Contains(r.URL.Path, "/deployments") {
				_ = json.NewEncoder(w).Encode(map[string]any{"deployments": []any{
					map[string]any{"id": "dep-1", "status": "succeeded", "service_id": "svc-web"},
				}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}))
		defer cloud.Close()

		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "cli.yaml")
		_ = os.WriteFile(cfgPath, []byte(fmt.Sprintf("cloud_url: %s\n", cloud.URL)), 0600)

		validPAT := "opsi_pat_valid_smoke_token_123"
		mcpServer := mcp.NewServer(mcp.ServerOptions{
			Version:          "1.0.0-smoke",
			ConfigPath:       cfgPath,
			DefaultProjectID: "proj-1",
			KeychainFactory: func() (keychain.Store, error) {
				return &memoryKeychainStore{pat: validPAT}, nil
			},
		})

		// Call deployments_list through MCP server
		reqBytes, _ := json.Marshal(mcp.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/call",
			Params:  json.RawMessage(`{"name":"deployments_list","arguments":{"project_id":"proj-1"}}`),
		})
		resp, err := mcpServer.HandleMessage(ctx, reqBytes)
		if err != nil {
			t.Fatalf("MCP tool execution failed: %v", err)
		}
		callRes, ok := resp.Result.(mcp.CallToolResult)
		if !ok || callRes.IsError {
			t.Fatalf("expected successful MCP call, got: %+v", resp.Result)
		}

		// Simulate Codex capturing this tool result in its event stream
		codexEvent := fmt.Sprintf(`{"type":"item.completed","item":{"id":"call-smoke-1","type":"mcp_tool_call","server":"opsi","tool":"deployments_list","status":"completed","result":{"content":[{"type":"text","text":%q}]}}}`, callRes.Content[0].Text)

		parsed, err := parseCodexEventStreamWithTurnID("turn-smoke-valid", []byte(codexEvent+"\n"), "")
		if err != nil {
			t.Fatalf("collector failed: %v", err)
		}
		if parsed.ToolFailed {
			t.Fatal("expected tool to succeed")
		}
		if parsed.SuccessfulOpsiToolCalls != 1 || len(parsed.SuccessfulOpsiTools) != 1 || parsed.SuccessfulOpsiTools[0] != "deployments_list" {
			t.Fatalf("unexpected parsed result: %+v", parsed)
		}
	})

	// Case 2: Invalid / missing PAT -> deployments_list returns structured AUTH_REQUIRED error
	t.Run("invalid_pat_returns_structured_diagnostic", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "cli.yaml")
		_ = os.WriteFile(cfgPath, []byte("cloud_url: https://example.com\n"), 0600)

		// Unauthenticated MCP server
		unauthMCPServer := mcp.NewServer(mcp.ServerOptions{
			Version:          "1.0.0-smoke",
			ConfigPath:       cfgPath,
			DefaultProjectID: "proj-1",
			KeychainFactory: func() (keychain.Store, error) {
				return &memoryKeychainStore{pat: ""}, nil
			},
		})

		reqBytes, _ := json.Marshal(mcp.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      2,
			Method:  "tools/call",
			Params:  json.RawMessage(`{"name":"deployments_list","arguments":{"project_id":"proj-1"}}`),
		})
		resp, err := unauthMCPServer.HandleMessage(ctx, reqBytes)
		if err != nil {
			t.Fatalf("MCP call error: %v", err)
		}
		callRes, ok := resp.Result.(mcp.CallToolResult)
		if !ok || !callRes.IsError {
			t.Fatalf("expected error result, got %+v", resp.Result)
		}
		if callRes.StructuredContent == nil || callRes.StructuredContent.Code != "AUTH_REQUIRED" {
			t.Fatalf("expected structured AUTH_REQUIRED error, got: %+v", callRes.StructuredContent)
		}

		// Simulate Codex capturing this error in its event stream
		resJSON, _ := json.Marshal(callRes)
		codexEvent := fmt.Sprintf(`{"type":"item.completed","item":{"id":"call-smoke-2","type":"mcp_tool_call","server":"opsi","tool":"deployments_list","status":"failed","result":%s}}`, string(resJSON))

		parsed, err := parseCodexEventStreamWithTurnID("turn-smoke-unauth", []byte(codexEvent+"\n"), "")
		if err != nil {
			t.Fatalf("unexpected collector error: %v", err)
		}
		if !parsed.ToolFailed {
			t.Fatal("expected ToolFailed to be true")
		}
		if parsed.EventInvalid {
			t.Fatal("expected EventInvalid to be false because valid diagnostic was provided")
		}
		if !strings.Contains(parsed.ToolFailureMessage, "AUTH_REQUIRED") || !strings.Contains(parsed.ToolFailureMessage, "unauthenticated") {
			t.Fatalf("expected code and message in failure diagnostic, got: %s", parsed.ToolFailureMessage)
		}
	})
}
