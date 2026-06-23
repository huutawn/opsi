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
	if result.UserID != "owner@example.com" || result.Role != "Owner" || result.ProjectID != "proj" {
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
