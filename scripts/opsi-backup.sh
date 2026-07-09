#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_DIR="${OPSI_BACKUP_DIR:-}"
ARTIFACT="${OPSI_BACKUP_ARTIFACT:-}"
CLOUD_DSN="${OPSI_CLOUD_DATABASE_URL:-${DATABASE_URL:-}}"
AGENT_ONLY="${OPSI_AGENT_ONLY_BACKUP:-}"
FORMAT="opsi-dr-backup-v2"
SCHEMA_VERSION="${OPSI_DR_SCHEMA_VERSION:-1}"

if [[ -z "$BACKUP_DIR" ]]; then
  echo "OPSI_BACKUP_DIR is required" >&2
  exit 2
fi
if [[ -z "$CLOUD_DSN" && "$AGENT_ONLY" != "1" ]]; then
  echo "OPSI_CLOUD_DATABASE_URL is required" >&2
  exit 2
fi
if [[ -z "$ARTIFACT" ]]; then
  ARTIFACT="$BACKUP_DIR/opsi-dr-backup.tar.gz"
fi

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/opsi-backup.XXXXXX")"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT
mkdir -p "$STAGE/cloud" "$STAGE/agent" "$BACKUP_DIR"

if [[ -n "$CLOUD_DSN" ]]; then
  if [[ -n "${OPSI_PGDUMP_CMD:-}" ]]; then
    read -r -a pgdump_cmd <<< "$OPSI_PGDUMP_CMD"
  else
    command -v pg_dump >/dev/null || { echo "pg_dump is required for Cloud DB backup" >&2; exit 2; }
    pgdump_cmd=(pg_dump)
  fi
  "${pgdump_cmd[@]}" --format=custom --no-owner --no-acl "$CLOUD_DSN" > "$STAGE/cloud/cloud.dump"
fi

if [[ -n "${OPSI_AGENT_DEPLOY_DB:-}${OPSI_AGENT_SERVICE_CATALOG_DB:-}${OPSI_AGENT_TELEMETRY_DB:-}" ]]; then
  (cd "$ROOT/agent" && env GOCACHE="${GOCACHE:-/tmp/opsi-go-cache}" GOTOOLCHAIN="${GOTOOLCHAIN:-local}" go run ./cmd/opsi-agent-dr backup \
    -dir "$STAGE" \
    -deploy-db "${OPSI_AGENT_DEPLOY_DB:-}" \
    -service-catalog-db "${OPSI_AGENT_SERVICE_CATALOG_DB:-}" \
    -telemetry-db "${OPSI_AGENT_TELEMETRY_DB:-}")
fi

cat > "$STAGE/manifest.json" <<JSON
{"format":"$FORMAT","artifact":"$ARTIFACT","cloud_schema_version":$SCHEMA_VERSION,"min_restore_schema_version":$SCHEMA_VERSION,"created_by":"scripts/opsi-backup.sh","covers":["cloud_postgres_all_current_tables_if_configured","cloud_projects_members_rbac_nodes_agents_bootstrap_services_deployments_relay_idempotency_audit_pat_hashes_if_configured","agent_deploy_sqlite_if_configured","agent_service_catalog_sqlite_if_configured","agent_telemetry_incident_audit_uptime_metadata_if_configured"],"requires_external":["cloud_signing_keys","cloud_bootstrap_encryption_key","agent_tls_private_key","agent_cloud_relay_hmac_secret","k3s_datastore_or_cluster_backup"],"excludes":["app_secret_values","PAT_values","ssh_private_keys","kubeconfig","raw_logs","raw_metrics","source_code_snapshots","docker_layers","managed_service_user_data","k3s_datastore"]}
JSON
tar -C "$STAGE" -czf "$ARTIFACT" .
"$ROOT/scripts/opsi-inspect-backup.sh" "$ARTIFACT" >/dev/null
echo "backup artifact: $ARTIFACT"
