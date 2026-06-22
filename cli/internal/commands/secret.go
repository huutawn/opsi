package commands

import (
	"context"
	"encoding/json"
	"fmt"
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
	userID       string
	role         string
	pat          string
	otp          string
	otpRequestID string
	totp         string
}

func newSecretCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	flags := &secretFlags{role: "Owner"}
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
	cmd := &cobra.Command{
		Use:   "setup-totp",
		Short: "Create local TOTP fallback setup URI",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.userID == "" {
				return fmt.Errorf("project-id and user-id are required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			pat, err := resolvePAT(flags.pat, factory)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := agentclient.New(cfg).SetupTOTP(ctx, &agentv1.SetupTOTPRequest{ProjectID: flags.projectID, UserID: flags.userID, Role: flags.role, PAT: pat})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
		},
	}
	addSecretAuthFlags(cmd, flags)
	return cmd
}

func newSecretMutationCommand(configPath *string, factory func() (keychain.Store, error), flags *secretFlags, use, short string, call func(context.Context, *agentclient.Client, *agentv1.SecretRequest) (*agentv1.SecretResponse, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.serviceID == "" || flags.name == "" || flags.userID == "" {
				return fmt.Errorf("project-id, service-id, name and user-id are required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			pat, err := resolvePAT(flags.pat, factory)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			req := &agentv1.SecretRequest{ProjectID: flags.projectID, ServiceID: flags.serviceID, Name: flags.name, Namespace: flags.namespace, UserID: flags.userID, Role: flags.role, PAT: pat, OTPCode: flags.otp, OTPRequestID: flags.otpRequestID, TOTPCode: flags.totp}
			resp, err := call(ctx, agentclient.New(cfg), req)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
		},
	}
	addSecretAuthFlags(cmd, flags)
	cmd.Flags().StringVar(&flags.serviceID, "service-id", "", "service id")
	cmd.Flags().StringVar(&flags.name, "name", "", "kubernetes secret name")
	cmd.Flags().StringVar(&flags.namespace, "namespace", "", "kubernetes namespace")
	cmd.Flags().StringVar(&flags.otp, "otp", "", "cloud OTP code")
	cmd.Flags().StringVar(&flags.otpRequestID, "otp-request-id", "", "cloud OTP request id")
	cmd.Flags().StringVar(&flags.totp, "totp", "", "local TOTP code")
	return cmd
}

func addSecretAuthFlags(cmd *cobra.Command, flags *secretFlags) {
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.userID, "user-id", "", "user id")
	cmd.Flags().StringVar(&flags.role, "role", flags.role, "project role: Owner, Developer, Viewer")
	cmd.Flags().StringVar(&flags.pat, "pat", "", "personal access token; defaults to OS keychain")
}

func resolvePAT(value string, factory func() (keychain.Store, error)) (string, error) {
	if value != "" {
		return value, nil
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
		return "", fmt.Errorf("PAT is required; run opsi login or pass --pat")
	}
	return pat, nil
}
