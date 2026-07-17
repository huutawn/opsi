package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func TestLoginStoresPATInKeychain(t *testing.T) {
	dir := t.TempDir()
	patPath := filepath.Join(dir, "initial-owner.pat")
	if err := os.WriteFile(patPath, []byte("token-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	cmd := NewRootCommand(Options{
		Version: "test",
		KeychainFactory: func() (keychain.Store, error) {
			return store, nil
		},
	})
	buf := bytes.NewBuffer(nil)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"login", "--pat-file", patPath})

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

	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "secret values are not accepted in argv") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretValuedArgvFlagsAreRemoved(t *testing.T) {
	root := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return keychain.NewFakeStore(), nil
	}})
	for _, testCase := range []struct {
		path    []string
		removed []string
		file    []string
	}{
		{path: []string{"login"}, removed: []string{"pat"}, file: []string{"pat-file"}},
		{path: []string{"secret", "reveal"}, removed: []string{"pat", "otp", "totp"}, file: []string{"pat-file", "otp-file", "totp-file"}},
	} {
		command, _, err := root.Find(testCase.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range testCase.removed {
			if command.Flags().Lookup(name) != nil {
				t.Fatalf("secret-valued --%s remains on %v", name, testCase.path)
			}
		}
		for _, name := range testCase.file {
			if command.Flags().Lookup(name) == nil {
				t.Fatalf("protected --%s is missing on %v", name, testCase.path)
			}
		}
	}
}
