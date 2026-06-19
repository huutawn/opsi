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
