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
	if !ValidFailure(FailureReviewNotReady) {
		t.Fatal("expected FailureReviewNotReady to be valid")
	}
	if !ValidFailure(FailureCutoverAlreadyRunning) {
		t.Fatal("expected FailureCutoverAlreadyRunning to be valid")
	}
	if ValidFailure("UNKNOWN_FAILURE_CODE") {
		t.Fatal("expected UNKNOWN_FAILURE_CODE to be invalid")
	}
}

func TestApplicationCutoverValidationAndEvidenceHash(t *testing.T) {
	now := time.Now().UTC()
	cutover := ApplicationCutover{
		SchemaVersion:                       CutoverSchemaVersion,
		ID:                                  "acut-1",
		ProjectID:                           "proj-1",
		EnvironmentID:                       "env-1",
		ApplicationID:                       "app-1",
		CutoverReviewID:                     "acrv-1",
		SourceBindingID:                     "bind-src",
		TargetBindingID:                     "bind-tgt",
		SourceResourceID:                    "res-src",
		TargetResourceID:                    "res-tgt",
		ReviewedApplicationConfigRevision:   1,
		ReviewedApplicationConfigHash:       strings.Repeat("a", 64),
		PreCutoverApplicationConfigRevision: 1,
		PreCutoverApplicationConfigHash:     strings.Repeat("a", 64),
		ResultingApplicationConfigRevision:  2,
		ResultingApplicationConfigHash:      strings.Repeat("b", 64),
		PreCutoverDeploymentJobID:           "dep-1",
		PreCutoverBuildRecordID:             "br-1",
		PreCutoverImageDigest:               strings.Repeat("c", 64),
		DeploymentJobID:                     "dep-2",
		Lifecycle:                           CutoverSucceeded,
		RequestedBy:                         "user-1",
		RequestedAt:                         now,
		CompletedAt:                         &now,
		VerificationSummary: CutoverVerificationSummary{
			SourceSQLPreflight:       "PASS",
			TargetSQLPreflight:       "PASS",
			TargetRoleAttributes:     "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:          true,
			WorkloadReady:            true,
			TargetDBConnected:        true,
			RestoredDataVerified:     true,
			TargetOnlyMarkerPresent:  true,
			SourceOnlyMarkerAbsent:   true,
			PostCutoverTargetWritten: true,
			SourceRollbackPreserved:  true,
		},
	}
	cutover.EvidenceHash = CutoverEvidenceHash(cutover)

	if err := cutover.ValidateSucceeded(); err != nil {
		t.Fatalf("expected valid succeeded cutover: %v", err)
	}

	// Tampered evidence hash
	badHash := cutover
	badHash.EvidenceHash = "badhash"
	if err := badHash.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with tampered evidence hash")
	}

	// Incomplete verification
	unverified := cutover
	unverified.VerificationSummary.TargetDBConnected = false
	unverified.EvidenceHash = CutoverEvidenceHash(unverified)
	if err := unverified.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with incomplete verification")
	}

	// Check serialization security
	data, err := json.Marshal(cutover)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, forbidden := range []string{"password", "secret", "bearer", "token"} {
		if strings.Contains(strings.ToLower(serialized), `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q found in cutover JSON", forbidden)
		}
	}
}

func TestApplicationCutoverRollbackEvidenceValidation(t *testing.T) {
	now := time.Now().UTC()
	rollback := ApplicationCutoverRollback{
		SchemaVersion:                               RollbackSchemaVersion,
		ID:                                          "acrb-1",
		ProjectID:                                   "proj-1",
		EnvironmentID:                               "env-1",
		ApplicationID:                               "app-1",
		CutoverID:                                   "acut-1",
		SourceBindingID:                             "bind-source",
		TargetBindingID:                             "bind-target",
		SourceResourceID:                            "res-source",
		TargetResourceID:                            "res-target",
		CurrentApplicationConfigRevision:            2,
		CurrentApplicationConfigHash:                strings.Repeat("b", 64),
		OriginalPreCutoverApplicationConfigRevision: 1,
		OriginalPreCutoverApplicationConfigHash:     strings.Repeat("a", 64),
		ResultingApplicationConfigRevision:          3,
		ResultingApplicationConfigHash:              strings.Repeat("c", 64),
		DeploymentJobID:                             "dep-3",
		Lifecycle:                                   RollbackSucceeded,
		RequestedBy:                                 "user-1",
		RequestedAt:                                 now,
		CompletedAt:                                 &now,
		Warnings:                                    []string{WarningTargetWritesMayNotBeOnSource},
		VerificationSummary: RollbackVerificationSummary{
			SourceSQLPreflight:        "PASS",
			TargetSQLPreflight:        "PASS",
			SourceRoleAttributes:      "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:           true,
			WorkloadReady:             true,
			SourceDBConnected:         true,
			SourceMarkerPresent:       true,
			TargetMarkerAbsent:        true,
			PostRollbackSourceWritten: true,
			TargetAuthorityPreserved:  true,
		},
	}
	rollback.EvidenceHash = RollbackEvidenceHash(rollback)

	if err := rollback.ValidateSucceeded(); err != nil {
		t.Fatalf("expected valid succeeded rollback: %v", err)
	}

	// Tampered evidence hash
	badHash := rollback
	badHash.EvidenceHash = "badhash"
	if err := badHash.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with tampered evidence hash")
	}

	// Incomplete verification
	unverified := rollback
	unverified.VerificationSummary.SourceDBConnected = false
	unverified.EvidenceHash = RollbackEvidenceHash(unverified)
	if err := unverified.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with incomplete verification")
	}

	// Check serialization security
	data, err := json.Marshal(rollback)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, forbidden := range []string{"password", "secret", "bearer", "token"} {
		if strings.Contains(strings.ToLower(serialized), `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q found in rollback JSON", forbidden)
		}
	}
}

func TestApplicationCutoverFinalizationEvidenceValidation(t *testing.T) {
	now := time.Now().UTC()
	finalization := ApplicationCutoverFinalization{
		SchemaVersion:             FinalizationSchemaVersion,
		ID:                        "acfn-1",
		ProjectID:                 "proj-1",
		EnvironmentID:             "env-1",
		ApplicationID:             "app-1",
		CutoverID:                 "acut-1",
		SourceBindingID:           "bind-source",
		TargetBindingID:           "bind-target",
		SourceResourceID:          "res-source",
		TargetResourceID:          "res-target",
		ApplicationConfigRevision: 2,
		ApplicationConfigHash:     strings.Repeat("b", 64),
		CutoverEvidenceHash:       strings.Repeat("c", 64),
		Lifecycle:                 FinalizationSucceeded,
		RequestedBy:               "user-1",
		RequestedAt:               now,
		CompletedAt:               &now,
		VerificationSummary: FinalizationVerificationSummary{
			TargetSQLPreflight:        "PASS",
			TargetRoleAttributes:      "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			TargetDBConnected:         true,
			TargetOnlyMarkerPresent:   true,
			PostCutoverMarkerPresent:  true,
			SourceMarkerAbsent:        true,
			SourceBindingRevoked:      true,
			SourceCredentialRejected:  true,
			SourceResourceRetained:    true,
			PostFinalizeTargetWritten: true,
		},
	}
	finalization.EvidenceHash = FinalizationEvidenceHash(finalization)

	if err := finalization.ValidateSucceeded(); err != nil {
		t.Fatalf("expected valid succeeded finalization: %v", err)
	}

	// Tampered evidence hash
	badHash := finalization
	badHash.EvidenceHash = "badhash"
	if err := badHash.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with tampered evidence hash")
	}

	// Incomplete verification
	unverified := finalization
	unverified.VerificationSummary.SourceBindingRevoked = false
	unverified.EvidenceHash = FinalizationEvidenceHash(unverified)
	if err := unverified.ValidateSucceeded(); err == nil {
		t.Fatal("expected error with incomplete verification")
	}

	// Check serialization security (no secrets)
	data, err := json.Marshal(finalization)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, forbidden := range []string{"password", "secret", "bearer", "token"} {
		if strings.Contains(strings.ToLower(serialized), `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q found in finalization JSON", forbidden)
		}
	}
}
