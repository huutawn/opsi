package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestVerifyPATReturnsMembershipRole(t *testing.T) {
	hash, err := HashPAT("pat_live")
	if err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: MemoryStore{Candidates: []Candidate{{UserID: "owner@example.com", ProjectID: "proj", Role: "Owner", Hash: hash, ExpiresAt: time.Now().Add(time.Hour)}}}}
	result, err := svc.VerifyPAT(context.Background(), VerifyRequest{Token: "pat_live", ProjectID: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != "owner@example.com" || result.Role != "owner" || result.ProjectID != "proj" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestVerifyPATResolvesSingleProjectAndRejectsAmbiguousContext(t *testing.T) {
	hash, err := HashPAT("pat_live")
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{ID: "pat", UserID: "u", OrgID: "org", ProjectID: "proj-1", Role: "Owner", Hash: hash, ExpiresAt: time.Now().Add(time.Hour)}
	store := MemoryStore{Candidates: []Candidate{candidate}}
	result, err := (Service{Store: store}).VerifyPAT(context.Background(), VerifyRequest{Token: "pat_live"})
	if err != nil || result.ProjectID != "proj-1" || result.OrgID != "org" {
		t.Fatalf("single project result=%+v err=%v", result, err)
	}
	candidate.ProjectID = "proj-2"
	store.Candidates = append(store.Candidates, candidate)
	if _, err := (Service{Store: store}).VerifyPAT(context.Background(), VerifyRequest{Token: "pat_live"}); !errors.Is(err, ErrProjectChoice) {
		t.Fatalf("ambiguous projectless verification err=%v", err)
	}
}

func TestVerifyOrgPATReturnsOrgMembershipRole(t *testing.T) {
	hash, err := HashPAT("pat_live")
	if err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: MemoryStore{Candidates: []Candidate{{UserID: "u", OrgID: "org", Role: "Admin", Hash: hash, ExpiresAt: time.Now().Add(time.Hour)}}}}
	result, err := svc.VerifyOrgPAT(context.Background(), VerifyRequest{Token: "pat_live", OrgID: "org"})
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != "u" || result.OrgID != "org" || result.Role != "admin" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestVerifyPATRejectsExpiredRevokedAndWrongToken(t *testing.T) {
	hash, err := HashPAT("pat_live")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		candidate Candidate
		token     string
		want      error
	}{
		{name: "wrong", candidate: Candidate{UserID: "u", ProjectID: "proj", Role: "Owner", Hash: hash, ExpiresAt: now.Add(time.Hour)}, token: "bad", want: ErrInvalidToken},
		{name: "expired", candidate: Candidate{UserID: "u", ProjectID: "proj", Role: "Owner", Hash: hash, ExpiresAt: now.Add(-time.Second)}, token: "pat_live", want: ErrExpired},
		{name: "revoked", candidate: Candidate{UserID: "u", ProjectID: "proj", Role: "Owner", Hash: hash, ExpiresAt: now.Add(time.Hour), Revoked: true}, token: "pat_live", want: ErrRevoked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := Service{Store: MemoryStore{Candidates: []Candidate{tt.candidate}}, Now: func() time.Time { return now }}
			_, err := svc.VerifyPAT(context.Background(), VerifyRequest{Token: tt.token, ProjectID: "proj"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestPATIssueRotateAndRevoke(t *testing.T) {
	now := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	store := &MemoryStore{
		Candidates:      []Candidate{{ID: "membership", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: "proj", Role: "Owner"}},
		OAuthIdentities: map[string]string{"github\x001234": "u"},
	}
	svc := Service{Store: store, Now: func() time.Time { return now }}

	issued, err := svc.IssuePATForOAuth(context.Background(), "github", "1234", "proj", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || issued.Session.Role != "owner" || issued.Session.ProjectID != "proj" {
		t.Fatalf("bad issue result: %+v", issued.Session)
	}
	if _, err := svc.VerifyPAT(context.Background(), VerifyRequest{Token: issued.Token, ProjectID: "proj"}); err != nil {
		t.Fatal(err)
	}

	rotated, old, err := svc.RotatePAT(context.Background(), issued.Token, "proj", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Token == "" || rotated.Token == issued.Token || old.TokenID == "" {
		t.Fatalf("bad rotation: new=%q old=%+v", rotated.Token, old)
	}
	if _, err := svc.VerifyPAT(context.Background(), VerifyRequest{Token: issued.Token, ProjectID: "proj"}); err != nil {
		t.Fatalf("old token should remain valid until local commit/revoke: %v", err)
	}

	if _, err := svc.RevokePAT(context.Background(), issued.Token, "proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyPAT(context.Background(), VerifyRequest{Token: issued.Token, ProjectID: "proj"}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("expected revoked old token, got %v", err)
	}
	if _, err := svc.VerifyPAT(context.Background(), VerifyRequest{Token: rotated.Token, ProjectID: "proj"}); err != nil {
		t.Fatalf("new token should remain valid: %v", err)
	}
}

func TestIssuePATForOAuthRequiresPrelinkedProviderSubject(t *testing.T) {
	store := &MemoryStore{
		Candidates:      []Candidate{{ID: "membership", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: "proj", Role: "Owner"}},
		OAuthIdentities: map[string]string{"github\x001234": "u"},
	}
	svc := Service{Store: store}
	issued, err := svc.IssuePATForOAuth(context.Background(), "github", "1234", "proj", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Session.UserID != "u" || issued.Token == "" {
		t.Fatalf("unexpected OAuth issue result: %+v", issued.Session)
	}
	if _, err := svc.IssuePATForOAuth(context.Background(), "github", "different", "proj", time.Hour); !errors.Is(err, ErrOAuthIdentity) {
		t.Fatalf("expected unlinked subject rejection, got %v", err)
	}
}

func TestIssuePATForOAuthResolvesSingleProjectAndRejectsAmbiguousMembership(t *testing.T) {
	store := &MemoryStore{
		Candidates:      []Candidate{{ID: "membership", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: "proj-1", Role: "Owner"}},
		OAuthIdentities: map[string]string{"github\x001234": "u"},
	}
	service := Service{Store: store}
	issued, err := service.IssuePATForOAuth(context.Background(), "github", "1234", "", time.Hour)
	if err != nil || issued.Session.ProjectID != "proj-1" {
		t.Fatalf("single-project issue=%+v err=%v", issued.Session, err)
	}
	store.Candidates = append(store.Candidates, Candidate{ID: "membership-2", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: "proj-2", Role: "Developer"})
	if _, err := service.IssuePATForOAuth(context.Background(), "github", "1234", "", time.Hour); !errors.Is(err, ErrProjectChoice) {
		t.Fatalf("ambiguous membership err=%v", err)
	}
}

func TestResolveOAuthUserIsReadOnlyAndRequiresPrelinkedIdentity(t *testing.T) {
	store := &MemoryStore{OAuthIdentities: map[string]string{"github\x0012345": "user-1"}}
	service := Service{Store: store}
	userID, err := service.ResolveOAuthUser(context.Background(), "github", "12345")
	if err != nil || userID != "user-1" {
		t.Fatalf("userID=%q err=%v", userID, err)
	}
	if len(store.Candidates) != 0 {
		t.Fatalf("read-only resolution issued PAT candidates: %+v", store.Candidates)
	}
	if _, err := service.ResolveOAuthUser(context.Background(), "github", "missing"); !errors.Is(err, ErrOAuthIdentity) {
		t.Fatalf("missing identity err=%v", err)
	}
}
