package telemetry

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeRunner map[string]string

func (f fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	return []byte(f[strings.Join(args, " ")]), nil
}

func TestKubernetesCollectorMapsPodMetricsAndLogs(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC)
	runner := fakeRunner{
		"get pods -A -o json":                   `{"items":[{"metadata":{"name":"api-123","namespace":"default","labels":{"opsi.dev/project-id":"proj","opsi.dev/service-id":"svc-api"}},"spec":{"nodeName":"node-a"}}]}`,
		"top pods -A --containers --no-headers": "default api-123 app 25m 64Mi\n",
		"logs -n default api-123 --all-containers=true --tail 10 --since 1m0s --timestamps": "2026-06-20T01:02:03Z ERROR failed to connect\n",
	}
	collector := KubernetesCollector{Runner: runner, LogTailLines: 10, LogSince: time.Minute, Now: func() time.Time { return now }}
	metrics, logs, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected cpu+memory metrics, got %+v", metrics)
	}
	if metrics[0].ProjectID != "proj" || metrics[0].ServiceID != "svc-api" || metrics[0].PodID != "api-123" || metrics[0].Value != 0.025 {
		t.Fatalf("unexpected cpu metric: %+v", metrics[0])
	}
	if metrics[1].Value != 64*1024*1024 {
		t.Fatalf("unexpected memory metric: %+v", metrics[1])
	}
	if len(logs) != 1 || logs[0].Level != "error" || logs[0].Message != "ERROR failed to connect" || !logs[0].Unread {
		t.Fatalf("unexpected logs: %+v", logs)
	}
}

func TestBuildChunksWithOptionsYieldsBetweenChunks(t *testing.T) {
	base := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	records := make([]SyncRecord, 0, 20)
	for i := 0; i < 20; i++ {
		observed := base.Add(time.Duration(i) * time.Second)
		records = append(records, SyncRecord{Kind: "metric", Metric: &MetricRecord{ProjectID: "proj", NodeID: "node", Name: "metric.with.large.payload", Value: float64(i), Unit: "count", ObservedAt: observed}, ObservedAt: observed})
	}
	started := time.Now()
	chunks, err := BuildChunksWithOptions(context.Background(), "proj", records, ChunkOptions{MaxBytes: 120, Yield: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	if time.Since(started) < 2*time.Millisecond {
		t.Fatal("expected chunk builder to yield")
	}
}
