# Install the Opsi CLI prerelease

The installer downloads a tagged prerelease archive and `checksums.txt` over
HTTPS, verifies SHA-256, and installs the `opsi` binary with adjacent
`opsi-ui` static assets. It does not require root or a repository checkout.

```sh
curl -fsSL https://raw.githubusercontent.com/huutawn/opsi/main/scripts/install-cli.sh | sh
opsi start
```

Without `OPSI_VERSION`, the installer resolves the latest published beta.
Exact pins remain supported:

```sh
curl -fsSL https://raw.githubusercontent.com/huutawn/opsi/main/scripts/install-cli.sh \
  | OPSI_VERSION=v0.1.0-beta.2 sh
```

The supported targets are Linux and macOS on amd64 and arm64. Existing files,
symlinks, directories, unsupported targets, non-HTTPS URLs, and checksum
mismatches and unexpected archive paths are rejected. Use
`OPSI_INSTALLER_SELF_TEST=1` to exercise archive/checksum safety without
downloading anything. Use
`OPSI_INSTALL_SELF_TEST=1` after a real installation to run `opsi version`.

`OPSI_RELEASE_BASE_URL` changes both the archive and checksum authority. It is
therefore rejected unless `OPSI_ALLOW_UNSAFE_CUSTOM_MIRROR=1` is explicit; this
does not provide independent verification and trust moves to that mirror.

After installation:

```sh
opsi start
curl --fail http://127.0.0.1:9780/health
```

The browser entry point is served from the installed `opsi-ui/index.html`.
`OPSI_UI_DIR` remains an explicit development override only.

The Local UI uses the Hosted Beta Cloud at `https://opsidev.site` unless an
explicit YAML config selects a self-hosted `cloud_url`.

### Project-scoped Runtime Agent Observability & Network Requirements

Project observability in the Local Dashboard dynamically discovers all nodes and
agents for the selected project directly from the Cloud registry using an
`AgentTargetResolver` authority. Observability never falls back to loopback
`127.0.0.1:9443` or a single hardcoded `agent_addr`:

1. **Valid Cloud PAT**: Workstations running Opsi CLI/UI require a valid Cloud Personal Access Token (PAT) stored in the OS keychain via `opsi auth login` to query the Cloud registry for project agent targets.
2. **Local Client mTLS**: If the environment uses mutual TLS, `tls.client_cert_path` and `tls.client_key_path` must be configured in the local `cli.yaml` and reference readable certificate and private key files.
3. **Direct TLS Port Ingress**: Each agent on a VPS must have its registered TLS port (default `9443`) open in the VPS firewall (e.g., `ufw`, security group) and reachable directly from the workstation running Opsi CLI/UI.
4. **Direct VPS Access & No Cloud Relay**: In this release, the Opsi CLI/UI connects directly to each VPS agent endpoint over TLS with SHA-256 certificate pinning. There is no Cloud telemetry relay; if workstation network or firewall blocks an agent port, the dashboard displays `Unavailable` or `Partial` with actionable diagnostic guidance rather than falsely assuming workloads or servers are healthy.
5. **Connection Diagnostics**: `opsi server connect --project-id <project> --node-id <node>` resolves Cloud metadata and saves direct configuration for standalone CLI commands without project context. Run `opsi status` afterwards to verify reachability, port access, and TLS pinning.
