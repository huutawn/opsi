package telemetry

import (
	"context"
	"testing"
	"time"
)

func TestBuildChunksCompressesDeltaRecords(t *testing.T) {
	observed := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	records := []SyncRecord{
		{Kind: "metric", Metric: &MetricRecord{ProjectID: "proj", NodeID: "node", Name: "cpu", Value: 1, Unit: "cores", ObservedAt: observed}, ObservedAt: observed},
		{Kind: "metric", Metric: &MetricRecord{ProjectID: "proj", NodeID: "node", Name: "mem", Value: 2, Unit: "bytes", ObservedAt: observed.Add(time.Second)}, ObservedAt: observed.Add(time.Second)},
	}
	chunks, err := BuildChunks(context.Background(), "proj", records, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || !chunks[0].Done || chunks[0].Compression != "zstd" || chunks[0].ChecksumSHA256 == "" {
		t.Fatalf("unexpected chunk: %+v", chunks)
	}
	decoded, err := DecodeChunkPayload(chunks[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || decoded[0]["kind"] != "metric" {
		t.Fatalf("unexpected decoded payload: %+v", decoded)
	}
}

func TestFingerprintNormalizesVolatileTokens(t *testing.T) {
	left := Fingerprint("Pod 123 failed with id abcdef123456")
	right := Fingerprint("pod 456 failed with id ffffff999999")
	if left != right {
		t.Fatalf("expected same fingerprint: %s != %s", left, right)
	}
}
