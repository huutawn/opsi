package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnalyzeIncidentFallbackContract(t *testing.T) {
	server := NewServer(Config{})
	body := []byte(`{"schema_version":"opsi.incident_context.v1","incident_id":"inc-1","project_id":"p1","service_id":"svc","anomaly_type":"cpu"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ai/incidents/analyze", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		SchemaVersion      string `json:"schema_version"`
		IncidentID         string `json:"incident_id"`
		RecommendedActions []any  `json:"recommended_actions"`
		Metadata           struct {
			Provider string `json:"provider"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.SchemaVersion != "opsi.rca.v1" || out.IncidentID != "inc-1" || len(out.RecommendedActions) == 0 || out.Metadata.Provider != "fixture" {
		t.Fatalf("bad response: %+v", out)
	}
}

func TestAnalyzeIncidentRejectsSecretLikePayload(t *testing.T) {
	server := NewServer(Config{})
	body := []byte(`{"schema_version":"opsi.incident_context.v1","incident_id":"inc-1","project_id":"p1","password":"leak"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ai/incidents/analyze", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnalyzeIncidentGeminiProviderUsesConfiguredEndpoint(t *testing.T) {
	t.Setenv("GEMINI_TEST_KEY", "test-key")
	gemini := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/test-model:generateContent" || r.URL.Query().Get("key") != "test-key" {
			http.Error(w, "bad Gemini request: "+r.URL.String(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]string{{"text": "CPU saturated after the latest deployment."}}}}}})
	}))
	defer gemini.Close()
	server := NewServer(Config{AI: AIConfig{Provider: "gemini", APIKeyEnv: "GEMINI_TEST_KEY", Model: "test-model", Endpoint: gemini.URL, Timeout: Duration(time.Second)}})
	body := []byte(`{"schema_version":"opsi.incident_context.v1","incident_id":"inc-1","project_id":"p1","service_id":"svc","anomaly_type":"cpu"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ai/incidents/analyze", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		RootCause string `json:"root_cause"`
		Metadata  struct {
			Provider     string `json:"provider"`
			FallbackUsed bool   `json:"fallback_used"`
			Model        string `json:"model"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Metadata.Provider != "gemini" || out.Metadata.FallbackUsed || out.Metadata.Model != "test-model" || !strings.Contains(out.RootCause, "CPU saturated") {
		t.Fatalf("bad Gemini response: %+v", out)
	}
}

func TestAnalyzeIncidentGeminiProviderFallsBackWhenAllowed(t *testing.T) {
	server := NewServer(Config{AI: AIConfig{Provider: "gemini", APIKeyEnv: "MISSING_GEMINI_KEY", FallbackFixture: true}})
	body := []byte(`{"schema_version":"opsi.incident_context.v1","incident_id":"inc-1","project_id":"p1","service_id":"svc","anomaly_type":"cpu"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ai/incidents/analyze", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Metadata struct {
			Provider           string `json:"provider"`
			ConfiguredProvider string `json:"configured_provider"`
			FallbackUsed       bool   `json:"fallback_used"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Metadata.Provider != "fixture" || out.Metadata.ConfiguredProvider != "gemini" || !out.Metadata.FallbackUsed {
		t.Fatalf("bad fallback response: %+v", out)
	}
}
