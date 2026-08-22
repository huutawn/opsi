package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

type memoryKeychain struct {
	pat string
}

func (m *memoryKeychain) GetPAT() (string, error) { return m.pat, nil }
func (m *memoryKeychain) SetPAT(val string) error { m.pat = val; return nil }
func (m *memoryKeychain) DeletePAT() error        { m.pat = ""; return nil }

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
	if len(toolsList) != 20 {
		t.Errorf("expected exactly 20 tools, got %d", len(toolsList))
	}

	// Invariant: all MCP-01 facts and MCP-02 advisory tools are non-operational.
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

	// 1. Non-loopback addresses must be rejected
	invalidAddrs := []string{
		"192.168.1.100:9781",
		"0.0.0.0:9781",
		":9781",
		"8.8.8.8:9781",
	}
	for _, addr := range invalidAddrs {
		err := s.ServeHTTP(ctx, addr)
		if err == nil {
			t.Errorf("expected error binding to non-loopback address %q, got nil", addr)
		}
	}

	// 2. Start a real server on 127.0.0.1 with dynamic port for testing
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on loopback: %v", err)
	}
	serverAddr := listener.Addr().String()
	listener.Close() // Release for ServeHTTP test

	httpCtx, httpCancel := context.WithCancel(context.Background())
	defer httpCancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ServeHTTP(httpCtx, serverAddr)
	}()

	baseURL := "http://" + serverAddr

	// Wait for server to start
	client := &http.Client{Timeout: 2 * time.Second}
	var healthOk bool
	for range 20 {
		hResp, hErr := client.Get(baseURL + "/health")
		if hErr == nil && hResp.StatusCode == http.StatusOK {
			hResp.Body.Close()
			healthOk = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthOk {
		t.Fatalf("MCP HTTP server did not become healthy in time")
	}

	// 3. Normal POST request with application/json and valid Host header
	reqBody := `{"jsonrpc":"2.0","id":100,"method":"ping"}`
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
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

	// 4. DNS Rebinding Protection: Host header pointing to external domain must be rejected with 403
	badHostReq, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(reqBody))
	badHostReq.Header.Set("Content-Type", "application/json")
	badHostReq.Host = "evil-site.com"
	badHostResp, err := client.Do(badHostReq)
	if err != nil {
		t.Fatalf("POST /mcp with bad host failed: %v", err)
	}
	defer badHostResp.Body.Close()
	if badHostResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for DNS rebinding Host header, got %d", badHostResp.StatusCode)
	}

	// 5. Cross-Origin Protection: Malicious Origin header must be rejected with 403
	badOriginReq, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(reqBody))
	badOriginReq.Header.Set("Content-Type", "application/json")
	badOriginReq.Header.Set("Origin", "https://malicious-website.com")
	badOriginResp, err := client.Do(badOriginReq)
	if err != nil {
		t.Fatalf("POST /mcp with bad origin failed: %v", err)
	}
	defer badOriginResp.Body.Close()
	if badOriginResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for external Origin header, got %d", badOriginResp.StatusCode)
	}

	// 6. Content-Type Protection: Non-JSON Content-Type must be rejected with 415
	badCTReq, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(reqBody))
	badCTReq.Header.Set("Content-Type", "text/plain")
	badCTResp, err := client.Do(badCTReq)
	if err != nil {
		t.Fatalf("POST /mcp with text/plain Content-Type failed: %v", err)
	}
	defer badCTResp.Body.Close()
	if badCTResp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415 Unsupported Media Type for text/plain, got %d", badCTResp.StatusCode)
	}
}
