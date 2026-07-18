package cloudclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBaseURLValidation(t *testing.T) {
	for _, raw := range []string{"https://cloud.example.test", "https://cloud.example.test/", "http://127.0.0.1:9800", "http://localhost:9800", "http://[::1]:9800"} {
		client, err := New(raw, "pat", "test", nil)
		if err != nil {
			t.Fatalf("valid URL %q: %v", raw, err)
		}
		if strings.HasSuffix(client.BaseURL.Path, "/") {
			t.Fatalf("base URL was not normalized: %s", client.BaseURL)
		}
	}
	for _, raw := range []string{"http://cloud.example.test", "http://192.0.2.1", "https://user:pass@cloud.example.test", "https://cloud.example.test?q=1", "https://cloud.example.test/#fragment", "/relative", "ftp://cloud.example.test"} {
		if _, err := New(raw, "pat", "test", nil); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
}

func TestRequestHeadersTypedErrorsAndMutationBehavior(t *testing.T) {
	const pat = "super-secret-pat"
	var mutationCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+pat || request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != "opsi-cli/test" {
			t.Errorf("unexpected headers: %+v", request.Header)
		}
		if request.Method == http.MethodPost {
			mutationCount.Add(1)
			if request.Header.Get("X-Request-ID") == "" || request.Header.Get("Idempotency-Key") == "" || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("missing mutation headers: %+v", request.Header)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(response, `{"error_code":"TEMPORARY","message":"try later `+pat+`"}`)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"services":[{"id":"svc-1","project_id":"proj-1","status":"active"}]}`)
	}))
	defer server.Close()
	client, err := New(server.URL, pat, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	services, err := client.ListServices(context.Background(), "proj-1")
	if err != nil || len(services) != 1 || services[0].ID != "svc-1" {
		t.Fatalf("services=%+v err=%v", services, err)
	}
	_, err = client.ClaimRepository(context.Background(), "proj-1", 123)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != "TEMPORARY" || apiErr.Message != "try later [REDACTED]" {
		t.Fatalf("typed error=%#v", err)
	}
	if strings.Contains(err.Error(), pat) {
		t.Fatalf("PAT leaked in error: %v", err)
	}
	if mutationCount.Load() != 1 {
		t.Fatalf("mutation was retried %d times", mutationCount.Load())
	}
}

func TestResponseBoundAndHTMLNotReflected(t *testing.T) {
	t.Run("bounded", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(response, strings.Repeat("x", responseLimit+1))
		}))
		defer server.Close()
		client, _ := New(server.URL, "pat", "test", server.Client())
		if _, err := client.ListServices(context.Background(), "proj"); err == nil || !strings.Contains(err.Error(), "2 MiB") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("sanitized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(response, "<html>private upstream detail</html>")
		}))
		defer server.Close()
		client, _ := New(server.URL, "pat", "test", server.Client())
		_, err := client.ListServices(context.Background(), "proj")
		if err == nil || strings.Contains(err.Error(), "private upstream") {
			t.Fatalf("raw body reflected: %v", err)
		}
	})
}

func TestListNodesContractAndSafeDecodeFailures(t *testing.T) {
	const pat = "node-list-pat"
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantNodes   int
		wantErr     string
	}{
		{name: "empty envelope", status: http.StatusOK, contentType: "application/json", body: `{"nodes":[]}`},
		{name: "direct Agent metadata", status: http.StatusOK, contentType: "application/json", body: `{"nodes":[{"id":"node-1","project_id":"project-1","agent_id":"agent-1","agent_endpoint":"52.77.226.123","agent_port":9443,"agent_tls_server_name":"agent.example.test","agent_cert_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`, wantNodes: 1},
		{name: "missing envelope", status: http.StatusOK, contentType: "application/json", body: `{}`, wantErr: "unexpected response schema"},
		{name: "wrong nodes type", status: http.StatusOK, contentType: "application/json", body: `{"nodes":{}}`, wantErr: "unexpected response schema"},
		{name: "HTML response", status: http.StatusOK, contentType: "text/html; charset=utf-8", body: `<html>upstream ` + pat + `</html>`, wantErr: "invalid JSON (status 200, content-type \"text/html\")"},
		{name: "plain text response", status: http.StatusOK, contentType: "text/plain", body: "unavailable " + pat, wantErr: "invalid JSON (status 200, content-type \"text/plain\")"},
		{name: "malformed JSON", status: http.StatusOK, contentType: "application/json", body: `{"nodes":[`, wantErr: "invalid JSON"},
		{name: "truncated JSON", status: http.StatusOK, contentType: "application/json", body: `{"nodes":[{"id":"node-1"}`, wantErr: "invalid JSON"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", test.contentType)
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))
			defer server.Close()

			client, err := New(server.URL, pat, "test", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := client.ListNodes(context.Background(), "project-1")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) || strings.Contains(err.Error(), pat) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || len(nodes) != test.wantNodes {
				t.Fatalf("nodes=%+v err=%v", nodes, err)
			}
			if test.wantNodes == 1 {
				node := nodes[0]
				if node.AgentID != "agent-1" || node.AgentEndpoint != "52.77.226.123" || node.AgentPort != 9443 || node.AgentTLSServerName != "agent.example.test" || node.AgentCertSHA256 != strings.Repeat("a", 64) {
					t.Fatalf("direct Agent metadata changed: %+v", node)
				}
			}
		})
	}
}

func TestListNodesHTTPStatusesReturnTypedErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/plain")
				response.WriteHeader(status)
				_, _ = io.WriteString(response, "private failure")
			}))
			defer server.Close()
			client, err := New(server.URL, "pat", "test", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ListNodes(context.Background(), "project-1")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != status || strings.Contains(err.Error(), "private failure") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestListNodesAgainstExternalHandler(t *testing.T) {
	rawURL := os.Getenv("OPSI_CLOUDCLIENT_CONTRACT_URL")
	projectID := os.Getenv("OPSI_CLOUDCLIENT_CONTRACT_PROJECT_ID")
	agentID := os.Getenv("OPSI_CLOUDCLIENT_CONTRACT_AGENT_ID")
	if rawURL == "" || projectID == "" || agentID == "" {
		t.Skip("run by the Cloud handler-to-CLI-client contract test")
	}
	client, err := New(rawURL, "contract-pat", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := client.ListNodes(context.Background(), projectID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	node := nodes[0]
	if node.AgentID != agentID || node.AgentEndpoint != "52.77.226.123" || node.AgentPort != 9443 || node.AgentTLSServerName != "52.77.226.123" || node.AgentCertSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("direct Agent metadata changed: %+v", node)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls.Add(1)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, destination.URL, http.StatusFound)
	}))
	defer source.Close()
	client, _ := New(source.URL, "pat", "test", &http.Client{Timeout: time.Second})
	_, err := client.ListServices(context.Background(), "proj")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("redirect error=%v", err)
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("redirect destination was called")
	}
}

type captureTransport struct {
	request *http.Request
}

func (c *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	c.request = request
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"services":[]}`)), Request: request}, nil
}

func TestPathSegmentsAreEscaped(t *testing.T) {
	transport := &captureTransport{}
	client, err := New("https://cloud.example.test/base/", "pat", "test", &http.Client{Transport: transport, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListServices(context.Background(), "project/with space"); err != nil {
		t.Fatal(err)
	}
	if got := transport.request.URL.EscapedPath(); got != "/base/api/projects/project%2Fwith%20space/services" {
		t.Fatalf("escaped path=%q", got)
	}
}

func TestTransportErrorCannotLeakPAT(t *testing.T) {
	const pat = "transport-secret-pat"
	client, err := New("https://cloud.example.test", pat, "test", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport included " + pat)
	}), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListServices(context.Background(), "proj")
	if err == nil || strings.Contains(err.Error(), pat) {
		t.Fatalf("PAT leaked from transport error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
