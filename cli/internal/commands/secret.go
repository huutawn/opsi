package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
)

type secretFlags struct {
	projectID    string
	serviceID    string
	name         string
	namespace    string
	patFile      string
	otpFile      string
	otpRequestID string
	totpFile     string
}

type secretWriteResult struct {
	Status    string `json:"status"`
	ProjectID string `json:"project_id"`
	ServiceID string `json:"service_id,omitempty"`
	Name      string `json:"name,omitempty"`
}

func newSecretCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	flags := &secretFlags{}
	cmd := &cobra.Command{Use: "secret", Short: "Manage Agent/K3s secrets"}
	cmd.AddCommand(newSecretSetupTOTPCommand(configPath, factory, flags))
	cmd.AddCommand(newSecretMutationCommand(configPath, factory, flags, "create", "Create generated service credentials", func(ctx context.Context, client *agentclient.Client, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
		return client.CreateSecret(ctx, req)
	}))
	cmd.AddCommand(newSecretMutationCommand(configPath, factory, flags, "reveal", "Reveal service credentials after OTP/TOTP", func(ctx context.Context, client *agentclient.Client, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
		return client.RevealSecret(ctx, req)
	}))
	cmd.AddCommand(newSecretMutationCommand(configPath, factory, flags, "rotate", "Rotate service credentials", func(ctx context.Context, client *agentclient.Client, req *agentv1.SecretRequest) (*agentv1.SecretResponse, error) {
		return client.RotateSecret(ctx, req)
	}))
	return cmd
}

func newSecretSetupTOTPCommand(configPath *string, factory func() (keychain.Store, error), flags *secretFlags) *cobra.Command {
	var outputFile string
	cmd := &cobra.Command{
		Use:   "setup-totp",
		Short: "Create local TOTP fallback setup URI",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || outputFile == "" {
				return errors.New("project-id and output-file are required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			pat, err := resolvePAT(flags.patFile, factory)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			ctx = agentclient.WithPAT(ctx, pat)
			resp, err := agentclient.New(cfg).SetupTOTP(ctx, &agentv1.SetupTOTPRequest{ProjectID: flags.projectID})
			if err != nil {
				return errors.New("Agent setup request failed")
			}
			if err := writeProtectedResponse(outputFile, resp); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(secretWriteResult{Status: "written", ProjectID: flags.projectID})
		},
	}
	addSecretAuthFlags(cmd, flags)
	cmd.Flags().StringVar(&outputFile, "output-file", "", "new protected file for the sensitive response")
	return cmd
}

func newSecretMutationCommand(configPath *string, factory func() (keychain.Store, error), flags *secretFlags, use, short string, call func(context.Context, *agentclient.Client, *agentv1.SecretRequest) (*agentv1.SecretResponse, error)) *cobra.Command {
	var outputFile string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.serviceID == "" || flags.name == "" || outputFile == "" {
				return errors.New("project-id, service-id, name and output-file are required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			pat, err := resolvePAT(flags.patFile, factory)
			if err != nil {
				return err
			}
			otp, err := resolveProtectedCode(flags.otpFile, "OTP")
			if err != nil {
				return err
			}
			totp, err := resolveProtectedCode(flags.totpFile, "TOTP")
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			ctx = agentclient.WithPAT(ctx, pat)
			req := &agentv1.SecretRequest{ProjectID: flags.projectID, ServiceID: flags.serviceID, Name: flags.name, Namespace: flags.namespace, OTPCode: otp, OTPRequestID: flags.otpRequestID, TOTPCode: totp}
			resp, err := call(ctx, agentclient.New(cfg), req)
			if err != nil {
				return errors.New("Agent secret request failed")
			}
			if err := writeProtectedResponse(outputFile, resp); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(secretWriteResult{Status: "written", ProjectID: flags.projectID, ServiceID: flags.serviceID, Name: flags.name})
		},
	}
	addSecretAuthFlags(cmd, flags)
	cmd.Flags().StringVar(&flags.serviceID, "service-id", "", "service id")
	cmd.Flags().StringVar(&flags.name, "name", "", "kubernetes secret name")
	cmd.Flags().StringVar(&flags.namespace, "namespace", "", "kubernetes namespace")
	cmd.Flags().StringVar(&flags.otpFile, "otp-file", "", "protected cloud OTP file; use /dev/stdin for piped input")
	cmd.Flags().StringVar(&flags.otpRequestID, "otp-request-id", "", "cloud OTP request id")
	cmd.Flags().StringVar(&flags.totpFile, "totp-file", "", "protected local TOTP file; use /dev/stdin for piped input")
	cmd.Flags().StringVar(&outputFile, "output-file", "", "new protected file for the sensitive response")
	return cmd
}

func writeProtectedResponse(path string, response any) (err error) {
	return writeProtectedResponseUsing(path, response, func(file *os.File, data []byte) error {
		_, err := file.Write(data)
		return err
	})
}

func writeProtectedResponseUsing(path string, response any, write func(*os.File, []byte) error) (err error) {
	data, err := json.Marshal(response)
	if err != nil {
		return errors.New("encode protected response")
	}
	data = append(data, '\n')
	if len(data) > maxProtectedSecretBytes {
		return errors.New("protected response exceeds 1 MiB")
	}

	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("create protected output file")
	}
	file := os.NewFile(uintptr(fd), "protected-output")
	keep := false
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := write(file, data); err != nil {
		return errors.New("write protected output file")
	}
	if err := file.Sync(); err != nil {
		return errors.New("sync protected output file")
	}
	if err := file.Close(); err != nil {
		file = nil
		return errors.New("close protected output file")
	}
	file = nil
	keep = true
	return nil
}

func addSecretAuthFlags(cmd *cobra.Command, flags *secretFlags) {
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.patFile, "pat-file", "", "protected PAT file; defaults to OS keychain")
}

func redactPATError(err error, pat string) error {
	if err == nil || pat == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), pat, "[REDACTED]"))
}

func resolvePAT(path string, factory func() (keychain.Store, error)) (string, error) {
	if path != "" {
		value, err := readProtectedSecret(path, "PAT")
		if err != nil {
			return "", err
		}
		defer clearBytes(value)
		pat := strings.TrimSpace(string(value))
		if pat == "" {
			return "", fmt.Errorf("PAT file is empty")
		}
		return pat, nil
	}
	store, err := factory()
	if err != nil {
		return "", err
	}
	pat, err := store.GetPAT()
	if err != nil {
		return "", err
	}
	if pat == "" {
		return "", fmt.Errorf("PAT is required; run opsi login --pat-file PATH or configure the OS keychain")
	}
	return pat, nil
}

func resolveProtectedCode(path, label string) (string, error) {
	if path == "" {
		return "", nil
	}
	value, err := readProtectedSecret(path, label)
	if err != nil {
		return "", err
	}
	defer clearBytes(value)
	code := strings.TrimSpace(string(value))
	if code == "" {
		return "", fmt.Errorf("%s file is empty", label)
	}
	return code, nil
}
