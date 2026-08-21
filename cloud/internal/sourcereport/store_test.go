package sourcereport

import (
	"context"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/sourcescanner"
)

func TestMemoryStore(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	report := Report{
		ProjectID:       "proj-1",
		ApplicationID:   "app-1",
		RepositoryID:    42,
		CommitSHA:       "43a701b3b2f3ade736a7b064183f37da70c78fe4",
		ApplicationRoot: ".",
		ScannerVersion:  sourcescanner.ScannerVersion,
		BuildJobID:      "bj-123",
		AnalysisStatus:  sourcescanner.AnalysisStatusComplete,
		Findings: []sourcescanner.Finding{
			{
				FindingID:    "f1",
				RuleID:       sourcescanner.RuleLoopbackEndpoint,
				Severity:     sourcescanner.SeverityWarn,
				Confidence:   sourcescanner.ConfidenceHigh,
				File:         "app.js",
				Line:         10,
				SafeEvidence: "http://localhost:8080",
			},
		},
		ReportHash: "hash-123",
		CreatedAt:  time.Now().UTC(),
	}

	saved, isNew, err := store.Upsert(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Fatal("expected isNew to be true")
	}
	if saved.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	// Fetch latest
	fetched, err := store.GetLatest(ctx, "proj-1", "app-1", 42, "43a701b3b2f3ade736a7b064183f37da70c78fe4", ".", sourcescanner.ScannerVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched.Findings) != 1 || fetched.Findings[0].RuleID != sourcescanner.RuleLoopbackEndpoint {
		t.Fatalf("unexpected fetched findings: %+v", fetched.Findings)
	}

	// Fetch by BuildJobID
	byJob, err := store.GetForBuildJob(ctx, "bj-123")
	if err != nil {
		t.Fatal(err)
	}
	if byJob.ID != saved.ID {
		t.Fatalf("expected ID %s, got %s", saved.ID, byJob.ID)
	}

	// Fetch by Commit
	byCommit, err := store.GetForCommit(ctx, "proj-1", "app-1", "43a701b3b2f3ade736a7b064183f37da70c78fe4")
	if err != nil {
		t.Fatal(err)
	}
	if byCommit.ID != saved.ID {
		t.Fatalf("expected ID %s, got %s", saved.ID, byCommit.ID)
	}

	// Not found
	_, err = store.GetForCommit(ctx, "proj-1", "app-1", "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
