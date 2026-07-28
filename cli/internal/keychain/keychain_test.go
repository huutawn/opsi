package keychain

import "testing"

func TestFakeStorePAT(t *testing.T) {
	store := NewFakeStore()
	if err := store.SetPAT("token-1"); err != nil {
		t.Fatal(err)
	}
	token, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-1" {
		t.Fatalf("unexpected token %q", token)
	}
}

func TestFakeStoreMissingPAT(t *testing.T) {
	_, err := NewFakeStore().GetPAT()
	if err == nil {
		t.Fatal("expected missing PAT error")
	}
}

func TestFakeStoreDeletePAT(t *testing.T) {
	store := NewFakeStore()
	if err := store.SetPAT("token-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePAT(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPAT(); err == nil {
		t.Fatal("expected PAT to be deleted")
	}
}

func TestFakeStoreActionSecretsAreOpaqueAndDeletable(t *testing.T) {
	store := NewFakeStore()
	privateKey := []byte{0, 1, 2, 3, 255}
	if err := store.SetActionPrivateKey("device-1", privateKey); err != nil {
		t.Fatal(err)
	}
	privateKey[0] = 9
	loaded, err := store.GetActionPrivateKey("device-1")
	if err != nil || loaded[0] != 0 {
		t.Fatalf("private key alias or error: %v %v", loaded, err)
	}
	if err := store.SetPendingApproval("challenge-1", []byte(`{"grant":"opaque"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePendingApproval("challenge-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPendingApproval("challenge-1"); err != ErrActionSecretNotFound {
		t.Fatalf("pending grant error=%v", err)
	}
}
