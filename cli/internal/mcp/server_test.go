package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

type memoryKeychain struct {
	pat string
}

func (m *memoryKeychain) GetPAT() (string, error)    { return m.pat, nil }
func (m *memoryKeychain) SetPAT(val string) error     { m.pat = val; return nil }
func (m *memoryKeychain) DeletePAT() error           { m.pat = ""; return nil }

func TestMCPServer_InitializeAndListTools(t *testing.T) {
	s := NewServer(ServerOptions{
		Version: "1.0.0-test",
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: "test-pat"}, nil
		},
	})
	ctx := context.Background()

	// 1. initialize request
	initReq, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}`),
	})

	resp, err := s.HandleMessage(ctx, initReq)
	if err != nil {
		t.Fatalf("HandleMessage initialize failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %v", resp.Error)
	}
	initResult, ok := resp.Result.(InitializeResult)
	if !ok {
		t.Fatalf("expected InitializeResult type, got %T", resp.Result)
	}
	if initResult.ProtocolVersion != ProtocolVersion {
		t.Errorf("expected protocol version %s, got %s", ProtocolVersion, initResult.ProtocolVersion)
	}
	if initResult.ServerInfo.Name != ServerName {
		t.Errorf("expected server name %s, got %s", ServerName, initResult.ServerInfo.Name)
	}

	// 2. ping request
	pingReq, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "ping",
	})
	pingResp, err := s.HandleMessage(ctx, pingReq)
	if err != nil || pingResp.Error != nil {
		t.Fatalf("ping failed: %v", pingResp)
	}

	// 3. tools/list request
	toolsReq, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/list",
	})
	toolsResp, err := s.HandleMessage(ctx, toolsReq)
	if err != nil || toolsResp.Error != nil {
		t.Fatalf("tools/list failed: %v", toolsResp)
	}
	toolsMap, ok := toolsResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map in tools/list result, got %T", toolsResp.Result)
	}
	toolsList, ok := toolsMap["tools"].([]Tool)
	if !ok {
		t.Fatalf("expected []Tool in result, got %T", toolsMap["tools"])
	}
	if len(toolsList) != 18 {
		t.Errorf("expected exactly 18 tools, got %d", len(toolsList))
	}

	// Invariant: Verify all 18 tools are strictly read-only
	mutationActionVerbs := []string{"create_", "update_", "delete_", "apply_", "deploy_", "build_start", "build_create", "verify_start", "execute_", "patch_", "mutate_"}
	for _, tool := range toolsList {
		for _, kw := range mutationActionVerbs {
			if strings.HasPrefix(tool.Name, kw) {
				t.Fatalf("SECURITY VIOLATION: mutation tool found in MCP-01: %s", tool.Name)
			}
		}
	}

	// 4. resources/list request
	resListReq, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      4,
		Method:  "resources/list",
	})
	resListResp, err := s.HandleMessage(ctx, resListReq)
	if err != nil || resListResp.Error != nil {
		t.Fatalf("resources/list failed: %v", resListResp)
	}
}

func TestMCPServer_StdioTransport(t *testing.T) {
	s := NewServer(ServerOptions{
		Version: "1.0.0-test",
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: "test-pat"}, nil
		},
	})

	inputData := `{"jsonrpc":"2.0","id":10,"method":"ping"}
{"jsonrpc":"2.0","id":11,"method":"tools/list"}
`
	in := strings.NewReader(inputData)
	out := &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.ServeStdio(ctx, in, out)
	if err != nil {
		t.Fatalf("ServeStdio failed: %v", err)
	}

	outputStr := out.String()
	if !strings.Contains(outputStr, `"id":10`) {
		t.Errorf("expected response with id:10, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, `"id":11`) {
		t.Errorf("expected response with id:11, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, "project_context") {
		t.Errorf("expected tools list in output, got:\n%s", outputStr)
	}
}

func TestMCPServer_HTTPTransport(t *testing.T) {
	s := NewServer(ServerOptions{
		Version: "1.0.0-test",
		KeychainFactory: func() (keychain.Store, error) {
			return &memoryKeychain{pat: "test-pat"}, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Non-loopback address must be rejected
	err := s.ServeHTTP(ctx, "192.168.1.100:9781")
	if err == nil {
		t.Errorf("expected error binding to non-loopback address, got nil")
	}

	// 2. Test HTTP handler directly via httptest
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		resp, _ := s.HandleMessage(r.Context(), body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	reqBody := `{"jsonrpc":"2.0","id":100,"method":"ping"}`
	resp, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /mcp failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var jsonResp JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&jsonResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if jsonResp.ID != float64(100) {
		t.Errorf("expected id 100, got %v", jsonResp.ID)
	}
}
