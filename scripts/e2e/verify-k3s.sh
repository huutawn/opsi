#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-run}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="${OPSI_E2E_RUN_ID:-e2e-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
ARTIFACT_DIR="${OPSI_E2E_ARTIFACT_DIR:-$ROOT/.tmp/e2e-k3s/$RUN_ID}"
LOCAL_URL="${OPSI_E2E_LOCAL_URL:-http://127.0.0.1:9780}"
PROJECT_ID="${OPSI_E2E_PROJECT_ID:-}"
BUILD_RECORD_ID="${OPSI_E2E_BUILD_RECORD_ID:-}"
BAD_BUILD_RECORD_ID="${OPSI_E2E_BAD_BUILD_RECORD_ID:-}"
ENVIRONMENT_ID="${OPSI_E2E_ENVIRONMENT_ID:-}"
SERVICE_KEY="${OPSI_E2E_SERVICE_KEY:-}"
SERVICE_NAME="$SERVICE_KEY"
REPLICAS="${OPSI_E2E_REPLICAS:-}"
CONTAINER_PORT="${OPSI_E2E_CONTAINER_PORT:-}"
CPU_REQUEST="${OPSI_E2E_CPU_REQUEST:-}"
MEMORY_REQUEST="${OPSI_E2E_MEMORY_REQUEST:-}"
CPU_LIMIT="${OPSI_E2E_CPU_LIMIT:-}"
MEMORY_LIMIT="${OPSI_E2E_MEMORY_LIMIT:-}"
TARGET_HOST="${OPSI_E2E_VPS_HOST:-}"
TARGET_SSH_USER="${OPSI_E2E_VPS_SSH_USER:-root}"
TARGET_SSH_PORT="${OPSI_E2E_VPS_SSH_PORT:-22}"
OPSI_E2E_SSH_KEY_PATH="${OPSI_E2E_SSH_KEY_PATH:-}"
HOST_KEY_SHA256="${OPSI_E2E_VPS_HOST_KEY_SHA256:-}"
SECRET_NAME="${OPSI_E2E_SECRET_NAME:-opsi-e2e-secret}"
TOTP_CODE="${OPSI_E2E_TOTP_CODE:-}"
OTP_REQUEST_ID="${OPSI_E2E_OTP_REQUEST_ID:-}"
OTP_CODE="${OPSI_E2E_OTP_CODE:-}"
APP_SECRET_VALUE="${OPSI_E2E_APP_SECRET_VALUE:-e2e-secret-value-$RUN_ID}"
POLL_SECONDS="${OPSI_E2E_POLL_SECONDS:-900}"
KNOWN_HOSTS_FILE=""
BOOTSTRAP_REQUEST_FILE=""
SELF_TEST_DIR=""

cleanup_temps() {
  [ -z "$BOOTSTRAP_REQUEST_FILE" ] || rm -f -- "$BOOTSTRAP_REQUEST_FILE"
  [ -z "$KNOWN_HOSTS_FILE" ] || rm -f -- "$KNOWN_HOSTS_FILE"
  [ -z "$SELF_TEST_DIR" ] || rm -rf -- "$SELF_TEST_DIR"
}

trap cleanup_temps EXIT

usage() {
  cat <<'EOF'
Usage:
  make verify-e2e-k3s-preflight
  make verify-e2e-k3s
  ./scripts/e2e/verify-k3s.sh --self-test

Required env for full run:
  OPSI_E2E_PROJECT_ID
  OPSI_E2E_LOCAL_URL
  OPSI_E2E_VPS_HOST
  OPSI_E2E_SSH_KEY_PATH
  OPSI_E2E_VPS_HOST_KEY_SHA256
  OPSI_E2E_BUILD_RECORD_ID
  OPSI_E2E_BAD_BUILD_RECORD_ID
  OPSI_E2E_ENVIRONMENT_ID
  OPSI_E2E_SERVICE_KEY
  OPSI_E2E_REPLICAS
  OPSI_E2E_CONTAINER_PORT
  OPSI_E2E_CPU_REQUEST
  OPSI_E2E_MEMORY_REQUEST
  OPSI_E2E_CPU_LIMIT
  OPSI_E2E_MEMORY_LIMIT
  OPSI_E2E_TOTP_CODE or OPSI_E2E_OTP_REQUEST_ID + OPSI_E2E_OTP_CODE

  The local URL must be the CLI local backend. Immutable BuildRecords and
  topology/policy authority are resolved by Cloud; this script supplies no
  source, manifest, digest, or caller identity.
EOF
}

log() {
  mkdir -p "$ARTIFACT_DIR"
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$ARTIFACT_DIR/evidence.redacted.log"
}

fail() {
  log "FAIL: $*"
  manual_cleanup
  exit 1
}

redact() {
  python3 -c 'import re, sys
secrets = [s for s in sys.argv[1:] if s]
data = sys.stdin.read()
for s in secrets:
    data = data.replace(s, "[REDACTED]")
patterns = [
    r"(?i)(authorization\s*[:=]\s*bearer\s+)[^\s\",}]+",
    r"(?i)((token|agent_token|registration_token|pat|private_key|kubeconfig|app_secret|otp_code|totp_code)\s*[\"=:]+\s*)(\"[^\"]*\"|[^,\s}]+)",
]
for pat in patterns:
    data = re.sub(pat, lambda m: m.group(1) + "[REDACTED]", data)
sys.stdout.write(data)' "$APP_SECRET_VALUE" "$TOTP_CODE" "$OTP_CODE"
}

json_get() {
  python3 -c 'import json, sys
path = sys.argv[1].split(".")
data = json.load(sys.stdin)
for p in path:
    if p == "":
        continue
    if isinstance(data, list):
        data = data[int(p)]
    else:
        data = data[p]
print(data)' "$1"
}

json_first() {
  python3 -c 'import json, sys
arr = json.load(sys.stdin)
key, val = sys.argv[1], sys.argv[2]
if isinstance(arr, dict):
    arr = arr.get("events") or arr.get("deployments") or arr.get("sessions") or arr.get("incidents") or arr.get("actions") or []
for item in arr:
    if str(item.get(key, "")) == val:
        print(item.get("id", ""))
        raise SystemExit(0)
raise SystemExit(1)' "$1" "$2"
}

need_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "missing tool: $1"
}

need_env() {
  [ -n "${!1:-}" ] || fail "missing env: $1"
}

validate_ssh_key_path() {
  local raw="$OPSI_E2E_SSH_KEY_PATH" resolved
  [ -n "$raw" ] || return 1
  resolved="$(python3 - "$raw" <<'PY'
import os, stat, sys

path = os.path.abspath(os.path.expanduser(sys.argv[1]))
current = os.path.sep
for part in path.split(os.path.sep)[1:]:
    current = os.path.join(current, part)
    try:
        info = os.lstat(current)
    except OSError:
        raise SystemExit("SSH key path cannot be accessed")
    if stat.S_ISLNK(info.st_mode):
        raise SystemExit("SSH key path must not contain symlinks")
info = os.lstat(path)
if not stat.S_ISREG(info.st_mode):
    raise SystemExit("SSH key path must be a regular file")
if not info.st_mode & stat.S_IRUSR or not os.access(path, os.R_OK):
    raise SystemExit("SSH key file must be readable")
if info.st_mode & 0o077:
    raise SystemExit("SSH key file must not grant group or other permissions")
if info.st_size == 0:
    raise SystemExit("SSH key file must not be empty")
if info.st_size > 1024 * 1024:
    raise SystemExit("SSH key file exceeds 1 MiB")
with open(path, "rb") as key_file:
    key = key_file.read(1024 * 1024 + 1)
markers = (
    b"-----BEGIN " + b"OPENSSH PRIVATE KEY-----",
    b"-----BEGIN " + b"PRIVATE KEY-----",
    b"-----BEGIN " + b"ENCRYPTED PRIVATE KEY-----",
    b"-----BEGIN " + b"RSA PRIVATE KEY-----",
    b"-----BEGIN " + b"EC PRIVATE KEY-----",
    b"-----BEGIN " + b"DSA PRIVATE KEY-----",
)
if not any(marker in key for marker in markers):
    raise SystemExit("SSH key file has no recognized private-key marker")
print(path)
PY
)" || return 1
  OPSI_E2E_SSH_KEY_PATH="$resolved"
}

select_host_key() {
  local candidates="$1" expected="$2" output="$3" line fingerprint matches=0
  : > "$output"
  chmod 600 "$output"
  while IFS= read -r line; do
    [ -n "$line" ] && [ "${line#\#}" = "$line" ] || continue
    fingerprint="$(printf '%s\n' "$line" | ssh-keygen -lf - -E sha256 2>/dev/null | awk 'NR == 1 { print $2 }')"
    if [ "$fingerprint" = "$expected" ]; then
      printf '%s\n' "$line" >> "$output"
      matches=$((matches + 1))
    fi
  done < "$candidates"
  [ "$matches" -eq 1 ]
}

pin_host_identity() {
  local candidates
  [[ "$TARGET_HOST" != -* && "$TARGET_HOST" != *[$' \t\r\n']* ]] || fail "invalid OPSI_E2E_VPS_HOST"
  [[ "$TARGET_SSH_PORT" =~ ^[0-9]+$ ]] && [ "$TARGET_SSH_PORT" -ge 1 ] && [ "$TARGET_SSH_PORT" -le 65535 ] || fail "invalid OPSI_E2E_VPS_SSH_PORT"
  [[ "$HOST_KEY_SHA256" =~ ^SHA256:[A-Za-z0-9+/]{43}=?$ ]] || fail "invalid OPSI_E2E_VPS_HOST_KEY_SHA256"
  candidates="$(mktemp)"
  KNOWN_HOSTS_FILE="$(mktemp)"
  chmod 600 "$candidates" "$KNOWN_HOSTS_FILE"
  if ! timeout 15s ssh-keyscan -T 5 -p "$TARGET_SSH_PORT" "$TARGET_HOST" > "$candidates" 2>/dev/null; then
    rm -f -- "$candidates"
    fail "SSH host-key scan failed"
  fi
  if ! select_host_key "$candidates" "$HOST_KEY_SHA256" "$KNOWN_HOSTS_FILE"; then
    rm -f -- "$candidates"
    fail "SSH host-key fingerprint had zero or multiple matches"
  fi
  rm -f -- "$candidates"
}

preflight() {
  mkdir -p "$ARTIFACT_DIR"
  log "preflight: artifact_dir=$ARTIFACT_DIR"
  for t in bash curl python3 ssh ssh-keygen ssh-keyscan timeout go node npm kubectl; do need_tool "$t"; done
  need_env OPSI_E2E_PROJECT_ID
  need_env OPSI_E2E_VPS_HOST
  need_env OPSI_E2E_SSH_KEY_PATH
  need_env OPSI_E2E_VPS_HOST_KEY_SHA256
  for name in OPSI_E2E_BUILD_RECORD_ID OPSI_E2E_BAD_BUILD_RECORD_ID OPSI_E2E_ENVIRONMENT_ID OPSI_E2E_SERVICE_KEY OPSI_E2E_REPLICAS OPSI_E2E_CONTAINER_PORT OPSI_E2E_CPU_REQUEST OPSI_E2E_MEMORY_REQUEST OPSI_E2E_CPU_LIMIT OPSI_E2E_MEMORY_LIMIT; do
    need_env "$name"
  done
  if [ -z "$TOTP_CODE" ] && { [ -z "$OTP_REQUEST_ID" ] || [ -z "$OTP_CODE" ]; }; then
    fail "missing second factor: set OPSI_E2E_TOTP_CODE or OPSI_E2E_OTP_REQUEST_ID + OPSI_E2E_OTP_CODE"
  fi
  validate_ssh_key_path || fail "OPSI_E2E_SSH_KEY_PATH failed protected private-key validation"
  pin_host_identity
  curl -fsS "$LOCAL_URL/health" >/dev/null || fail "local backend unavailable at OPSI_E2E_LOCAL_URL"
  remote_k3s 'test "$(uname -s)" = Linux && test -r /etc/os-release' >/dev/null || fail "SSH key authentication/preflight failed"
  log "preflight: ok"
}

session_token() {
  local body
  body="$(curl -fsS "$LOCAL_URL/api/local/session")" || fail "local session unavailable"
  printf '%s' "$body" | redact > "$ARTIFACT_DIR/session.redacted.json"
  printf '%s' "$body" | json_get local_session
}

api_file() {
  local method="$1" path="$2" body_file="$3" label="$4" write="${5:-0}"
  local out status headers=(-H "content-type: application/json" -H "X-Request-ID: $RUN_ID-$label")
  if [ "$write" = "1" ]; then
    headers+=(-H "Idempotency-Key: $RUN_ID-$label" -H "X-Local-Session: $LOCAL_SESSION")
  fi
  out="$(mktemp)"
  if [ "$body_file" = "-" ]; then
    status="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "${headers[@]}" "$LOCAL_URL$path")" || status="000"
  else
    status="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "${headers[@]}" --data-binary "@$body_file" "$LOCAL_URL$path")" || status="000"
  fi
  redact < "$out" > "$ARTIFACT_DIR/$label.redacted.json"
  if [ "${status#2}" = "$status" ]; then
    log "api $label failed status=$status body=$(tr '\n' ' ' < "$ARTIFACT_DIR/$label.redacted.json")"
    rm -f "$out"
    return 1
  fi
  cat "$out"
  rm -f "$out"
}

write_json() {
  local file="$1" expr="$2" key_path="${3:-}"
  python3 - "$file" "$expr" "$key_path" <<'PY'
import json, os, stat, sys
file, kind, key_path = sys.argv[1:4]
e = os.environ
if kind == "bootstrap":
    fd = os.open(key_path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_size < 1 or info.st_size > 1024 * 1024:
            raise SystemExit("SSH key changed after validation")
        key = os.read(fd, 1024 * 1024 + 1)
    finally:
        os.close(fd)
    markers = (
        b"-----BEGIN " + b"OPENSSH PRIVATE KEY-----",
        b"-----BEGIN " + b"PRIVATE KEY-----",
        b"-----BEGIN " + b"ENCRYPTED PRIVATE KEY-----",
        b"-----BEGIN " + b"RSA PRIVATE KEY-----",
        b"-----BEGIN " + b"EC PRIVATE KEY-----",
        b"-----BEGIN " + b"DSA PRIVATE KEY-----",
    )
    if len(key) > 1024 * 1024 or not any(marker in key for marker in markers):
        raise SystemExit("SSH key changed after validation")
    data = {"role":"first_server","public_host":e["OPSI_E2E_VPS_HOST"],"ssh_port":int(e.get("OPSI_E2E_VPS_SSH_PORT","22")),"ssh_username":e.get("OPSI_E2E_VPS_SSH_USER","root"),"auth_method":"private_key","ssh_private_key":key.decode("utf-8")}
elif kind in {"deployment", "bad_deployment"}:
    build_record_id = e["OPSI_E2E_BUILD_RECORD_ID"] if kind == "deployment" else e["OPSI_E2E_BAD_BUILD_RECORD_ID"]
    data = {
        "schema_version":"opsi.deployment_job/v1",
        "build_record_id":build_record_id,
        "environment_id":e["OPSI_E2E_ENVIRONMENT_ID"],
        "workload":{
            "schema_version":"opsi.workload_spec/v1",
            "service_key":e["OPSI_E2E_SERVICE_KEY"],
            "application_container_name":"app",
            "replicas":int(e["OPSI_E2E_REPLICAS"]),
            "container_port":int(e["OPSI_E2E_CONTAINER_PORT"]),
            "resources":{
                "requests":{"cpu":e["OPSI_E2E_CPU_REQUEST"],"memory":e["OPSI_E2E_MEMORY_REQUEST"]},
                "limits":{"cpu":e["OPSI_E2E_CPU_LIMIT"],"memory":e["OPSI_E2E_MEMORY_LIMIT"]},
            },
            "termination_grace_period_seconds":30,
            "exposure":{"mode":"internal"},
        },
    }
elif kind == "secret":
    data = {"service_id":e["OPSI_E2E_SERVICE_ID"],"name":e.get("OPSI_E2E_SECRET_NAME","opsi-e2e-secret"),"namespace":"default"}
elif kind == "second_factor":
    data = {"service_id":e["OPSI_E2E_SERVICE_ID"],"name":e.get("OPSI_E2E_SECRET_NAME","opsi-e2e-secret"),"namespace":"default","reveal":True}
    if e.get("OPSI_E2E_TOTP_CODE"): data["totp_code"] = e["OPSI_E2E_TOTP_CODE"]
    else: data.update({"otp_request_id":e["OPSI_E2E_OTP_REQUEST_ID"],"otp_code":e["OPSI_E2E_OTP_CODE"]})
elif kind == "incident_resolve":
    data = {}
else:
    raise SystemExit("unknown json kind")
open(file, "w").write(json.dumps(data))
PY
}

wait_json_field() {
  local path="$1" field="$2" expect="$3" label="$4" start now body value
  start="$(date +%s)"
  while :; do
    body="$(api_file GET "$path" - "$label" 0)" || true
    value="$(printf '%s' "$body" | json_get "$field" 2>/dev/null || true)"
    [ "$value" = "$expect" ] && return 0
    now="$(date +%s)"
    [ $((now - start)) -lt "$POLL_SECONDS" ] || fail "timeout waiting for $path field $field=$expect, last=$value"
    sleep 10
  done
}

wait_deployment_status() {
  local deploy_id="$1" expect="$2" start now body value
  start="$(date +%s)"
  while :; do
    body="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$deploy_id" - "deployment-$deploy_id" 0)" || true
    value="$(printf '%s' "$body" | json_get status 2>/dev/null || true)"
    [ "$value" = "$expect" ] && return 0
    case "$value" in
      failed|succeeded) fail "deployment $deploy_id reached $value while waiting for $expect" ;;
    esac
    now="$(date +%s)"
    [ $((now - start)) -lt "$POLL_SECONDS" ] || fail "timeout waiting for deployment $deploy_id=$expect, last=$value"
    sleep 10
  done
}

check_artifacts_clean() {
  python3 - "$ARTIFACT_DIR" "$APP_SECRET_VALUE" "$TOTP_CODE" "$OTP_CODE" <<'PY'
import pathlib, re, sys
root = pathlib.Path(sys.argv[1])
secrets = [s for s in sys.argv[2:] if s]
for path in root.rglob("*"):
    if not path.is_file():
        continue
    text = path.read_text(errors="ignore")
    if re.search(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----", text):
        print(path)
        raise SystemExit(1)
    for secret in secrets:
        if secret and secret in text:
            print(path)
            raise SystemExit(1)
PY
}

remote_k3s() {
  ssh -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$KNOWN_HOSTS_FILE" -o ConnectTimeout=20 -i "$OPSI_E2E_SSH_KEY_PATH" -p "$TARGET_SSH_PORT" "$TARGET_SSH_USER@$TARGET_HOST" "$@"
}

verify_runtime() {
  remote_k3s "sudo k3s kubectl -n default rollout status deployment/$SERVICE_NAME --timeout=120s" | redact > "$ARTIFACT_DIR/k3s-rollout.redacted.log" || fail "K3s rollout status failed"
  remote_k3s "sudo k3s kubectl -n default get deploy,svc,pods -l app.kubernetes.io/name=$SERVICE_NAME -o wide" | redact > "$ARTIFACT_DIR/k3s-runtime.redacted.log" || fail "K3s runtime state failed"
}

verify_incident_detail() {
  local incident_id="$1"
  python3 -c 'import json, sys
expected = sys.argv[1]
data = json.load(sys.stdin)
incident = data.get("incident", data)
if incident.get("incident_id") != expected:
    raise SystemExit("incident detail returned the wrong incident_id")
forbidden = {
    "root" + "_cause",
    "recommended" + "_actions",
    "rca" + "_metadata",
    "action" + "_hash",
    "mitigation" + "_actions_json",
}
def walk(value):
    if isinstance(value, dict):
        for key, child in value.items():
            if key.lower() in forbidden:
                raise SystemExit(f"legacy incident field exposed: {key}")
            walk(child)
    elif isinstance(value, list):
        for child in value:
            walk(child)
walk(data)' "$incident_id"
}

verify_agent_incident_resolve_audit() {
  local incident_id="$1" project_q incident_q
  printf -v project_q '%q' "$PROJECT_ID"
  printf -v incident_q '%q' "$incident_id"
  remote_k3s "sudo python3 -c 'import sqlite3,sys; row=sqlite3.connect(sys.argv[1]).execute(\"SELECT COUNT(*) FROM audit_log WHERE project_id=? AND action=? AND resource_type=? AND resource_id=? AND result=?\",(sys.argv[2],\"incident.resolve\",\"incident\",sys.argv[3],\"success\")).fetchone(); ok=bool(row and row[0] > 0); print(\"incident.resolve audit verified\" if ok else \"\"); raise SystemExit(0 if ok else 1)' /var/lib/opsi/opsi-agent.sqlite $project_q $incident_q" \
    > "$ARTIFACT_DIR/incident-resolve-audit.txt" || fail "Agent incident.resolve audit missing"
}

manual_cleanup() {
  mkdir -p "$ARTIFACT_DIR"
  cat > "$ARTIFACT_DIR/cleanup.txt" <<EOF
Manual cleanup for run $RUN_ID:
  Re-establish the same PEM-only, fingerprint-pinned SSH boundary before any target cleanup.
  Delete only the Opsi-owned Deployment/Service labeled app.kubernetes.io/name=$SERVICE_NAME.
  Review the reset script with --dry-run before any separately authorized reset.
  Review $LOCAL_URL via local UI and revoke/remove E2E project resources created with idempotency prefix $RUN_ID.
EOF
}

run_e2e() {
  preflight
  LOCAL_SESSION="$(session_token)"
  [ -n "$LOCAL_SESSION" ] || fail "local session token missing"
  local f body id good_deploy_id bad_deploy_id service_id incidents incident_id incident_detail resolve audit deployment_events deployment_record
  BOOTSTRAP_REQUEST_FILE="$(mktemp)"
  chmod 600 "$BOOTSTRAP_REQUEST_FILE"
  write_json "$BOOTSTRAP_REQUEST_FILE" bootstrap "$OPSI_E2E_SSH_KEY_PATH"
  if ! body="$(api_file POST "/api/local/projects/$PROJECT_ID/nodes/bootstrap" "$BOOTSTRAP_REQUEST_FILE" bootstrap 1)"; then
    rm -f -- "$BOOTSTRAP_REQUEST_FILE"
    BOOTSTRAP_REQUEST_FILE=""
    fail "bootstrap session create failed"
  fi
  rm -f -- "$BOOTSTRAP_REQUEST_FILE"
  BOOTSTRAP_REQUEST_FILE=""
  id="$(printf '%s' "$body" | json_get id)" || fail "bootstrap response missing id"
  wait_json_field "/api/local/projects/$PROJECT_ID/bootstrap-sessions/$id" status completed bootstrap-session
  log "step 1/11 bootstrap completed through local backend: session=$id target=$TARGET_HOST"
  wait_json_field "/api/local/projects/$PROJECT_ID/readiness" status ready readiness
  log "step 2/11 Agent heartbeat/readiness verified"

  f="$(mktemp)"; write_json "$f" deployment
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/deployments" "$f" deploy-create 1)" || fail "immutable deployment create failed"
  rm -f "$f"
  good_deploy_id="$(printf '%s' "$body" | json_get id)" || fail "deployment response missing id"
  wait_deployment_status "$good_deploy_id" succeeded
  service_id="$(printf '%s' "$body" | json_get service_id)" || fail "canonical deployment response missing service_id"
  export OPSI_E2E_SERVICE_ID="$service_id"
  log "step 3/11 immutable deployment succeeded: service=$service_id service_key=$SERVICE_KEY deployment=$good_deploy_id build_record=$BUILD_RECORD_ID"
  verify_runtime
  log "step 4/11 K3s rollout/runtime verified"

  f="$(mktemp)"; write_json "$f" secret
  api_file POST "/api/local/projects/$PROJECT_ID/secrets" "$f" secret-create 1 >/dev/null || fail "secret create failed"
  write_json "$f" second_factor
  api_file POST "/api/local/projects/$PROJECT_ID/secrets/$SECRET_NAME/rotate" "$f" secret-rotate 1 >/dev/null || fail "secret rotate failed"
  if api_file POST "/api/local/projects/$PROJECT_ID/secrets/$SECRET_NAME/reveal" "$f" secret-reveal 1 | grep -q "$APP_SECRET_VALUE"; then
    fail "secret value leaked into reveal output"
  fi
  rm -f "$f"
  log "step 5/11 secret create/rotate/reveal path ran via local Agent facade"

  api_file GET "/api/local/projects/$PROJECT_ID/telemetry/summary?service_id=$service_id" - telemetry-summary 0 >/dev/null || fail "telemetry summary failed"
  api_file GET "/api/local/projects/$PROJECT_ID/logs?service_id=$service_id&limit=50" - logs 0 >/dev/null || fail "logs failed"
  log "step 6/11 sanitized telemetry/logs fetched through local backend"

  f="$(mktemp)"; write_json "$f" bad_deployment
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/deployments" "$f" bad-deploy-create 1)" || fail "bad immutable deployment create failed"
  rm -f "$f"
  bad_deploy_id="$(printf '%s' "$body" | json_get id)" || fail "bad deployment response missing id"
  wait_deployment_status "$bad_deploy_id" failed
  log "step 7/11 controlled immutable failure reached canonical failed state: deployment=$bad_deploy_id build_record=$BAD_BUILD_RECORD_ID"
  incidents="$(api_file GET "/api/local/projects/$PROJECT_ID/incidents?status=open&limit=10" - incidents 0)" || fail "incident list failed"
  incident_id="$(printf '%s' "$incidents" | python3 -c 'import json,sys; d=json.load(sys.stdin); a=d.get("incidents", d if isinstance(d,list) else []); print(next((item.get("incident_id","") for item in a if item.get("service_id") == sys.argv[1]), ""))' "$service_id")"
  [ -n "$incident_id" ] || fail "no controlled incident found; E2E does not pass without a real Agent incident"
  incident_detail="$(api_file GET "/api/local/projects/$PROJECT_ID/incidents/$incident_id" - incident-detail 0)" || fail "incident detail failed"
  printf '%s' "$incident_detail" | verify_incident_detail "$incident_id" || fail "incident detail violated factual contract"
  f="$(mktemp)"; write_json "$f" incident_resolve
  resolve="$(api_file POST "/api/local/projects/$PROJECT_ID/incidents/$incident_id/resolve" "$f" incident-resolve 1)" || fail "incident resolve failed"
  rm -f "$f"
  if [ "$(printf '%s' "$resolve" | json_get status 2>/dev/null || true)" != "resolved" ]; then
    incident_detail="$(api_file GET "/api/local/projects/$PROJECT_ID/incidents/$incident_id" - incident-detail-resolved 0)" || fail "resolved incident detail failed"
    [ "$(printf '%s' "$incident_detail" | json_get incident.status 2>/dev/null || true)" = "resolved" ] || fail "incident status was not resolved"
  fi
  verify_agent_incident_resolve_audit "$incident_id"
  log "step 8/11 factual incident list/detail/resolve lifecycle verified: incident=$incident_id"

  deployment_events="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$good_deploy_id/events" - deployment-events 0)" || fail "deployment events fetch failed"
  printf '%s' "$deployment_events" | grep -q 'DEPLOYMENT_SUCCEEDED' || fail "deployment success event missing"
  audit="$(api_file GET "/api/local/projects/$PROJECT_ID/audit" - audit 0)" || fail "audit fetch failed"
  printf '%s' "$audit" | grep -q 'IMMUTABLE_DEPLOYMENT_CREATED' || fail "immutable deployment audit event missing"
  deployment_record="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$good_deploy_id" - deployment-evidence 0)" || fail "canonical deployment evidence fetch failed"
  for evidence in "$good_deploy_id" "$BUILD_RECORD_ID" 'sha256:' 'runtime_id' 'node_id' 'agent_id' 'succeeded'; do
    printf '%s' "$deployment_record" | grep -q "$evidence" || fail "canonical deployment evidence missing $evidence"
  done
  log "step 9/11 immutable deployment evidence verified: job=$good_deploy_id build_record=$BUILD_RECORD_ID"
  check_artifacts_clean || fail "redaction failed: artifact contains sensitive value"
  log "step 10/11 artifacts verified without sensitive payloads"
  manual_cleanup
  log "step 11/11 cleanup instructions written"
  log "PASS: clean VPS/K3s E2E proof complete"
}

self_test() {
  local key_public fixture match original_key forbidden pem_marker expected request
  mkdir -p "$ARTIFACT_DIR"
  OPSI_E2E_APP_SECRET_VALUE="app-secret" OPSI_E2E_TOTP_CODE="123456" OPSI_E2E_OTP_CODE="" \
    bash -c 'printf "token=abc kubeconfig=raw app-secret 123456" | '"$0"' --redact-only' > "$ARTIFACT_DIR/redaction-test.txt"
  grep -q '\[REDACTED\]' "$ARTIFACT_DIR/redaction-test.txt" || fail "self-test redaction failed"
  original_key="$OPSI_E2E_SSH_KEY_PATH"
  OPSI_E2E_SSH_KEY_PATH=""
  if validate_ssh_key_path >/dev/null 2>&1; then fail "self-test accepted missing PEM-key input"; fi
  SELF_TEST_DIR="$(mktemp -d)"
  ssh-keygen -q -t ed25519 -N '' -f "$SELF_TEST_DIR/key"
  chmod 600 "$SELF_TEST_DIR/key"
  ln -s "$SELF_TEST_DIR/key" "$SELF_TEST_DIR/key-link"
  OPSI_E2E_SSH_KEY_PATH="$SELF_TEST_DIR/key-link"
  if validate_ssh_key_path >/dev/null 2>&1; then fail "self-test accepted symlink key"; fi
  chmod 640 "$SELF_TEST_DIR/key"
  OPSI_E2E_SSH_KEY_PATH="$SELF_TEST_DIR/key"
  if validate_ssh_key_path >/dev/null 2>&1; then fail "self-test accepted insecure key mode"; fi
  chmod 600 "$SELF_TEST_DIR/key"
  validate_ssh_key_path >/dev/null || fail "self-test rejected protected key"
  request="$SELF_TEST_DIR/bootstrap.json"
  : > "$request"
  chmod 600 "$request"
  OPSI_E2E_VPS_HOST=fixture OPSI_E2E_VPS_SSH_USER=fixture-user OPSI_E2E_VPS_SSH_PORT=22 \
    write_json "$request" bootstrap "$OPSI_E2E_SSH_KEY_PATH" || fail "self-test bootstrap JSON generation failed"
  [ "$(stat -c '%a' "$request")" = 600 ] || fail "self-test bootstrap request mode was not 0600"
  python3 - "$request" <<'PY' || fail "self-test bootstrap JSON omitted the PEM credential"
import json, sys
with open(sys.argv[1]) as request_file:
    data = json.load(request_file)
if data.get("auth_method") != "private_key" or "ssh_private_key" not in data or "PRIVATE KEY" not in data["ssh_private_key"]:
    raise SystemExit(1)
PY
  rm -f -- "$request"
  key_public="$(<"$SELF_TEST_DIR/key.pub")"
  fixture="$SELF_TEST_DIR/host-keys"
  match="$SELF_TEST_DIR/known-hosts"
  printf 'fixture %s\n' "$key_public" > "$fixture"
  expected="$(ssh-keygen -lf "$fixture" -E sha256 | awk 'NR == 1 { print $2 }')"
  select_host_key "$fixture" "$expected" "$match" || fail "self-test rejected correct host fingerprint"
  if select_host_key "$fixture" "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" "$match"; then
    fail "self-test accepted incorrect host fingerprint"
  fi
  pem_marker='-----BEGIN OPENSSH '"PRIVATE KEY-----"
  printf '%s\n' "$pem_marker" > "$ARTIFACT_DIR/pem-leak.txt"
  if check_artifacts_clean >/dev/null 2>&1; then fail "self-test artifact validation accepted a PEM marker"; fi
  rm -f -- "$ARTIFACT_DIR/pem-leak.txt"
  OPSI_E2E_SSH_KEY_PATH="$original_key"
  printf '{"incident":{"incident_id":"inc-self-test","status":"open"}}' | verify_incident_detail inc-self-test || fail "self-test factual incident detail failed"
  if printf '{"incident":{"incident_id":"inc-self-test","action_%s":"legacy"}}' hash | verify_incident_detail inc-self-test >/dev/null 2>&1; then
    fail "self-test legacy incident field was accepted"
  fi
  if env -i PATH="$PATH" OPSI_E2E_ARTIFACT_DIR="$ARTIFACT_DIR/missing" "$0" --preflight >/tmp/opsi-e2e-preflight.out 2>&1; then
    fail "self-test missing prereq did not fail"
  fi
  grep -Eq "missing (env|tool):" /tmp/opsi-e2e-preflight.out || fail "self-test missing prereq message unclear"
  grep -q 'POST "/api/local/projects/\$PROJECT_ID/deployments"' "$0" || fail "self-test canonical deployment endpoint missing"
  grep -q 'GET "/api/local/projects/\$PROJECT_ID/deployments/\$deploy_id"' "$0" || fail "self-test canonical deployment polling missing"
  grep -q 'build_record_id' "$0" || fail "self-test BuildRecord request construction missing"
  grep -q 'auth_method":"private_key"' "$0" || fail "self-test PEM bootstrap request missing"
  grep -q 'ssh_private_key' "$0" || fail "self-test PEM bootstrap field missing"
  grep -q 'UserKnownHostsFile=' "$0" || fail "self-test dedicated known_hosts missing"
  grep -q "k3s kubectl" "$0" || fail "self-test real K3s check missing"
  grep -q "X-Local-Session" "$0" || fail "self-test local session guard missing"
  grep -q 'incidents/\$incident_id/resolve' "$0" || fail "self-test incident resolve path missing"
  legacy_scope='services''/'
  if grep -q "$legacy_scope" "$0"; then fail "self-test found a service-scoped deployment surface"; fi
  for forbidden in 'repo''_url' 'git''_sha' 'docker''file' 'manifest''_path' 'requested''_by' 'user''_id' 'role''='; do
    if grep -q "$forbidden" "$0"; then fail "self-test found retired caller/source field: $forbidden"; fi
  done
  for forbidden in 'pass''word' 'ssh''pass' 'accept''-new'; do
    if grep -qi "$forbidden" "$0"; then fail "self-test found retired SSH transport token: $forbidden"; fi
  done
  grep -q "Manual cleanup" "$0" || fail "self-test cleanup path missing"
  log "self-test: ok"
}

case "$MODE" in
  --help|-h) usage ;;
  --preflight) preflight ;;
  --self-test) self_test ;;
  --redact-only) redact ;;
  run) run_e2e ;;
  *) usage; exit 2 ;;
esac
