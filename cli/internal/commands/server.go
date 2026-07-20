package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/spf13/cobra"
)

type serverFlags struct {
	projectID      string
	sessionID      string
	nodeID         string
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
	cmd.AddCommand(newServerConnectCommand(configPath, options))
	cmd.AddCommand(newServerAgentUpgradeCommand(configPath, options))
	cmd.AddCommand(newServerDecommissionCommand(configPath, options))
	return cmd
}

type agentUpgradeFlags struct {
	projectID       string
	nodeID          string
	artifactURL     string
	artifactSHA256  string
	expectedVersion string
	sshUsername     string
	sshPort         int
	identityFile    string
	knownHostsFile  string
}

func newServerAgentUpgradeCommand(configPath *string, options Options) *cobra.Command {
	flags := &agentUpgradeFlags{sshUsername: "ubuntu", sshPort: 22}
	cmd := &cobra.Command{
		Use:   "upgrade-agent",
		Short: "Atomically upgrade one Cloud-owned Agent without changing K3s or identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := flags.validate(); err != nil {
				return err
			}
			client, err := newCommandCloudClient(*configPath, options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			nodes, err := client.ListNodes(ctx, flags.projectID)
			if err != nil {
				return fmt.Errorf("resolve upgrade target through Cloud: %w", err)
			}
			node := findCloudNode(nodes, flags.nodeID)
			if node == nil || node.PublicHost == "" || node.AgentID == "" {
				return errors.New("Cloud returned no upgradeable node/Agent identity")
			}
			if err := runAtomicAgentUpgrade(ctx, *node, *flags); err != nil {
				return err
			}
			oldAgentID, oldVersion := node.AgentID, node.AgentVersion
			for {
				nodes, err = client.ListNodes(ctx, flags.projectID)
				if err == nil {
					current := findCloudNode(nodes, flags.nodeID)
					if current != nil && current.AgentID != oldAgentID {
						return errors.New("Agent identity changed during upgrade")
					}
					if current != nil && current.AgentVersion == flags.expectedVersion {
						return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"project_id": flags.projectID, "node_id": current.ID, "agent_id": current.AgentID, "previous_version": oldVersion, "version": current.AgentVersion, "artifact_sha256": flags.artifactSHA256, "k3s_changed": false})
					}
				}
				select {
				case <-ctx.Done():
					return errors.New("Agent upgraded locally but the expected Cloud heartbeat did not arrive before timeout")
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "Cloud project ID")
	cmd.Flags().StringVar(&flags.nodeID, "node-id", "", "existing Cloud node ID")
	cmd.Flags().StringVar(&flags.artifactURL, "artifact-url", "", "public HTTPS opsi-agent binary URL")
	cmd.Flags().StringVar(&flags.artifactSHA256, "artifact-sha256", "", "immutable SHA-256 of the Agent binary")
	cmd.Flags().StringVar(&flags.expectedVersion, "expected-version", "", "release version expected in the recovered Cloud heartbeat")
	cmd.Flags().StringVar(&flags.sshUsername, "ssh-username", flags.sshUsername, "existing target SSH username")
	cmd.Flags().IntVar(&flags.sshPort, "ssh-port", flags.sshPort, "existing target SSH port")
	cmd.Flags().StringVar(&flags.identityFile, "identity-file", "", "protected SSH private key path")
	cmd.Flags().StringVar(&flags.knownHostsFile, "known-hosts-file", "", "dedicated strict known_hosts path")
	return cmd
}

func (f agentUpgradeFlags) validate() error {
	if f.projectID == "" || f.nodeID == "" || f.artifactURL == "" || f.artifactSHA256 == "" || f.expectedVersion == "" || f.identityFile == "" || f.knownHostsFile == "" {
		return errors.New("project-id, node-id, artifact-url, artifact-sha256, expected-version, identity-file, and known-hosts-file are required")
	}
	if f.sshPort < 1 || f.sshPort > 65535 || !safeSSHAtom(f.sshUsername) || !safeReleaseVersion(f.expectedVersion) || !lowerHexSHA256(f.artifactSHA256) {
		return errors.New("Agent upgrade SSH, version, or checksum input is invalid")
	}
	parsed, err := url.Parse(f.artifactURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || !safeArtifactURL(f.artifactURL) {
		return errors.New("artifact-url must be a public HTTPS URL without credentials or fragments")
	}
	if err := validateProtectedRegularFile(f.identityFile, true); err != nil {
		return fmt.Errorf("identity-file: %w", err)
	}
	if err := validateProtectedRegularFile(f.knownHostsFile, false); err != nil {
		return fmt.Errorf("known-hosts-file: %w", err)
	}
	return nil
}

func findCloudNode(nodes []cloudclient.Node, nodeID string) *cloudclient.Node {
	for index := range nodes {
		if nodes[index].ID == nodeID {
			return &nodes[index]
		}
	}
	return nil
}

func runAtomicAgentUpgrade(ctx context.Context, node cloudclient.Node, flags agentUpgradeFlags) error {
	if !safeSSHHost(node.PublicHost) {
		return errors.New("Cloud node public host is invalid")
	}
	remote := "sudo -n env OPSI_AGENT_URL='" + flags.artifactURL + "' OPSI_AGENT_SHA256='" + flags.artifactSHA256 + "' OPSI_AGENT_VERSION='" + flags.expectedVersion + "' sh -s"
	target := flags.sshUsername + "@" + node.PublicHost
	args := []string{"-p", fmt.Sprintf("%d", flags.sshPort), "-i", filepath.Clean(flags.identityFile), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Clean(flags.knownHostsFile), "-o", "HostKeyAlgorithms=ssh-ed25519", target, remote}
	command := exec.CommandContext(ctx, "ssh", args...)
	command.Stdin = strings.NewReader(atomicAgentUpgradeScript)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return errors.New("atomic Agent upgrade failed over strict SSH; inspect target systemd logs without exposing credentials")
	}
	if strings.TrimSpace(output.String()) != "AGENT_UPGRADE_OK "+flags.artifactSHA256 {
		return errors.New("atomic Agent upgrade returned an unexpected result")
	}
	return nil
}

func validateProtectedRegularFile(path string, private bool) error {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path must be a regular non-symlink file")
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		return errors.New("private key must not be accessible by group or other users")
	}
	return nil
}

func lowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func safeSSHAtom(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func safeReleaseVersion(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && !strings.ContainsRune("._+-", char) {
			return false
		}
	}
	return true
}

func safeArtifactURL(value string) bool {
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && !strings.ContainsRune("._~:/?&=%+-", char) {
			return false
		}
	}
	return true
}

func safeSSHHost(value string) bool {
	if net.ParseIP(value) != nil {
		return true
	}
	if value == "" || len(value) > 253 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '.' && char != '-' {
			return false
		}
	}
	return true
}

const atomicAgentUpgradeScript = `set -eu
umask 077
release_root=/opt/opsi/agent/releases
current_link=/opt/opsi/agent/current
previous_link=/opt/opsi/agent/previous
config=/etc/opsi/agent.yaml
target_release="$release_root/$OPSI_AGENT_SHA256"
target_binary="$target_release/opsi-agent"
case "$OPSI_AGENT_SHA256" in *[!0-9a-f]*|'') exit 20 ;; esac
[ "${#OPSI_AGENT_SHA256}" -eq 64 ] || exit 20
[ -f "$config" ] || exit 21
[ -L "$current_link" ] || exit 22
old_release="$(readlink -f "$current_link")"
case "$old_release" in "$release_root"/*) ;; *) exit 22 ;; esac
stage="$(mktemp -d "$release_root/.upgrade.XXXXXX")"
cleanup() { rm -rf "$stage"; }
trap cleanup EXIT
if [ ! -f "$target_binary" ]; then
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 --fail --silent --show-error --location "$OPSI_AGENT_URL" --output "$stage/opsi-agent"
  actual="$(sha256sum "$stage/opsi-agent" | awk '{print $1}')"
  [ "$actual" = "$OPSI_AGENT_SHA256" ] || exit 23
  chmod 0755 "$stage/opsi-agent"
  "$stage/opsi-agent" --config "$config" --check >/dev/null
  version_output="$("$stage/opsi-agent" --version)"
  case "$version_output" in *"version=$OPSI_AGENT_VERSION "*) ;; *) exit 24 ;; esac
  mv "$stage" "$target_release"
  stage="$(mktemp -d "$release_root/.upgrade-cleanup.XXXXXX")"
else
  actual="$(sha256sum "$target_binary" | awk '{print $1}')"
  [ "$actual" = "$OPSI_AGENT_SHA256" ] || exit 23
  "$target_binary" --config "$config" --check >/dev/null
  version_output="$("$target_binary" --version)"
  case "$version_output" in *"version=$OPSI_AGENT_VERSION "*) ;; *) exit 24 ;; esac
fi
atomic_link() {
  target="$1"
  link="$2"
  temporary="${link}.tmp.$$"
  rm -f "$temporary"
  ln -s "$target" "$temporary"
  mv -Tf "$temporary" "$link"
}
wait_health() {
  attempt=0
  while [ "$attempt" -lt 30 ]; do
    if curl --fail --silent --show-error http://127.0.0.1:9080/health >/dev/null 2>&1 && systemctl is-active --quiet opsi-agent; then return 0; fi
    attempt=$((attempt + 1))
    sleep 1
  done
  return 1
}
if [ "$old_release" != "$target_release" ]; then atomic_link "releases/${old_release##*/}" "$previous_link"; fi
atomic_link "releases/$OPSI_AGENT_SHA256" "$current_link"
if systemctl restart opsi-agent && wait_health; then
  printf 'AGENT_UPGRADE_OK %s\n' "$OPSI_AGENT_SHA256"
  exit 0
fi
atomic_link "releases/${old_release##*/}" "$current_link"
systemctl restart opsi-agent
wait_health || exit 26
exit 25
`

func newServerDecommissionCommand(configPath *string, options Options) *cobra.Command {
	flags := &serverFlags{}
	var confirmReset bool
	cmd := &cobra.Command{
		Use:   "decommission",
		Short: "Mark a reset server record offline before replacement bootstrap",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.nodeID == "" {
				return errors.New("project-id and node-id are required")
			}
			if !confirmReset {
				return errors.New("--confirm-reset is required after the target has been reset")
			}
			client, err := newCommandCloudClient(*configPath, options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			node, err := client.MarkNodeOffline(ctx, flags.projectID, flags.nodeID, "node-offline:"+flags.nodeID)
			if err != nil {
				return fmt.Errorf("mark node offline: %w", err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(node)
		},
	}
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.nodeID, "node-id", "", "node id")
	cmd.Flags().BoolVar(&confirmReset, "confirm-reset", false, "confirm the target was reset outside the Agent")
	return cmd
}

func newServerConnectCommand(configPath *string, options Options) *cobra.Command {
	flags := &serverFlags{}
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Resolve and atomically save a pinned direct Agent connection",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.nodeID == "" {
				return errors.New("project-id and node-id are required")
			}
			if *configPath == "" {
				return errors.New("--config is required to save Agent connection metadata")
			}
			client, err := newCommandCloudClient(*configPath, options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			nodes, err := client.ListNodes(ctx, flags.projectID)
			if err != nil {
				return fmt.Errorf("list project nodes: %w", err)
			}
			var node *cloudclient.Node
			for index := range nodes {
				if nodes[index].ID == flags.nodeID {
					node = &nodes[index]
					break
				}
			}
			if node == nil {
				return errors.New("node was not found in the requested project")
			}
			if node.AgentID == "" || node.AgentEndpoint == "" || node.AgentPort < 1 || node.AgentPort > 65535 || node.AgentTLSServerName == "" || len(node.AgentCertSHA256) != 64 {
				return errors.New("node has no complete direct TLS Agent metadata")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			cfg.AgentAddr = net.JoinHostPort(node.AgentEndpoint, fmt.Sprintf("%d", node.AgentPort))
			cfg.TLS.PinnedServerCertSHA256 = node.AgentCertSHA256
			cfg.TLS.ServerName = node.AgentTLSServerName
			if err := config.Save(filepath.Clean(*configPath), cfg); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"node_id": node.ID, "agent_id": node.AgentID, "agent_endpoint": node.AgentEndpoint,
				"agent_port": node.AgentPort, "tls_server_name": node.AgentTLSServerName, "certificate_sha256": node.AgentCertSHA256,
			})
		},
	}
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.nodeID, "node-id", "", "node id")
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
