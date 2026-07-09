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
	store := &MemoryStore{Candidates: []Candidate{{ID: "membership", UserID: "u", Email: "u@example.test", OrgID: "org", ProjectID: "proj", Role: "Owner"}}}
	svc := Service{Store: store, Now: func() time.Time { return now }}

	issued, err := svc.IssuePATForEmail(context.Background(), "u@example.test", "proj", time.Hour)
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
