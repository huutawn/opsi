#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARTIFACT="${OPSI_BACKUP_ARTIFACT:-}"
RESTORE_DIR="${OPSI_RESTORE_DIR:-}"
CLOUD_DSN="${OPSI_CLOUD_DATABASE_URL:-${DATABASE_URL:-}}"
CLOUD_ONLY="${OPSI_CLOUD_ONLY_RESTORE:-}"
AGENT_ONLY="${OPSI_AGENT_ONLY_RESTORE:-}"

if [[ -z "$ARTIFACT" ]]; then
  echo "OPSI_BACKUP_ARTIFACT is required" >&2
  exit 2
fi
if [[ ! -s "$ARTIFACT" ]]; then
  echo "backup artifact is missing or empty: $ARTIFACT" >&2
  exit 2
fi
if [[ -z "$RESTORE_DIR" ]]; then
  RESTORE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/opsi-restore.XXXXXX")"
else
  rm -rf "$RESTORE_DIR"
  mkdir -p "$RESTORE_DIR"
fi
tar -C "$RESTORE_DIR" -xzf "$ARTIFACT"

if [[ "$AGENT_ONLY" != "1" && -f "$RESTORE_DIR/cloud/cloud.dump" ]]; then
  if [[ -z "$CLOUD_DSN" ]]; then
    echo "OPSI_CLOUD_DATABASE_URL is required for Cloud DB restore" >&2
    exit 2
  fi
  if [[ -n "${OPSI_PGRESTORE_CMD:-}" ]]; then
    read -r -a pgrestore_cmd <<< "$OPSI_PGRESTORE_CMD"
  else
    command -v pg_restore >/dev/null || { echo "pg_restore is required for Cloud DB restore" >&2; exit 2; }
    pgrestore_cmd=(pg_restore)
  fi
  "${pgrestore_cmd[@]}" --clean --if-exists --no-owner --no-acl --dbname "$CLOUD_DSN" < "$RESTORE_DIR/cloud/cloud.dump"
fi

if [[ "$CLOUD_ONLY" != "1" && -n "${OPSI_AGENT_DEPLOY_DB:-}${OPSI_AGENT_SERVICE_CATALOG_DB:-}${OPSI_AGENT_TELEMETRY_DB:-}" ]]; then
  (cd "$ROOT/agent" && env GOCACHE="${GOCACHE:-/tmp/opsi-go-cache}" GOTOOLCHAIN="${GOTOOLCHAIN:-local}" go run ./cmd/opsi-agent-dr restore \
    -dir "$RESTORE_DIR" \
    -deploy-db "${OPSI_AGENT_DEPLOY_DB:-}" \
    -service-catalog-db "${OPSI_AGENT_SERVICE_CATALOG_DB:-}" \
    -telemetry-db "${OPSI_AGENT_TELEMETRY_DB:-}")
fi

echo "restore completed from: $ARTIFACT"
