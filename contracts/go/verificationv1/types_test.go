package verificationv1

import (
	"testing"
)

func TestVerificationConstants(t *testing.T) {
	if SchemaVersion != "opsi.verification/v1" {
		t.Fatalf("unexpected SchemaVersion: %s", SchemaVersion)
	}

	if RunStatusVerified != "VERIFIED" {
		t.Fatalf("unexpected RunStatusVerified: %s", RunStatusVerified)
	}
	if RunStatusPartiallyVerified != "PARTIALLY_VERIFIED" {
		t.Fatalf("unexpected RunStatusPartiallyVerified: %s", RunStatusPartiallyVerified)
	}
	if RunStatusFailed != "FAILED" {
		t.Fatalf("unexpected RunStatusFailed: %s", RunStatusFailed)
	}
	if RunStatusStale != "STALE" {
		t.Fatalf("unexpected RunStatusStale: %s", RunStatusStale)
	}

	if LayerStatusHealthy != "HEALTHY" {
		t.Fatalf("unexpected LayerStatusHealthy: %s", LayerStatusHealthy)
	}
	if LayerStatusResolved != "RESOLVED" {
		t.Fatalf("unexpected LayerStatusResolved: %s", LayerStatusResolved)
	}
	if LayerStatusVerified != "VERIFIED" {
		t.Fatalf("unexpected LayerStatusVerified: %s", LayerStatusVerified)
	}
	if LayerStatusNotConfigured != "NOT_CONFIGURED" {
		t.Fatalf("unexpected LayerStatusNotConfigured: %s", LayerStatusNotConfigured)
	}
}
