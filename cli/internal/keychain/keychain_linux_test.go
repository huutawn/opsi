//go:build linux

package keychain

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSecretToolStoreSetGetDelete(t *testing.T) {
	const token = "test-pat-must-not-appear-in-errors"
	var operations []string
	store := &OSStore{
		timeout: time.Second,
		run: func(_ context.Context, _ string, args []string, input []byte) ([]byte, []byte, error) {
			operations = append(operations, args[0])
			switch args[0] {
			case "store":
				if string(input) != token {
					t.Fatal("store did not receive the supplied PAT on stdin")
				}
				if strings.Contains(strings.Join(args, " "), token) {
					t.Fatal("PAT was passed in the secret-tool argv")
				}
				return nil, nil, nil
			case "lookup":
				return []byte(token + "\n"), nil, nil
			case "clear":
				return nil, nil, nil
			default:
				t.Fatalf("unexpected operation %q", args[0])
				return nil, nil, nil
			}
		},
	}
	if err := store.SetPAT(token); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	if got != token {
		t.Fatalf("unexpected PAT length %d", len(got))
	}
	if err := store.DeletePAT(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(operations, ",") != "store,lookup,clear" {
		t.Fatalf("unexpected operations %q", operations)
	}
}

func TestSecretToolStoreErrorsAreSanitized(t *testing.T) {
	const token = "test-pat-must-not-leak"
	tests := []struct {
		name   string
		stderr string
		want   error
	}{
		{name: "unavailable", stderr: "cannot connect to Secret Service", want: ErrKeychainUnavailable},
		{name: "locked", stderr: "collection is locked", want: ErrKeychainUnavailable},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store := &OSStore{
				timeout: time.Second,
				run: func(context.Context, string, []string, []byte) ([]byte, []byte, error) {
					return nil, []byte(testCase.stderr), errors.New("secret-tool failed")
				},
			}
			err := store.SetPAT(token)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), testCase.stderr) {
				t.Fatalf("unsanitized keychain error: %q", err)
			}
		})
	}
}

func TestSecretToolStoreTimeoutsAreBounded(t *testing.T) {
	tests := []struct {
		name string
		call func(*OSStore) error
	}{
		{name: "store", call: func(store *OSStore) error { return store.SetPAT("test-pat") }},
		{name: "get", call: func(store *OSStore) error { _, err := store.GetPAT(); return err }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store := &OSStore{
				timeout: 20 * time.Millisecond,
				run: func(ctx context.Context, _ string, _ []string, _ []byte) ([]byte, []byte, error) {
					<-ctx.Done()
					return nil, nil, ctx.Err()
				},
			}
			started := time.Now()
			err := testCase.call(store)
			if !errors.Is(err, ErrKeychainTimeout) {
				t.Fatalf("unexpected error: %v", err)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("keychain call was not bounded: %s", elapsed)
			}
		})
	}
}

func TestSecretToolGetMissingPAT(t *testing.T) {
	store := &OSStore{
		timeout: time.Second,
		run: func(context.Context, string, []string, []byte) ([]byte, []byte, error) {
			return nil, []byte("No matching secret found"), errors.New("secret-tool failed")
		},
	}
	_, err := store.GetPAT()
	if !errors.Is(err, ErrPATNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExternalCommandTimeoutKillsAndReaps(t *testing.T) {
	t.Setenv("OPSI_KEYCHAIN_TIMEOUT_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := runExternalCommand(ctx, os.Args[0], []string{"-test.run=TestSecretToolTimeoutHelper", "--"}, nil)
	if err == nil {
		t.Fatal("expected timed-out child process to fail")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed-out child was not killed and reaped promptly: %s", elapsed)
	}
}

func TestSecretToolTimeoutHelper(t *testing.T) {
	if os.Getenv("OPSI_KEYCHAIN_TIMEOUT_HELPER") != "1" {
		return
	}
	select {}
}
