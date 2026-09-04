package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/opsi-dev/opsi/cli/internal/mcp"
	"github.com/opsi-dev/opsi/cli/internal/repository"
	"github.com/spf13/cobra"
)

type mcpFlags struct {
	projectID string
	version   bool
}

func newMCPCommand(configPath *string, options Options) *cobra.Command {
	flags := &mcpFlags{}
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start the read-only Model Context Protocol (MCP) server",
		Long:  "Starts a Model Context Protocol (MCP) server over stdio. Provides read-only access to project topology, source snapshots, deployment preflight, and dependency verification facts.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.version {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "opsi-mcp %s (%s)\n", options.Version, mcp.SurfaceVersion)
				return err
			}
			return runMCP(cmd.Context(), *configPath, options, flags, cmd)
		},
	}

	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "default project ID for MCP queries")
	cmd.Flags().BoolVar(&flags.version, "version", false, "print MCP server surface version")

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.version {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "opsi-mcp %s (%s)\n", options.Version, mcp.SurfaceVersion)
				return err
			}
			return runMCP(cmd.Context(), *configPath, options, flags, cmd)
		},
	}
	serveCmd.Flags().StringVar(&flags.projectID, "project-id", "", "default project ID for MCP queries")
	serveCmd.Flags().BoolVar(&flags.version, "version", false, "print MCP server surface version")

	cmd.AddCommand(serveCmd)
	return cmd
}

func runMCP(parent context.Context, configPath string, options Options, flags *mcpFlags, cmd *cobra.Command) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	repoRoot, _ := repository.Root(ctx, options.GitRunner, ".")

	server := mcp.NewServer(mcp.ServerOptions{
		Version:          options.Version,
		Revision:         options.Revision,
		ConfigPath:       configPath,
		DefaultProjectID: flags.projectID,
		RepoRoot:         repoRoot,
		KeychainFactory:  options.KeychainFactory,
		HTTPClient:       options.HTTPClient,
		GitRunner:        options.GitRunner,
		LogWriter:        cmd.ErrOrStderr(),
	})

	return server.ServeStdio(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
}
