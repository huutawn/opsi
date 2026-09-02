package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCalculatePercentile verifies exact and interpolated percentiles.
func TestCalculatePercentile(t *testing.T) {
	tests := []struct {
		name     string
		values   []float64
		p        float64
		expected float64
	}{
		{"empty", []float64{}, 50, 0.0},
		{"single value p50", []float64{10.0}, 50, 10.0},
		{"single value p99", []float64{10.0}, 99, 10.0},
		{"linear 1 to 5 p50", []float64{1.0, 2.0, 3.0, 4.0, 5.0}, 50, 3.0},
		{"linear 1 to 5 p0", []float64{1.0, 2.0, 3.0, 4.0, 5.0}, 0, 1.0},
		{"linear 1 to 5 p100", []float64{1.0, 2.0, 3.0, 4.0, 5.0}, 100, 5.0},
		{"linear 1 to 4 p50", []float64{10.0, 20.0, 30.0, 40.0}, 50, 25.0},
		{"linear 1 to 100 p95", func() []float64 {
			v := make([]float64, 101)
			for i := 0; i <= 100; i++ {
				v[i] = float64(i)
			}
			return v
		}(), 95, 95.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CalculatePercentile(tc.values, tc.p)
			if got != tc.expected {
				t.Errorf("CalculatePercentile() = %v, expected %v", got, tc.expected)
			}
		})
	}
}

// TestRegistrationEmailUniqueness verifies concurrent unique email generation.
func TestRegistrationEmailUniqueness(t *testing.T) {
	client := NewProbeClient("http://localhost:8080", "testrun123", 5*time.Second)
	numGoroutines := 10
	numPerGoroutine := 100
	total := numGoroutines * numPerGoroutine

	var wg sync.WaitGroup
	emailMap := sync.Map{}

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < numPerGoroutine; i++ {
				email, seq := client.GenerateRegistrationEmail()
				if !strings.HasPrefix(email, "opsi-latency-testrun123-") || !strings.HasSuffix(email, "@example.invalid") {
					t.Errorf("Invalid email format: %s", email)
				}
				if seq == 0 {
					t.Errorf("Sequence cannot be zero")
				}
				if _, exists := emailMap.LoadOrStore(email, true); exists {
					t.Errorf("Duplicate email generated: %s", email)
				}
			}
		}()
	}

	wg.Wait()

	count := 0
	emailMap.Range(func(k, v interface{}) bool {
		count++
		return true
	})

	if count != total {
		t.Errorf("Expected %d unique emails, got %d", total, count)
	}
}

// TestRedactErrorMessage verifies passwords and tokens are never leaked into logs.
func TestRedactErrorMessage(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"connection reset by peer", "connection reset by peer"},
		{"failed with Password: secret123!", "[REDACTED_ERROR_MESSAGE]"},
		{"Bearer eyJhbGciOiJIUzI1NiJ9 invalid", "[REDACTED_ERROR_MESSAGE]"},
		{"Token verification failed", "[REDACTED_ERROR_MESSAGE]"},
		{"", ""},
	}

	for _, c := range cases {
		got := RedactErrorMessage(c.input)
		if got != c.expected {
			t.Errorf("RedactErrorMessage(%q) = %q, expected %q", c.input, got, c.expected)
		}
	}
}

// TestClassifyError verifies error categorization.
func TestClassifyError(t *testing.T) {
	cases := []struct {
		err        error
		status     int
		expectedCl string
	}{
		{nil, 200, ""},
		{nil, 201, ""},
		{nil, 307, ""},
		{nil, 400, "http_4xx"},
		{nil, 401, "http_4xx"},
		{nil, 500, "http_5xx"},
		{nil, 503, "http_5xx"},
		{context.Canceled, 0, "canceled"},
		{fmt.Errorf("dial tcp 10.0.0.1: i/o timeout"), 0, "timeout"},
		{fmt.Errorf("dial tcp: lookup nonexistent.invalid: no such host"), 0, "dns_error"},
		{fmt.Errorf("dial tcp 127.0.0.1:80: connect: connection refused"), 0, "connect_error"},
		{fmt.Errorf("remote error: tls: handshake failure"), 0, "tls_error"},
	}

	for _, c := range cases {
		got := ClassifyError(c.err, c.status)
		if got != c.expectedCl {
			t.Errorf("ClassifyError(%v, %d) = %q, expected %q", c.err, c.status, got, c.expectedCl)
		}
	}
}

// TestFakeServerEndToEnd simulates full HTTP contracts and scenario execution.
func TestFakeServerEndToEnd(t *testing.T) {
	// Setup mock server
	mux := http.NewServeMux()

	// 1. GET / -> 307 to /login
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/login" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html><body>Login Page</body></html>"))
			return
		}
		http.NotFound(w, r)
	})

	// 2. POST /api/auth/login
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if payload["Email"] == "" || payload["Password"] == "" {
			http.Error(w, "missing credentials", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"accessToken":             "mock-access-token-12345",
			"accessTokenExpiresAtUtc": time.Now().Add(15 * time.Minute).Format(time.RFC3339),
			"refreshToken":            "mock-refresh-token-67890",
			"refreshTokenExpiresAtUtc": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	})

	// 3. POST /api/auth/register
	mux.HandleFunc("/api/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if payload["Email"] == "" || payload["Password"] == "" || payload["DisplayName"] == "" {
			http.Error(w, "missing fields", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":           "usr-mock-1234",
			"principalId":  "prn-mock-5678",
			"email":        payload["Email"],
			"displayName":  payload["DisplayName"],
			"createdAtUtc": time.Now().UTC().Format(time.RFC3339),
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	probeClient := NewProbeClient(server.URL, "fake-run-001", 5*time.Second)

	// Test 1: ProbeRoot
	rootMetrics, err := probeClient.ProbeRoot(context.Background())
	if err != nil {
		t.Fatalf("ProbeRoot failed: %v", err)
	}
	if len(rootMetrics) != 3 {
		t.Fatalf("Expected 3 root metrics (step1, step2, composite), got %d", len(rootMetrics))
	}
	if rootMetrics[0].StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("Step 1 status code = %d, expected 307", rootMetrics[0].StatusCode)
	}
	if rootMetrics[1].StatusCode != http.StatusOK {
		t.Errorf("Step 2 status code = %d, expected 200", rootMetrics[1].StatusCode)
	}

	// Test 2: ProbeLogin
	loginMetric, err := probeClient.ProbeLogin(context.Background(), "user@example.invalid", "P@ssword123!")
	if err != nil {
		t.Fatalf("ProbeLogin failed: %v", err)
	}
	if loginMetric.StatusCode != http.StatusOK {
		t.Errorf("Login status code = %d, expected 200", loginMetric.StatusCode)
	}

	// Test 3: ProbeRegister
	registerMetric, err := probeClient.ProbeRegister(context.Background(), "P@ssword123!")
	if err != nil {
		t.Fatalf("ProbeRegister failed: %v", err)
	}
	if registerMetric.StatusCode != http.StatusCreated {
		t.Errorf("Register status code = %d, expected 201", registerMetric.StatusCode)
	}

	// Test 4: Runner integration with low duration (100ms per level)
	config := ScenarioConfig{
		Name:            "test_runner",
		WarmupRequests:  2,
		VULevels:        []int{1, 2},
		LevelDuration:   200 * time.Millisecond,
		SeedEmail:       "seed@example.invalid",
		SeedPassword:    "P@ssword123!",
		EarlyStop5xxPct: 2.0,
		EarlyStopP95Ms:  5000.0,
	}
	runner := NewBenchmarkRunner(probeClient, config)

	res, err := runner.RunScenario(context.Background(), "post_login")
	if err != nil {
		t.Fatalf("RunScenario post_login failed: %v", err)
	}
	if len(res.WarmupMetrics) != 2 {
		t.Errorf("Expected 2 warmup metrics, got %d", len(res.WarmupMetrics))
	}
	if len(res.LevelSummaries) == 0 {
		t.Errorf("Expected non-empty level summaries")
	}
}

// TestEarlyStopOn5xx verifies runner aborts when 5xx errors exceed threshold.
func TestEarlyStopOn5xx(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	probeClient := NewProbeClient(server.URL, "fake-run-5xx", 2*time.Second)
	config := ScenarioConfig{
		Name:            "test_5xx_stop",
		WarmupRequests:  1,
		VULevels:        []int{1},
		LevelDuration:   5 * time.Second,
		SeedEmail:       "seed@example.invalid",
		SeedPassword:    "P@ssword123!",
		EarlyStop5xxPct: 2.0,
		EarlyStopP95Ms:  5000.0,
	}
	runner := NewBenchmarkRunner(probeClient, config)

	res, err := runner.RunScenario(context.Background(), "post_login")
	if err == nil {
		t.Errorf("Expected EarlyStopError due to 5xx, got nil error")
	}
	if res.EarlyStopReason == "" {
		t.Errorf("Expected EarlyStopReason to be populated, got empty string")
	}
}
