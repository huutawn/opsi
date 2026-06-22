package otp

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOTPVerifyOneTimeAndExpiry(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	svc := NewService()
	svc.DevEcho = true
	svc.now = func() time.Time { return now }
	resp, err := svc.RequestOTP(context.Background(), Request{ProjectID: "proj", UserID: "user", Purpose: "secret.reveal"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Code == "" {
		t.Fatal("expected dev echo code")
	}
	if err := svc.VerifyOTP(context.Background(), resp.RequestID, "proj", "user", "secret.reveal", resp.Code); err != nil {
		t.Fatal(err)
	}
	if err := svc.VerifyOTP(context.Background(), resp.RequestID, "proj", "user", "secret.reveal", resp.Code); !errors.Is(err, ErrUsed) {
		t.Fatalf("expected used error, got %v", err)
	}

	resp, err = svc.RequestOTP(context.Background(), Request{ProjectID: "proj", UserID: "user2", Purpose: "secret.reveal"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if err := svc.VerifyOTP(context.Background(), resp.RequestID, "proj", "user2", "secret.reveal", resp.Code); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestOTPRateLimit(t *testing.T) {
	svc := NewService()
	svc.DevEcho = true
	svc.Limit = 2
	svc.Window = 15 * time.Minute
	for i := 0; i < 2; i++ {
		if _, err := svc.RequestOTP(context.Background(), Request{ProjectID: "proj", UserID: "user", Purpose: "secret.reveal"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.RequestOTP(context.Background(), Request{ProjectID: "proj", UserID: "user", Purpose: "secret.reveal"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
}
