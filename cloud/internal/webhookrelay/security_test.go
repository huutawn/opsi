package webhookrelay

import (
	"bytes"
	"testing"
	"time"
)

func TestCredentialExpiresAtBoundaryAndCannotBeLeasedAgain(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	store := NewCredentialStore()
	store.now = func() time.Time { return now }
	privateKey := []byte("private-key-secret")
	store.Put("session-1", BootstrapCredential{AuthMethod: "private_key", Username: "ubuntu", PrivateKey: privateKey}, time.Minute)

	leased, ok := store.GetForBootstrapLease("session-1")
	if !ok || !bytes.Equal(leased.PrivateKey, privateKey) {
		t.Fatalf("credential was not available before expiry: ok=%v", ok)
	}
	zeroBootstrapCredential(&leased)
	now = now.Add(time.Minute)
	if _, ok := store.GetForBootstrapLease("session-1"); ok {
		t.Fatal("credential remained leaseable at its expiry boundary")
	}
	if store.Len() != 0 {
		t.Fatal("expired credential remained in the store")
	}
	if _, ok := store.GetForBootstrapLease("session-1"); ok {
		t.Fatal("expired credential was replayed")
	}
}

func TestRegistrationTokenExpiresAtBoundaryAndCannotReplay(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	store := NewRegistrationTokenStore()
	store.now = func() time.Time { return now }
	store.Put("session-1", "org-1", "project-1", "node-1", "registration-secret", time.Minute)

	now = now.Add(time.Minute)
	if _, ok := store.GetForBootstrapLease("session-1"); ok {
		t.Fatal("registration token remained available at its expiry boundary")
	}
	if _, ok := store.Exchange("registration-secret"); ok {
		t.Fatal("expired registration token was exchanged")
	}

	store.Put("session-2", "org-1", "project-1", "node-2", "registration-secret-2", time.Minute)
	now = now.Add(30 * time.Second)
	registration, ok := store.Exchange("registration-secret-2")
	if !ok || registration.SessionID != "session-2" || registration.Token != "" {
		t.Fatalf("valid one-time exchange=%+v ok=%v", registration, ok)
	}
	if _, ok := store.Exchange("registration-secret-2"); ok {
		t.Fatal("registration token replay succeeded")
	}
}
