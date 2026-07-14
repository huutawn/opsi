package cloudclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
