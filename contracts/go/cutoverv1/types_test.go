package cutoverv1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

func TestCutoverReviewValidationAndEvidenceHash(t *testing.T) {
	now := time.Now().UTC()
	review := ApplicationCutoverReview{
		SchemaVersion:             SchemaVersion,
		ID:                        "acrv-1",
		ProjectID:                 "proj-1",
		EnvironmentID:             "env-1",
		ApplicationID:             "app-1",
		SourceBindingID:           "bind-src",
		SourceResourceID:          "res-src",
		TargetResourceID:          "res-tgt",
		TargetBindingID:           "bind-tgt",
		ApplicationConfigRevision: 1,
		ApplicationConfigHash:     strings.Repeat("a", 64),
		SourceBindingRevision:     strings.Repeat("b", 64),
		TargetBindingRevision:     strings.Repeat("c", 64),
		SourceResourceRevision:    1,
		SourceResourceSpecHash:    strings.Repeat("d", 64),
		TargetResourceRevision:    1,
		TargetResourceSpecHash:    strings.Repeat("e", 64),
		TargetRestoreID:           "rst-1",
		TargetRestoreRevision:     strings.Repeat("f", 64),
		BackupID:                  "bak-1",
		BackupCompletedAt:         &now,
		RestoreCompletedAt:        &now,
		BackupAgeSeconds:          120,
		ValidationSummary: ValidationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			SourceBindingReady:   true,
			TargetBindingReady:   true,
			TargetRestoreReady:   true,
			TargetPVCUID:         "pvc-uid-1",
			TargetPVUID:          "pv-uid-1",
			TargetStorageHash:    strings.Repeat("9", 64),
		},
		Warnings:    []string{WarningNotContinuouslySynchronized, WarningBackupAgeNonZero},
		Lifecycle:   ReviewSucceeded,
		RequestedBy: "user-1",
		RequestedAt: now,
		ReviewedAt:  &now,
	}
	review.EvidenceHash = EvidenceHash(review)

	if err := review.ValidateSucceeded(); err != nil {
		t.Fatalf("expected valid succeeded review: %v", err)
	}

	// Tampered evidence hash
	reviewBadHash := review
	reviewBadHash.EvidenceHash = "badhash"
	if err := reviewBadHash.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with tampered evidence hash")
	}

	// Same source and target
	reviewSame := review
	reviewSame.TargetBindingID = reviewSame.SourceBindingID
	if err := reviewSame.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with same source and target binding")
	}

	// Failed preflight
	reviewFailedPreflight := review
	reviewFailedPreflight.ValidationSummary.TargetSQLPreflight = "FAILED"
	reviewFailedPreflight.EvidenceHash = EvidenceHash(reviewFailedPreflight)
	if err := reviewFailedPreflight.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with failed preflight")
	}
}

func TestCutoverReviewNoBearerTokenOrSecretsInJSON(t *testing.T) {
	now := time.Now().UTC()
	review := ApplicationCutoverReview{
		SchemaVersion:   SchemaVersion,
		ID:              "acrv-1",
		ProjectID:       "proj-1",
		EnvironmentID:   "env-1",
		ApplicationID:   "app-1",
		SourceBindingID: "bind-src",
		SourceResourceID: "res-src",
		TargetResourceID: "res-tgt",
		TargetBindingID: "bind-tgt",
		Lifecycle:       ReviewSucceeded,
		RequestedBy:     "user-1",
		RequestedAt:     now,
		ReviewedAt:      &now,
		LeaseToken:      "secret-lease-token",
		LeaseExpiresAt:  now.Add(time.Hour),
	}

	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)

	// Ensure lease token is omitted from JSON serialization
	if strings.Contains(serialized, "secret-lease-token") {
		t.Fatal("lease token must not be serialized to JSON")
	}

	// Ensure no review_token or ReviewToken fields exist
	if strings.Contains(serialized, "review_token") || strings.Contains(serialized, "ReviewToken") {
		t.Fatal("bearer review token field must not exist in review")
	}

	// Ensure no passwords or database URL credentials
	for _, forbidden := range []string{"password", "secret", "bearer", "token"} {
		if strings.Contains(strings.ToLower(serialized), `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q found in review JSON", forbidden)
		}
	}
}

func TestBindingAndRestoreRevisions(t *testing.T) {
	b := resourcev1.Binding{
		ID:            "bind-1",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		LogicalName:   "DATABASE",
		Protocol:      resourcev1.ProtocolPostgres,
		Lifecycle:     resourcev1.LifecycleReady,
		RoleName:      "rb_user",
		Database:      "opsi",
		CredentialID:  "cred-1",
	}
	rev1 := BindingRevision(b)
	if len(rev1) != 64 {
		t.Fatalf("expected 64 char hex hash, got %q", rev1)
	}

	b2 := b
	b2.RoleName = "rb_user_2"
	rev2 := BindingRevision(b2)
	if rev1 == rev2 {
		t.Fatal("expected different revision after role modification")
	}

	rst := restorev1.Restore{
		ID:                   "rst-1",
		ProjectID:            "proj-1",
		EnvironmentID:        "env-1",
		BackupID:             "bak-1",
		TargetResourceID:     "tgt-1",
		ArtifactSHA256:       strings.Repeat("a", 64),
		Lifecycle:            restorev1.LifecycleSucceeded,
		PristineEvidenceHash: strings.Repeat("b", 64),
	}
	rstRev1 := RestoreRevision(rst)
	if len(rstRev1) != 64 {
		t.Fatalf("expected 64 char hex hash, got %q", rstRev1)
	}
}

func TestValidFailureCodes(t *testing.T) {
	if !ValidFailure(FailureSourceBindingInvalid) {
		t.Fatal("expected FailureSourceBindingInvalid to be valid")
	}
	if !ValidFailure(FailureStaleReview) {
		t.Fatal("expected FailureStaleReview to be valid")
	}
	if ValidFailure("UNKNOWN_FAILURE_CODE") {
		t.Fatal("expected UNKNOWN_FAILURE_CODE to be invalid")
	}
}
