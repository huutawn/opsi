# Install the Opsi CLI prerelease

The installer downloads a tagged prerelease binary and `checksums.txt` over
HTTPS, verifies SHA-256 before installation, and atomically replaces only the
selected `opsi` path. It does not require root.

```sh
OPSI_VERSION=r5-013-<short-sha> \
OPSI_INSTALL_DIR="$HOME/.local/bin" \
  ./scripts/install-cli.sh
```

The supported targets are Linux and macOS on amd64 and arm64. Existing files,
symlinks, directories, unsupported targets, non-HTTPS URLs, and checksum
mismatches are rejected. Use `OPSI_INSTALLER_SELF_TEST=1` to exercise checksum
and target safety checks without downloading anything. Use
`OPSI_INSTALL_SELF_TEST=1` after a real installation to run `opsi version`.
