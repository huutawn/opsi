package commands

import (
	"bytes"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestLoginStoresPATInKeychain(t *testing.T) {
	store := keychain.NewFakeStore()
	cmd := NewRootCommand(Options{
		Version: "test",
		KeychainFactory: func() (keychain.Store, error) {
			return store, nil
		},
	})
	buf := bytes.NewBuffer(nil)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"login", "--pat", "token-1"})

	if err := cmd.Execute(); err != nil {
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

func TestLoginRequiresPAT(t *testing.T) {
	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	cmd.SetArgs([]string{"login"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error")
	}
}
