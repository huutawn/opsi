package actiondevice

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestServiceUsesTrustedPrincipalAndRejectsViewerOrEmptyActor(t *testing.T) {
	store := NewMemoryStore()
	service := Service{Store: store, Now: func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	request := RegisterRequest{DisplayName: "laptop", PublicKey: publicKey, IdempotencyKey: "register-1"}
	if _, _, err := service.Register(context.Background(), Principal{ProjectID: "p1", UserID: "", Role: "owner"}, request); !errors.Is(err, ErrPrincipalRequired) {
		t.Fatalf("empty actor error = %v", err)
	}
	if _, _, err := service.Register(context.Background(), Principal{ProjectID: "p1", UserID: "viewer", Role: "viewer"}, request); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("viewer error = %v", err)
	}
	device, replay, err := service.Register(context.Background(), Principal{ProjectID: "p1", UserID: "trusted-user", Role: "developer"}, request)
	if err != nil || replay {
		t.Fatalf("register = %#v %v %v", device, replay, err)
	}
	if device.OwnerPrincipal != "trusted-user" || device.TrustedActor != "trusted-user" {
		t.Fatalf("trusted actor not enforced: %#v", device)
	}
}

func TestRegisterReplayAndRevokeAreIdempotentAndAuditedOnce(t *testing.T) {
	store := NewMemoryStore()
	service := Service{Store: store}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	principal := Principal{ProjectID: "p1", UserID: "u1", Role: "owner"}
	request := RegisterRequest{DisplayName: "laptop", PublicKey: publicKey, IdempotencyKey: "same"}
	first, replay, err := service.Register(context.Background(), principal, request)
	if err != nil || replay {
		t.Fatal(err)
	}
	second, replay, err := service.Register(context.Background(), principal, request)
	if err != nil || !replay || second.ID != first.ID {
		t.Fatalf("replay = %#v %v %v", second, replay, err)
	}
	if got := store.AuditCount(); got != 1 {
		t.Fatalf("register audit count = %d", got)
	}
	if _, changed, err := service.Revoke(context.Background(), principal, first.ID); err != nil || !changed {
		t.Fatalf("first revoke changed=%v err=%v", changed, err)
	}
	if _, changed, err := service.Revoke(context.Background(), principal, first.ID); err != nil || changed {
		t.Fatalf("second revoke changed=%v err=%v", changed, err)
	}
	if got := store.AuditCount(); got != 2 {
		t.Fatalf("total audit count = %d", got)
	}
	if _, err := service.ResolveActive(context.Background(), "p1", first.ID, "u1"); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("revoked resolve error = %v", err)
	}
}

func TestRegisterRejectsInvalidKeyAndConflictingReplay(t *testing.T) {
	service := Service{Store: NewMemoryStore()}
	principal := Principal{ProjectID: "p1", UserID: "u1", Role: "owner"}
	if _, _, err := service.Register(context.Background(), principal, RegisterRequest{DisplayName: "x", PublicKey: []byte("not-ed25519"), IdempotencyKey: "k"}); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("invalid key error = %v", err)
	}
	key1, _, _ := ed25519.GenerateKey(rand.Reader)
	key2, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, err := service.Register(context.Background(), principal, RegisterRequest{DisplayName: "x", PublicKey: key1, IdempotencyKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Register(context.Background(), principal, RegisterRequest{DisplayName: "x", PublicKey: key2, IdempotencyKey: "k"}); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}
