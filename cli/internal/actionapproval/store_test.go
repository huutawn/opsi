package actionapproval

import (
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestStoreKeepsPrivateKeyAndPendingGrantInSecureBackend(t *testing.T) {
	backend := keychain.NewFakeStore()
	store := Store{Backend: backend}
	if err := store.SavePrivateKey("device-1", []byte("private")); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePendingGrant("challenge-1", []byte("grant")); err != nil {
		t.Fatal(err)
	}
	if value, err := store.PrivateKey("device-1"); err != nil || string(value) != "private" {
		t.Fatalf("private=%q err=%v", value, err)
	}
	if err := store.DeletePendingGrant("challenge-1"); err != nil {
		t.Fatal(err)
	}
}
