# Install the Opsi CLI prerelease

The installer downloads a tagged prerelease archive and `checksums.txt` over
HTTPS, verifies SHA-256, and installs the `opsi` binary with adjacent
`opsi-ui` static assets. It does not require root or a repository checkout.

```sh
OPSI_VERSION=r5-014-<short-sha> \
OPSI_INSTALL_DIR="$HOME/.local/bin" \
  ./scripts/install-cli.sh
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
