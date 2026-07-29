package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

type failingPATStore struct{}

func (failingPATStore) SetPAT(string) error     { return errors.New("keychain unavailable") }
func (failingPATStore) GetPAT() (string, error) { return "", keychain.ErrPATNotFound }
func (failingPATStore) DeletePAT() error        { return nil }

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

func TestLoginDoesNotClaimPATStoredOnKeychainFailure(t *testing.T) {
	dir := t.TempDir()
	patPath := filepath.Join(dir, "initial-owner.pat")
	if err := os.WriteFile(patPath, []byte("token-that-must-not-appear\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) {
		return failingPATStore{}, nil
	}})
	output := bytes.NewBuffer(nil)
	cmd.SetOut(output)
	cmd.SetErr(output)
	cmd.SetArgs([]string{"login", "--pat-file", patPath})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected keychain store failure")
	}
	if strings.Contains(output.String(), "PAT stored") || strings.Contains(output.String(), "token-that-must-not-appear") {
		t.Fatalf("unexpected login output: %q", output.String())
	}
}

func TestLoginReplacesExistingPAT(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.pat")
	secondPath := filepath.Join(dir, "second.pat")
	if err := os.WriteFile(firstPath, []byte("token-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("token-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	for _, path := range []string{firstPath, secondPath} {
		cmd := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }})
		cmd.SetArgs([]string{"login", "--pat-file", path})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	if got != "token-2" {
		t.Fatalf("unexpected PAT after replacement: %q", got)
	}
}

func TestAuthLifecycleKeepsPATInKeychainAndOutOfOutput(t *testing.T) {
	const oldPAT = "auth-old-pat-canary"
	const newPAT = "auth-new-pat-canary"
	var authorization []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = append(authorization, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/pat/verify":
			_, _ = w.Write([]byte(`{"project_id":"proj-1","role":"Owner"}`))
		case "/v1/auth/pat/rotate":
			_, _ = w.Write([]byte(`{"token":"` + newPAT + `","session":{"project_id":"proj-1","role":"Owner"}}`))
		case "/v1/auth/pat/revoke":
			_, _ = w.Write([]byte(`{"revoked":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(configPath, []byte("cloud_url: "+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := keychain.NewFakeStore()
	if err := store.SetPAT(oldPAT); err != nil {
		t.Fatal(err)
	}
	run := func(action string) string {
		t.Helper()
		command := NewRootCommand(Options{KeychainFactory: func() (keychain.Store, error) { return store, nil }, HTTPClient: server.Client()})
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs([]string{"--config", configPath, "auth", action, "--project-id", "proj-1"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), oldPAT) || strings.Contains(output.String(), newPAT) {
			t.Fatalf("auth %s leaked PAT: %s", action, output.String())
		}
		var result map[string]any
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("auth %s output: %v", action, err)
		}
		return output.String()
	}

	run("verify")
	run("rotate")
	if got, err := store.GetPAT(); err != nil || got != newPAT {
		t.Fatalf("rotated PAT=%q err=%v", got, err)
	}
	run("revoke")
	if _, err := store.GetPAT(); !errors.Is(err, keychain.ErrPATNotFound) {
		t.Fatalf("revoked PAT remained in keychain: %v", err)
	}
	if len(authorization) != 3 || authorization[0] != "Bearer "+oldPAT || authorization[1] != "Bearer "+oldPAT || authorization[2] != "Bearer "+newPAT {
		t.Fatalf("authorization sequence=%v", authorization)
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
