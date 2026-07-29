#!/usr/bin/env bash
set -euo pipefail

ARTIFACT="${1:-${OPSI_BACKUP_ARTIFACT:-}}"
CURRENT_SCHEMA="${OPSI_DR_SCHEMA_VERSION:-1}"
DEFAULT_FORBIDDEN_PATTERN='-----BEGIN [A-Z ]*PRIVATE KEY-----|PAT-plaintext|raw-token-value-should-not-appear|app-secret-plaintext|raw log contains secret|password=[^[:space:]]+'
FORBIDDEN_PATTERN="${OPSI_DR_FORBIDDEN_PATTERN:-$DEFAULT_FORBIDDEN_PATTERN}"

if [[ -z "$ARTIFACT" ]]; then
  echo "backup artifact path is required" >&2
  exit 2
fi
if [[ ! -s "$ARTIFACT" ]]; then
  echo "backup artifact is missing or empty: $ARTIFACT" >&2
  exit 2
fi

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/opsi-inspect.XXXXXX")"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

tar -C "$STAGE" -xzf "$ARTIFACT"
if [[ ! -s "$STAGE/manifest.json" ]]; then
  echo "backup manifest.json is required" >&2
  exit 2
fi
if ! grep -q '"format":"opsi-dr-backup-v2"' "$STAGE/manifest.json"; then
  echo "unsupported backup format" >&2
  exit 2
fi

min_schema="$(sed -n 's/.*"min_restore_schema_version":\([0-9][0-9]*\).*/\1/p' "$STAGE/manifest.json" | head -n1)"
if [[ -z "$min_schema" ]]; then
  echo "backup manifest min_restore_schema_version is required" >&2
  exit 2
fi
if (( min_schema > CURRENT_SCHEMA )); then
  echo "backup requires newer restore schema: $min_schema > $CURRENT_SCHEMA" >&2
  exit 2
fi

if find "$STAGE" -type f | grep -E '(^|/)(id_rsa|id_ed25519|kubeconfig|config)$|[.](pem|key)$' >/dev/null; then
  echo "backup artifact contains forbidden key or kubeconfig filename" >&2
  exit 2
fi
if LC_ALL=C grep -R -a -E -- "$FORBIDDEN_PATTERN" "$STAGE" >/dev/null; then
  echo "backup artifact contains forbidden plaintext sensitive data" >&2
  exit 2
fi

echo "backup artifact inspection passed: $ARTIFACT"
