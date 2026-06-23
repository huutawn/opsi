package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCAdvisorCollectorMapsContainerStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1.3/subcontainers" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
{"spec":{"labels":{"opsi.dev/project-id":"proj","opsi.dev/service-id":"svc","io.kubernetes.pod.name":"pod-1"}},"stats":[{"timestamp":"2026-06-23T00:00:00Z","cpu":{"usage":{"total":2000000000}},"memory":{"usage":1048576},"diskio":{"io_service_bytes":[{"stats_type":"Read","value":10}]},"network":{"interfaces":[{"rx_bytes":20,"tx_bytes":30}]}}]}
]`))
	}))
	defer server.Close()

	collector := CAdvisorCollector{Endpoint: server.URL, NodeID: "node-1", Timeout: time.Second}
	metrics, logs, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected no logs, got %d", len(logs))
	}
	if len(metrics) != 5 {
		t.Fatalf("expected 5 metrics, got %d: %+v", len(metrics), metrics)
	}
	wantNames := map[string]bool{"container.cpu_usage_seconds_total": true, "container.memory_usage": true, "container.diskio_read_bytes": true, "container.network_rx_bytes": true, "container.network_tx_bytes": true}
	for _, metric := range metrics {
		if !wantNames[metric.Name] {
			t.Fatalf("unexpected metric %s", metric.Name)
		}
		if metric.ProjectID != "proj" || metric.ServiceID != "svc" || metric.PodID != "pod-1" || metric.NodeID != "node-1" {
			t.Fatalf("unexpected scope: %+v", metric)
		}
	}
}
