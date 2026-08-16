package restorev1

import (
	"strings"
	"testing"
	"time"
)

func TestRestoreSuccessRequiresVerificationAndReviewPristineEvidence(t *testing.T) {
	now := time.Now().UTC()
	v := Restore{SchemaVersion: SchemaVersion, ID: "rst-1", SourceResourceID: "source", TargetResourceID: "target", TargetNodeID: "node", Lifecycle: LifecycleSucceeded, CompletedAt: &now, VerifyingAt: &now, ArchiveVerified: true, PGRestoreVersion: "pg_restore (PostgreSQL) 18.6", ArtifactSHA256: strings.Repeat("a", 64), VerificationMetadata: map[string]string{"connectivity": "authenticated", "transaction": "committed"}}
	if err := v.ValidateSucceeded(); err != nil {
		t.Fatal(err)
	}
	v.ArchiveVerified = false
	if v.ValidateSucceeded() == nil {
		t.Fatal("unverified restore accepted")
	}
	r := Review{SchemaVersion: SchemaVersion, ID: "review-1", BackupID: "b", TargetResourceID: "target", TargetNodeID: "node", SourceResourceID: "source", Lifecycle: ReviewSucceeded, Pristine: true, Objects: ObjectSummary{Schemas: 1}, ReviewedAt: &now, TargetSpecHash: strings.Repeat("a", 64), TargetPVCUID: "pvc", TargetDatabase: "opsi", TargetDatabaseOID: "42"}
	r.PristineEvidenceHash = PristineEvidenceHash(r)
	if err := r.ValidateSucceeded(); err != nil {
		t.Fatal(err)
	}
	r.Objects.Tables = 1
	if r.ValidateSucceeded() == nil {
		t.Fatal("non-pristine review accepted")
	}
}
