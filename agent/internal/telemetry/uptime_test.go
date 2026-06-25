package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSyntheticCheckerCreatesP1WhenExternalFailsInternalPasses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer server.Close()
	store, err := OpenSQLiteStore(t.TempDir() + "/telemetry.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	checker := SyntheticChecker{Store: store, Now: func() time.Time { return now }}
	incident, err := checker.Check(context.Background(), SyntheticTarget{ProjectID: "proj", ServiceID: "svc", PublicURL: server.URL, InternalReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if incident == nil || incident.Severity != "P1" || incident.AnomalyType != "external_health_check_failed" {
		t.Fatalf("unexpected incident: %+v", incident)
	}
	percent, err := store.UptimePercent(context.Background(), "proj", "svc", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if percent != 0 {
		t.Fatalf("expected failed uptime, got %f", percent)
	}
}

func TestSyntheticCheckerDoesNotIncidentWhenInternalAlsoFails(t *testing.T) {
	checker := SyntheticChecker{HTTPClient: http.DefaultClient}
	incident, _ := checker.Check(context.Background(), SyntheticTarget{ProjectID: "proj", ServiceID: "svc", PublicURL: "http://127.0.0.1:1", InternalReady: false})
	if incident != nil {
		t.Fatalf("unexpected incident: %+v", incident)
	}
}
