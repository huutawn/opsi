package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
