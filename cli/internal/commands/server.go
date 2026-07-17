package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/spf13/cobra"
)

type serverFlags struct {
	projectID      string
	sessionID      string
	role           string
	publicHost     string
	sshPort        int
	sshUsername    string
	authMethod     string
	credentialFile string
	idempotencyKey string
}

func newServerCommand(configPath *string, options Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "server",
		Aliases: []string{"node"},
		Short:   "Bootstrap and inspect Opsi servers",
	}
	cmd.AddCommand(newServerBootstrapCommand(configPath, options))
	cmd.AddCommand(newServerStatusCommand(configPath, options))
	cmd.AddCommand(newServerEventsCommand(configPath, options))
	return cmd
}

func newServerBootstrapCommand(configPath *string, options Options) *cobra.Command {
	flags := &serverFlags{role: "first_server", sshPort: 22, sshUsername: "root", authMethod: "private_key"}
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Create a K3s and Agent bootstrap session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.publicHost == "" || flags.sshUsername == "" {
				return errors.New("project-id, public-host and ssh-username are required")
			}
			if flags.sshPort < 1 || flags.sshPort > 65535 {
				return errors.New("ssh-port must be between 1 and 65535")
			}
			if flags.role != "first_server" && flags.role != "worker" {
				return errors.New("role must be first_server or worker")
			}
			if flags.authMethod != "private_key" && flags.authMethod != "password" {
				return errors.New("auth-method must be private_key or password")
			}
			credential, err := readBootstrapCredential(flags.credentialFile, flags.authMethod)
			if err != nil {
				return err
			}
			defer clearBytes(credential)

			request := cloudclient.BootstrapRequest{
				Role:        flags.role,
				PublicHost:  flags.publicHost,
				SSHPort:     flags.sshPort,
				SSHUsername: flags.sshUsername,
				AuthMethod:  flags.authMethod,
			}
			if flags.authMethod == "private_key" {
				request.SSHPrivateKey = string(credential)
			} else {
				request.SSHPassword = strings.TrimSuffix(strings.TrimSuffix(string(credential), "\n"), "\r")
			}
			if request.SSHPrivateKey == "" && request.SSHPassword == "" {
				return errors.New("bootstrap credential is empty")
			}

			client, err := newCommandCloudClient(*configPath, options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			session, err := client.CreateBootstrapSession(ctx, flags.projectID, request, flags.idempotencyKey)
			if err != nil {
				return fmt.Errorf("create bootstrap session: %w", err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(session)
		},
	}
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.role, "role", flags.role, "server role: first_server or worker")
	cmd.Flags().StringVar(&flags.publicHost, "public-host", "", "target VPS hostname or IP")
	cmd.Flags().IntVar(&flags.sshPort, "ssh-port", flags.sshPort, "target SSH port")
	cmd.Flags().StringVar(&flags.sshUsername, "ssh-username", flags.sshUsername, "target SSH username")
	cmd.Flags().StringVar(&flags.authMethod, "auth-method", flags.authMethod, "SSH authentication: private_key or password")
	cmd.Flags().StringVar(&flags.credentialFile, "credential-file", "", "protected SSH credential file; use /dev/stdin for piped input")
	cmd.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "stable retry key; generated when omitted")
	return cmd
}

func newServerStatusCommand(configPath *string, options Options) *cobra.Command {
	flags := &serverFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show bootstrap session status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" {
				return errors.New("project-id is required")
			}
			client, err := newCommandCloudClient(*configPath, options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if flags.sessionID != "" {
				session, err := client.GetBootstrapSession(ctx, flags.projectID, flags.sessionID)
				if err != nil {
					return fmt.Errorf("get bootstrap session: %w", err)
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(session)
			}
			sessions, err := client.ListBootstrapSessions(ctx, flags.projectID)
			if err != nil {
				return fmt.Errorf("list bootstrap sessions: %w", err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Sessions []cloudclient.BootstrapSession `json:"sessions"`
			}{Sessions: sessions})
		},
	}
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.sessionID, "session-id", "", "bootstrap session id; omit to list all sessions")
	return cmd
}

func newServerEventsCommand(configPath *string, options Options) *cobra.Command {
	flags := &serverFlags{}
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show redacted bootstrap progress events",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.sessionID == "" {
				return errors.New("project-id and session-id are required")
			}
			client, err := newCommandCloudClient(*configPath, options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			events, err := client.BootstrapEvents(ctx, flags.projectID, flags.sessionID)
			if err != nil {
				return fmt.Errorf("get bootstrap events: %w", err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(events)
		},
	}
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.sessionID, "session-id", "", "bootstrap session id")
	return cmd
}

func newCommandCloudClient(configPath string, options Options) (*cloudclient.Client, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return cloudclient.New(cfg.CloudURL, optionalPAT(options.KeychainFactory), options.Version, options.HTTPClient)
}

func readBootstrapCredential(path, authMethod string) ([]byte, error) {
	credential, err := readProtectedSecret(path, "bootstrap credential")
	if err != nil {
		return nil, err
	}
	if authMethod == "private_key" && !strings.Contains(string(credential), "PRIVATE KEY") {
		clearBytes(credential)
		return nil, errors.New("private_key credential does not contain a private key marker")
	}
	return credential, nil
}
