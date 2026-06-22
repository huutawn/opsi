package secret

import (
	"context"
	"io"
	"testing"
	"time"
)

type auditSink struct {
	records []AuditRecord
}

func (s *auditSink) InsertAudit(_ context.Context, record AuditRecord) error {
	s.records = append(s.records, record)
	return nil
}

func TestGenerateCredentialsUsesRequestedShapeAndEntropyErrors(t *testing.T) {
	value, err := GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Username) != 12 || len(value.Password) != 32 {
		t.Fatalf("unexpected credential shape: %+v", value)
	}
	if _, err := GenerateCredentialsFrom(errReader{}); err == nil {
		t.Fatal("expected entropy error")
	}
}

func TestRBACMatrix(t *testing.T) {
	if !CanReveal(RoleOwner) || CanReveal(RoleDeveloper) || CanReveal(RoleViewer) {
		t.Fatal("unexpected reveal RBAC")
	}
	if !CanCreate(RoleOwner) || !CanCreate(RoleDeveloper) || CanCreate(RoleViewer) {
		t.Fatal("unexpected create RBAC")
	}
}

func TestTOTPVerifyRFC6238Vector(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := GenerateTOTPCode(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" || !VerifyTOTP(secret, code, time.Unix(59, 0), 0) {
		t.Fatalf("unexpected totp code %q", code)
	}
}

func TestServiceRevealFallsBackToTOTPAndAudits(t *testing.T) {
	store := NewMemoryStore()
	audit := &auditSink{}
	now := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	svc := &Service{Store: store, Audit: audit, Encryption: StaticEncryptionVerifier(true), TOTPSecretByUser: map[string]string{}, Now: func() time.Time { return now }}
	auth := AuthContext{ProjectID: "proj", UserID: "owner", Role: RoleOwner}
	ref := SecretRef{ProjectID: "proj", ServiceID: "svc", Namespace: "default", Name: "db"}
	secretValue, _, err := svc.SetupTOTP(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), auth, ref); err != nil {
		t.Fatal(err)
	}
	code, err := GenerateTOTPCode(secretValue, now)
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := svc.Reveal(context.Background(), auth, ref, "", "", code)
	if err != nil {
		t.Fatal(err)
	}
	if revealed.Password == "" || revealed.Username == "" {
		t.Fatal("expected revealed credentials")
	}
	if len(audit.records) < 3 || audit.records[len(audit.records)-1].Action != ActionReveal || audit.records[len(audit.records)-1].Result != "success" {
		t.Fatalf("unexpected audit records: %+v", audit.records)
	}
}

func TestServiceRejectsNonOwnerReveal(t *testing.T) {
	svc := &Service{Store: NewMemoryStore(), Audit: &auditSink{}, Encryption: StaticEncryptionVerifier(true), TOTPSecretByUser: map[string]string{}}
	_, err := svc.Reveal(context.Background(), AuthContext{ProjectID: "proj", UserID: "dev", Role: RoleDeveloper}, SecretRef{ProjectID: "proj", ServiceID: "svc", Namespace: "default", Name: "db"}, "", "", "123456")
	if err == nil {
		t.Fatal("expected permission denial")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
