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

func TestServiceUsesVerifiedRoleInsteadOfRequestRole(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	svc := &Service{Store: store, Auth: fakeAuth{role: RoleViewer, userID: "viewer"}, Audit: &auditSink{}, Encryption: StaticEncryptionVerifier(true), TOTPSecretByUser: map[string]string{"proj:viewer": "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}, Now: func() time.Time { return now }}
	ref := SecretRef{ProjectID: "proj", ServiceID: "svc", Namespace: "default", Name: "db"}
	if err := store.Put(context.Background(), ref, SecretValue{Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	code, err := GenerateTOTPCode("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Reveal(context.Background(), AuthContext{ProjectID: "proj", UserID: "owner", Role: RoleOwner, PAT: "pat"}, ref, "", "", code)
	if err == nil {
		t.Fatal("expected verified Viewer role to be denied")
	}
}

func TestServiceLoadsTOTPFromDurableStoreOnCacheMiss(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	totpSecret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	store := NewMemoryStore()
	svc := &Service{Store: store, TOTPStore: fakeTOTPStore{secret: totpSecret}, Audit: &auditSink{}, Encryption: StaticEncryptionVerifier(true), TOTPSecretByUser: map[string]string{}, Now: func() time.Time { return now }}
	auth := AuthContext{ProjectID: "proj", UserID: "owner", Role: RoleOwner}
	ref := SecretRef{ProjectID: "proj", ServiceID: "svc", Namespace: "default", Name: "db"}
	if err := store.Put(context.Background(), ref, SecretValue{Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	code, err := GenerateTOTPCode(totpSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reveal(context.Background(), auth, ref, "", "", code); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRotateTriggersRolloutRestart(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	totpSecret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	restarter := &fakeRestarter{}
	svc := &Service{Store: NewMemoryStore(), Audit: &auditSink{}, Encryption: StaticEncryptionVerifier(true), Restarter: restarter, TOTPSecretByUser: map[string]string{"proj:owner": totpSecret}, Now: func() time.Time { return now }}
	auth := AuthContext{ProjectID: "proj", UserID: "owner", Role: RoleOwner}
	ref := SecretRef{ProjectID: "proj", ServiceID: "svc", Namespace: "default", Name: "db"}
	code, err := GenerateTOTPCode(totpSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rotate(context.Background(), auth, ref, "", "", code); err != nil {
		t.Fatal(err)
	}
	if restarter.ref.ServiceID != "svc" || restarter.calls != 1 {
		t.Fatalf("restart not called: %+v calls=%d", restarter.ref, restarter.calls)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type fakeAuth struct {
	role   Role
	userID string
}

func (f fakeAuth) VerifyAuth(_ context.Context, auth AuthContext) (AuthContext, error) {
	auth.Role = f.role
	auth.UserID = f.userID
	return auth, nil
}

type fakeTOTPStore struct{ secret string }

func (s fakeTOTPStore) PutTOTP(context.Context, AuthContext, string) error { return nil }

func (s fakeTOTPStore) GetTOTP(context.Context, AuthContext) (string, error) { return s.secret, nil }

type fakeRestarter struct {
	calls int
	ref   SecretRef
}

func (r *fakeRestarter) Restart(_ context.Context, ref SecretRef) error {
	r.calls++
	r.ref = ref
	return nil
}
