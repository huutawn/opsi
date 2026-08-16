package cutover

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type fakeRunner struct {
	calls      [][]string
	failPing   bool
	failTarget bool
	badPrivs   bool
}

func (r *fakeRunner) Run(_ context.Context, _ io.Reader, output io.Writer, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	callNum := len(r.calls)
	switch callNum {
	case 1: // Source ping (SELECT 1)
		if r.failPing {
			return errors.New("source connection refused")
		}
		_, _ = io.WriteString(output, "1\n")
	case 2: // Target ping (SELECT 1)
		if r.failTarget {
			return errors.New("target connection refused")
		}
		_, _ = io.WriteString(output, "1\n")
	case 3: // Target privilege check
		if r.badPrivs {
			_, _ = io.WriteString(output, "1:1:0:0:0:0\n1:1:1\n") // rolsuper=1 (bad)
		} else {
			_, _ = io.WriteString(output, "1:0:0:0:0:0\n1:1:1\n")
		}
	}
	return nil
}

func TestCutoverExecutorReview(t *testing.T) {
	lease := cutoverv1.ReviewLease{
		LeaseToken: "lease-123",
		Review: cutoverv1.ApplicationCutoverReview{
			SchemaVersion:             cutoverv1.SchemaVersion,
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
			SourceResourceSpecHash:    strings.Repeat("d", 64),
			TargetResourceSpecHash:    strings.Repeat("e", 64),
			TargetRestoreID:           "rst-1",
			TargetRestoreRevision:     strings.Repeat("f", 64),
			Lifecycle:                 cutoverv1.ReviewLeased,
		},
		SourceSpec: resourcev1.ManagedResourceSpec{
			ResourceID: "res-src",
			Connection: resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-src"},
		},
		TargetSpec: resourcev1.ManagedResourceSpec{
			ResourceID: "res-tgt",
			Connection: resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-tgt"},
		},
		SourceCredential: &resourcev1.ManagedResourceCredential{
			CredentialID: "cred-src",
			Username:     "src_user",
			Password:     "src_pass",
			Database:     "opsi",
		},
		TargetCredential: &resourcev1.ManagedResourceCredential{
			CredentialID: "cred-tgt",
			Username:     "tgt_user",
			Password:     "tgt_pass",
			Database:     "opsi",
		},
	}

	// 1. Success case
	runner := &fakeRunner{}
	executor := Executor{KubectlPath: "kubectl", Runner: runner}
	res := executor.Review(context.Background(), lease)
	if res.Status != cutoverv1.ReviewSucceeded {
		t.Fatalf("expected success, got %s: %s (%s)", res.Status, res.FailureCode, res.FailureMessageRedacted)
	}
	if res.SourceSQLPreflight != "PASS" || res.TargetSQLPreflight != "PASS" {
		t.Fatalf("unexpected preflight results: source=%s target=%s", res.SourceSQLPreflight, res.TargetSQLPreflight)
	}
	if len(res.EvidenceHash) != 64 {
		t.Fatalf("expected 64 char hex evidence hash, got %q", res.EvidenceHash)
	}

	// 2. Source ping failure
	runnerFailSource := &fakeRunner{failPing: true}
	executorFailSource := Executor{Runner: runnerFailSource}
	resFailSource := executorFailSource.Review(context.Background(), lease)
	if resFailSource.Status != cutoverv1.ReviewFailed || resFailSource.FailureCode != cutoverv1.FailureDatabaseUnavailable {
		t.Fatalf("expected FailureDatabaseUnavailable, got status=%s code=%s", resFailSource.Status, resFailSource.FailureCode)
	}

	// 3. Target privilege check failure (superuser detected)
	runnerBadPrivs := &fakeRunner{badPrivs: true}
	executorBadPrivs := Executor{Runner: runnerBadPrivs}
	resBadPrivs := executorBadPrivs.Review(context.Background(), lease)
	if resBadPrivs.Status != cutoverv1.ReviewFailed || resBadPrivs.FailureCode != cutoverv1.FailurePrivilegeInvalid {
		t.Fatalf("expected FailurePrivilegeInvalid, got status=%s code=%s", resBadPrivs.Status, resBadPrivs.FailureCode)
	}
}
