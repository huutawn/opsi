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
  | OPSI_VERSION=v0.1.0-beta.1 sh
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
explicit YAML config selects a self-hosted `cloud_url`. No Agent address is
invented: before `opsi server connect` or an explicit `agent_addr`, the Local
UI starts normally and reports the Agent as not connected.

The `v0.1.0-beta.1` support boundary is a single VPS with single-node K3s,
Web/API workloads, deployment, exposure, and rollback. DNS/TLS automation,
multi-VPS, managed databases, production hardening, and production SLA are
excluded from this beta.
