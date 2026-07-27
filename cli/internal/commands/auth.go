package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/spf13/cobra"
)

const maxProtectedSecretBytes = 1 << 20

func optionalPAT(factory func() (keychain.Store, error)) string {
	if factory == nil {
		return ""
	}
	store, err := factory()
	if err != nil {
		return ""
	}
	pat, err := store.GetPAT()
	if err != nil {
		return ""
	}
	return pat
}

func requiredPAT(factory func() (keychain.Store, error)) (keychain.Store, string, error) {
	if factory == nil {
		return nil, "", errors.New("OS keychain is unavailable")
	}
	store, err := factory()
	if err != nil {
		return nil, "", errors.New("open OS keychain")
	}
	pat, err := store.GetPAT()
	if err != nil || strings.TrimSpace(pat) == "" {
		return nil, "", errors.New("Cloud PAT not found in OS keychain; run opsi login --pat-file PATH")
	}
	return store, pat, nil
}

func newAuthCommand(configPath *string, options Options) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{Use: "auth", Short: "Verify, rotate, or revoke the keychain Cloud PAT"}
	run := func(command *cobra.Command, action string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		cfg, err := config.LoadSelected(*configPath)
		if err != nil {
			return err
		}
		store, pat, err := requiredPAT(options.KeychainFactory)
		if err != nil {
			return err
		}
		client, err := cloudclient.New(cfg.CloudURL, pat, options.Version, options.HTTPClient)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(command.Context(), 30*time.Second)
		defer cancel()
		var result map[string]any
		switch action {
		case "verify":
			result, err = client.VerifyPAT(ctx, projectID)
		case "rotate":
			result, err = client.RotatePAT(ctx, projectID)
			if err == nil {
				rotated, ok := result["token"].(string)
				if !ok || rotated == "" {
					return errors.New("Cloud PAT rotation returned no token")
				}
				if err := store.SetPAT(rotated); err != nil {
					return errors.New("store rotated Cloud PAT in OS keychain")
				}
				delete(result, "token")
			}
		case "revoke":
			result, err = client.RevokePAT(ctx, projectID)
			if err == nil {
				err = store.DeletePAT()
			}
		}
		if err != nil {
			return redactPATError(err, pat)
		}
		return json.NewEncoder(command.OutOrStdout()).Encode(result)
	}
	for _, action := range []string{"verify", "rotate", "revoke"} {
		action := action
		subcommand := &cobra.Command{Use: action, RunE: func(command *cobra.Command, _ []string) error { return run(command, action) }}
		subcommand.Flags().StringVar(&projectID, "project-id", "", "project id")
		cmd.AddCommand(subcommand)
	}
	return cmd
}

func readProtectedSecret(path, label string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s file is required; secret values are not accepted in argv", label)
	}
	file, err := openProtectedSecret(path)
	if err != nil {
		return nil, fmt.Errorf("open %s file: %w", label, err)
	}
	defer file.Close()
	if path != "/dev/stdin" {
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect %s file: %w", label, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s file must be a regular file", label)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s file must not be group or world accessible", label)
		}
	}
	value, err := io.ReadAll(io.LimitReader(file, maxProtectedSecretBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s file", label)
	}
	if len(value) > maxProtectedSecretBytes {
		clearBytes(value)
		return nil, fmt.Errorf("%s file exceeds 1 MiB", label)
	}
	if len(value) == 0 {
		return nil, errors.New("protected secret file is empty")
	}
	return value, nil
}

func openProtectedSecret(path string) (*os.File, error) {
	if path == "/dev/stdin" {
		return os.Open(path)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errors.New("protected secret file must not be a symlink")
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
