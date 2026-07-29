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
WORKLOAD_NAMESPACE="default"
REPLICAS="${OPSI_E2E_REPLICAS:-}"
CONTAINER_PORT="${OPSI_E2E_CONTAINER_PORT:-}"
CPU_REQUEST="${OPSI_E2E_CPU_REQUEST:-}"
MEMORY_REQUEST="${OPSI_E2E_MEMORY_REQUEST:-}"
CPU_LIMIT="${OPSI_E2E_CPU_LIMIT:-}"
MEMORY_LIMIT="${OPSI_E2E_MEMORY_LIMIT:-}"
TARGET_HOST="${OPSI_E2E_VPS_HOST:-}"
TARGET_SSH_USER="${OPSI_E2E_VPS_SSH_USER:-}"
TARGET_SSH_PORT="${OPSI_E2E_VPS_SSH_PORT:-22}"
OPSI_E2E_SSH_KEY_PATH="${OPSI_E2E_SSH_KEY_PATH:-}"
HOST_KEY_SHA256="${OPSI_E2E_VPS_HOST_KEY_SHA256:-}"
PUBLIC_HOSTNAME="${OPSI_E2E_PUBLIC_HOSTNAME:-}"
PUBLIC_PORT="${OPSI_E2E_PUBLIC_PORT:-80}"
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
  OPSI_E2E_PUBLIC_HOSTNAME

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
  [[ "$TARGET_HOST" != -* && "$TARGET_HOST" != *[$' \t\r\n']* ]] || return 1
  [[ "$TARGET_SSH_PORT" =~ ^[0-9]+$ ]] && [ "$TARGET_SSH_PORT" -ge 1 ] && [ "$TARGET_SSH_PORT" -le 65535 ] || return 1
  [[ "$HOST_KEY_SHA256" =~ ^SHA256:[A-Za-z0-9+/]{43}=?$ ]] || return 1
  candidates="$(mktemp)"
  KNOWN_HOSTS_FILE="$(mktemp)"
  chmod 600 "$candidates" "$KNOWN_HOSTS_FILE"
  if ! timeout 15s ssh-keyscan -T 5 -p "$TARGET_SSH_PORT" "$TARGET_HOST" > "$candidates" 2>/dev/null; then
    rm -f -- "$candidates"
    return 1
  fi
  if ! select_host_key "$candidates" "$HOST_KEY_SHA256" "$KNOWN_HOSTS_FILE"; then
    rm -f -- "$candidates"
    return 1
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
  need_env OPSI_E2E_VPS_SSH_USER
  need_env OPSI_E2E_PUBLIC_HOSTNAME
  if [ -z "$TOTP_CODE" ] && { [ -z "$OTP_REQUEST_ID" ] || [ -z "$OTP_CODE" ]; }; then
    fail "missing second factor: set OPSI_E2E_TOTP_CODE or OPSI_E2E_OTP_REQUEST_ID + OPSI_E2E_OTP_CODE"
  fi
  validate_ssh_key_path || fail "OPSI_E2E_SSH_KEY_PATH failed protected private-key validation"
  pin_host_identity || fail "SSH host-key fingerprint pinning failed"
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
elif kind == "exposure":
    import hashlib
    exposure = {
        "schema_version":"opsi.exposure_spec/v1",
        "project_id":e["OPSI_E2E_PROJECT_ID"],
        "environment_id":e["OPSI_E2E_ENVIRONMENT_ID"],
        "runtime_id":e["OPSI_E2E_RUNTIME_ID"],
        "service_key":e["OPSI_E2E_SERVICE_KEY"],
        "deployment_job_id":e["OPSI_E2E_EXPOSURE_DEPLOYMENT_ID"],
        "hostname":e["OPSI_E2E_PUBLIC_HOSTNAME"].strip().lower().rstrip("."),
        "path":"/",
        "service_port":int(e["OPSI_E2E_CONTAINER_PORT"]),
        "tls":{"mode":"disabled"},
        "spec_hash":"",
    }
    canonical = json.dumps(exposure, separators=(",", ":")).encode()
    exposure["spec_hash"] = hashlib.sha256(canonical).hexdigest()
    data = {
        "schema_version":"opsi.exposure_mutation/v1",
        "base_deployment_job_id":e["OPSI_E2E_EXPOSURE_BASE_DEPLOYMENT_ID"],
        "exposure":exposure,
    }
    if e.get("OPSI_E2E_EXPOSURE_STATE_HASH"):
        data["expected_state_hash"] = e["OPSI_E2E_EXPOSURE_STATE_HASH"]
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
    if deployment_wait_decision "$body" "$expect"; then
      return 0
    elif [ "$?" -eq 2 ]; then
      fail "deployment $deploy_id reached $value while waiting for $expect"
    fi
    now="$(date +%s)"
    [ $((now - start)) -lt "$POLL_SECONDS" ] || fail "timeout waiting for deployment $deploy_id=$expect, last=$value"
    sleep 10
  done
}

deployment_wait_decision() {
  local body="$1" expect="$2" value
  value="$(printf '%s' "$body" | json_get status 2>/dev/null || true)"
  [ "$value" = "$expect" ] && return 0
  case "$value" in
    failed|rollback_failed|succeeded|rolled_back|cancelled) return 2 ;;
    *) return 1 ;;
  esac
}

validate_healthy_deployment() {
  python3 -c "$(cat <<'PY'
import json, re, sys

d = json.load(sys.stdin)
is_hash = lambda value: isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value)
digest = d.get("desired_digest", "")
terminal = d.get("terminal_result") or {}
intent = d.get("rollout_intent") or {}
desired = intent.get("desired") or {}
target = desired.get("target") or {}
resources = terminal.get("resources") or []
if d.get("mode") != "rollout" or d.get("status") != "succeeded" or d.get("rollout_state") != "succeeded":
    raise SystemExit("healthy A is not a succeeded rollout")
if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest) or d.get("current_digest") != digest:
    raise SystemExit("healthy A digest state is inconsistent")
if terminal.get("rollout_state") != "succeeded" or terminal.get("desired_digest") != digest or terminal.get("current_digest") != digest:
    raise SystemExit("healthy A terminal result is inconsistent")
if not d.get("known_good_id") or not is_hash(d.get("known_good_hash")) or not is_hash(d.get("readiness_evidence_hash")):
    raise SystemExit("healthy A known-good/readiness evidence is incomplete")
if terminal.get("known_good_id") != d.get("known_good_id") or terminal.get("known_good_hash") != d.get("known_good_hash") or terminal.get("readiness_evidence_hash") != d.get("readiness_evidence_hash"):
    raise SystemExit("healthy A terminal evidence drifted")
if desired.get("deployment_job_id") != d.get("id") or (desired.get("image") or {}).get("digest") != digest:
    raise SystemExit("healthy A intent is not bound to the job/digest")
for key in ("runtime_id", "node_id", "agent_id", "service_id"):
    if not d.get(key):
        raise SystemExit("healthy A identity is incomplete: " + key)
if target.get("runtime_id") != d.get("runtime_id") or target.get("node_id") != d.get("node_id") or target.get("agent_id") != d.get("agent_id"):
    raise SystemExit("healthy A intent target drifted")
if not resources:
    raise SystemExit("healthy A resource identities are missing")
for resource in resources:
    if not all(resource.get(key) for key in ("kind", "name", "uid", "resource_version")) or not is_hash(resource.get("functional_hash")):
        raise SystemExit("healthy A resource identity is incomplete")
deployments = [resource for resource in resources if resource.get("kind") == "Deployment"]
services = [resource for resource in resources if resource.get("kind") == "Service"]
if len(deployments) != 1 or len(services) != 1 or deployments[0].get("namespace") != services[0].get("namespace") or deployments[0].get("name") != services[0].get("name"):
    raise SystemExit("healthy A Deployment/Service identities are inconsistent")
if not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", deployments[0]["namespace"]) or not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", deployments[0]["name"]):
    raise SystemExit("healthy A Kubernetes identity is unsafe")
for value in (digest, d["known_good_id"], d["known_good_hash"], d["readiness_evidence_hash"], d["service_id"], d["runtime_id"], d["node_id"], d["agent_id"], deployments[0]["namespace"], deployments[0]["name"]):
    print(value)
PY
)"
}

validate_rolled_back_deployment() {
  local digest_a="$1" known_id="$2" known_hash="$3" namespace="$4" deployment_name="$5"
  python3 -c "$(cat <<'PY'
import json, re, sys

digest_a, known_id, known_hash, namespace, deployment_name = sys.argv[1:6]
d = json.load(sys.stdin)
is_hash = lambda value: isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value)
terminal = d.get("terminal_result") or {}
digest_b = d.get("desired_digest", "")
if d.get("mode") != "rollout" or d.get("status") != "rolled_back" or d.get("rollout_state") != "rolled_back":
    raise SystemExit("broken B is not rolled_back")
if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest_b) or digest_b == digest_a:
    raise SystemExit("broken B desired digest is invalid")
if d.get("current_digest") != digest_a or d.get("previous_digest") != digest_a:
    raise SystemExit("broken B did not restore exact digest A")
if d.get("known_good_id") != known_id or d.get("known_good_hash") != known_hash:
    raise SystemExit("broken B replaced known-good A")
if terminal.get("rollout_state") != "rolled_back" or terminal.get("desired_digest") != digest_b or terminal.get("current_digest") != digest_a or terminal.get("previous_digest") != digest_a:
    raise SystemExit("broken B terminal digest state drifted")
if terminal.get("known_good_id") != known_id or terminal.get("known_good_hash") != known_hash:
    raise SystemExit("broken B terminal known-good drifted")
if not is_hash(d.get("readiness_evidence_hash")) or terminal.get("readiness_evidence_hash") != d.get("readiness_evidence_hash"):
    raise SystemExit("broken B readiness evidence is incomplete")
resources = terminal.get("resources") or []
if not resources:
    raise SystemExit("broken B resource identities are missing")
for resource in resources:
    if not all(resource.get(key) for key in ("kind", "name", "uid", "resource_version")) or not is_hash(resource.get("functional_hash")):
        raise SystemExit("broken B resource identity is incomplete")
if not any(resource.get("kind") == "Deployment" and resource.get("namespace") == namespace and resource.get("name") == deployment_name for resource in resources):
    raise SystemExit("broken B did not report the restored Deployment identity")
print(digest_b)
PY
)" "$digest_a" "$known_id" "$known_hash" "$namespace" "$deployment_name"
}

validate_exposure_preview() {
  local base_id="$1" expected_host="$2" expected_env="$3" expected_runtime="$4" expected_service="$5" expected_port="$6"
  python3 - "$base_id" "$expected_host" "$expected_env" "$expected_runtime" "$expected_service" "$expected_port" 3<&0 <<'PY'
import json, os, re, sys
base_id, host, env, runtime, service, port = sys.argv[1:]
d = json.load(os.fdopen(3))
desired = d.get("desired") or {}
if d.get("schema_version") != "opsi.exposure_preview/v1" or d.get("base_deployment_job_id") != base_id:
    raise SystemExit("exposure preview identity is invalid")
if d.get("eligible") is not True or d.get("decision_code") != "EXPOSURE_READY":
    raise SystemExit("exposure preview is not eligible")
if not re.fullmatch(r"[0-9a-f]{64}", d.get("state_hash", "")):
    raise SystemExit("exposure preview state_hash is invalid")
if desired.get("schema_version") != "opsi.exposure_spec/v1" or desired.get("project_id") != os.environ["OPSI_E2E_PROJECT_ID"]:
    raise SystemExit("exposure preview project/spec identity is invalid")
if (desired.get("hostname"), desired.get("environment_id"), desired.get("runtime_id"), desired.get("service_key"), str(desired.get("service_port"))) != (host, env, runtime, service, port):
    raise SystemExit("exposure preview target drifted")
if desired.get("path") != "/" or (desired.get("tls") or {}).get("mode") != "disabled":
    raise SystemExit("exposure preview route/TLS is invalid")
if desired.get("deployment_job_id") != os.environ.get("OPSI_E2E_EXPOSURE_DEPLOYMENT_ID", desired.get("deployment_job_id")) or not desired.get("deployment_job_id", "").startswith("dep-") or not re.fullmatch(r"[0-9a-f]{64}", desired.get("spec_hash", "")):
    raise SystemExit("exposure preview desired identity/hash is invalid")
print(d["state_hash"])
PY
}

validate_exposure_rollout() {
  local base_id="$1" expected_host="$2" expected_env="$3" expected_runtime="$4" expected_service="$5" expected_port="$6" expected_namespace="$7" expected_service_name="$8"
  python3 - "$base_id" "$expected_host" "$expected_env" "$expected_runtime" "$expected_service" "$expected_port" "$expected_namespace" "$expected_service_name" 3<&0 <<'PY'
import json, os, re, sys
base_id, host, env, runtime, service, port, namespace, service_name = sys.argv[1:]
d = json.load(os.fdopen(3))
spec = d.get("exposure_spec") or {}
terminal = d.get("terminal_result") or {}
resources = terminal.get("resources") or []
if d.get("status") != "succeeded" or d.get("rollout_state") != "succeeded" or d.get("base_deployment_id") != base_id:
    raise SystemExit("exposure rollout did not succeed")
if (spec.get("hostname"), spec.get("path"), spec.get("environment_id"), spec.get("runtime_id"), spec.get("service_key"), str(spec.get("service_port"))) != (host, "/", env, runtime, service, port):
    raise SystemExit("exposure rollout spec drifted")
if spec.get("deployment_job_id") != os.environ.get("OPSI_E2E_EXPOSURE_DEPLOYMENT_ID", spec.get("deployment_job_id")) or (spec.get("tls") or {}).get("mode") != "disabled" or not re.fullmatch(r"[0-9a-f]{64}", spec.get("spec_hash", "")):
    raise SystemExit("exposure rollout spec hash/TLS is invalid")
by_kind = {item.get("kind"): item for item in resources}
if set(by_kind) != {"Deployment", "Service", "Ingress"}:
    raise SystemExit("exposure rollout must report Deployment, Service and Ingress")
for kind, item in by_kind.items():
    if item.get("namespace") != namespace or not item.get("name") or not item.get("uid") or not item.get("resource_version") or not re.fullmatch(r"[0-9a-f]{64}", item.get("functional_hash", "")):
        raise SystemExit("exposure resource identity is incomplete: " + kind)
if by_kind["Service"].get("name") != service_name or by_kind["Deployment"].get("name") != service_name:
    raise SystemExit("exposure workload resource names drifted")
print(namespace)
print(by_kind["Ingress"]["name"])
print(by_kind["Service"]["name"])
print(port)
PY
}

verify_ingress() {
  local namespace="$1" ingress_name="$2" service_name="$3" service_port="$4" expected_host="$5" expected_path="$6" raw
  raw="$(mktemp)"
  remote_k3s "sudo k3s kubectl -n $(printf '%q' "$namespace") get ingress $(printf '%q' "$ingress_name") -o json" > "$raw" || { rm -f "$raw"; fail "Traefik Ingress fetch failed"; }
  python3 - "$raw" "$namespace" "$ingress_name" "$service_name" "$service_port" "$expected_host" "$expected_path" <<'PY' || { rm -f "$raw"; fail "Traefik Ingress/backend validation failed"; }
import json, sys
path, namespace, ingress_name, service_name, service_port, expected_host, expected_path = sys.argv[1:]
d = json.load(open(path))
meta = d.get("metadata") or {}
spec = d.get("spec") or {}
if d.get("kind") != "Ingress" or meta.get("namespace") != namespace or meta.get("name") != ingress_name or spec.get("ingressClassName") != "traefik":
    raise SystemExit("Ingress identity/class mismatch")
if any(not key.startswith("opsi.dev/") for key in (meta.get("annotations") or {})):
    raise SystemExit("unsafe Ingress annotation present")
rules = spec.get("rules") or []
if len(rules) != 1 or rules[0].get("host") != expected_host:
    raise SystemExit("Ingress hostname mismatch")
paths = ((rules[0].get("http") or {}).get("paths") or [])
if len(paths) != 1:
    raise SystemExit("Ingress path count mismatch")
route = paths[0]
backend = ((route.get("backend") or {}).get("service") or {})
port = (backend.get("port") or {}).get("number")
if route.get("path") != expected_path or route.get("pathType") != "Prefix" or backend.get("name") != service_name or str(port) != service_port:
    raise SystemExit("Ingress route/backend mismatch")
print("ingress=verified")
PY
  rm -f "$raw"
}

public_endpoint_hash() {
  local label="$1" body status size digest
  body="$(mktemp)"
  status="$(curl --resolve "$PUBLIC_HOSTNAME:$PUBLIC_PORT:$TARGET_HOST" --connect-timeout 10 --max-time 30 --max-filesize 1048576 -sS -o "$body" -w '%{http_code}' "http://$PUBLIC_HOSTNAME:$PUBLIC_PORT/")" || { rm -f "$body"; fail "public endpoint request failed"; }
  size="$(wc -c < "$body" | tr -d ' ')"
  digest="$(sha256sum "$body" | awk '{print $1}')"
  [ "$status" = "200" ] || { rm -f "$body"; fail "public endpoint returned unexpected HTTP status: $status"; }
  printf 'status=%s size=%s sha256=%s\n' "$status" "$size" "$digest" > "$ARTIFACT_DIR/$label-public-response.txt"
  rm -f "$body"
  printf '%s' "$digest"
}

validate_restored_response_hash() {
  [[ "$1" =~ ^[0-9a-f]{64}$ && "$1" = "$2" ]]
}

validate_rollback_events() {
  python3 -c 'import json, sys
d = json.load(sys.stdin)
events = d.get("events", d if isinstance(d, list) else [])
steps = [event.get("step") for event in events]
position = -1
for expected in ("failed", "rolling_back", "rolled_back"):
    try:
        position = steps.index(expected, position + 1)
    except ValueError:
        raise SystemExit("rollback event sequence is incomplete")'
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
  local digest="${1:-}" label="${2:-k3s}"
  remote_k3s "sudo k3s kubectl -n $WORKLOAD_NAMESPACE rollout status deployment/$SERVICE_NAME --timeout=120s" | redact > "$ARTIFACT_DIR/$label-rollout.redacted.log" || fail "K3s rollout status failed"
  remote_k3s "sudo k3s kubectl -n $WORKLOAD_NAMESPACE get deploy,svc,pods,endpoints -l app.kubernetes.io/name=$SERVICE_NAME -o wide" | redact > "$ARTIFACT_DIR/$label-runtime.redacted.log" || fail "K3s runtime state failed"
  if [ -n "$digest" ]; then
    local raw
    raw="$(mktemp)"
    remote_k3s "sudo k3s kubectl -n $WORKLOAD_NAMESPACE get pods -l app.kubernetes.io/name=$SERVICE_NAME -o json" > "$raw" || { rm -f -- "$raw"; fail "K3s pod JSON fetch failed"; }
    redact < "$raw" > "$ARTIFACT_DIR/$label-pods.redacted.json"
    python3 - "$raw" "$digest" <<'PY' || { rm -f -- "$raw"; fail "K3s final application image did not match the expected digest"; }
import json, sys
path, digest = sys.argv[1:]
data = json.load(open(path))
pods = data.get("items") or []
if not pods:
    raise SystemExit("no workload pods")
for pod in pods:
    statuses = ((pod.get("status") or {}).get("containerStatuses") or [])
    apps = [item for item in statuses if item.get("name") == "app"]
    specs = [item for item in (((pod.get("spec") or {}).get("containers")) or []) if item.get("name") == "app"]
    if not apps or not specs or any(not item.get("ready") or digest not in item.get("imageID", "") for item in apps) or any(not item.get("image", "").endswith("@" + digest) for item in specs):
        raise SystemExit("application container is not ready on the expected digest")
PY
    rm -f -- "$raw"
  fi
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

select_fresh_incident() {
  local service_id="$1" minimum_created_at="$2"
  python3 -c 'import json, sys
service_id, minimum_created_at = sys.argv[1], int(sys.argv[2])
data = json.load(sys.stdin)
incidents = data.get("incidents", data if isinstance(data, list) else [])
fresh = []
for incident in incidents:
    try:
        created_at = int(incident.get("created_at_unix", 0))
    except (TypeError, ValueError):
        continue
    if incident.get("service_id") == service_id and created_at >= minimum_created_at:
        fresh.append((created_at, incident.get("incident_id", "")))
fresh.sort(reverse=True)
print(fresh[0][1] if fresh else "")' "$service_id" "$minimum_created_at"
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
  Delete only the Opsi-owned Deployment/Service in namespace $WORKLOAD_NAMESPACE labeled app.kubernetes.io/name=$SERVICE_NAME.
  Review the reset script with --dry-run before any separately authorized reset.
  Review $LOCAL_URL via local UI and revoke/remove E2E project resources created with idempotency prefix $RUN_ID.
EOF
}

run_e2e() {
  preflight
  LOCAL_SESSION="$(session_token)"
  [ -n "$LOCAL_SESSION" ] || fail "local session token missing"
  local f body id good_deploy_id bad_deploy_id exposure_deploy_id bad_deployment_started_at service_id incidents incident_id incident_detail resolve audit deployment_events deployment_record good_values bad_digest exposure_record exposure_values exposure_preview restored_hash
  local -a good_fields
  local -a exposure_fields
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
  deployment_record="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$good_deploy_id" - deployment-a-evidence 0)" || fail "healthy A deployment evidence fetch failed"
  good_values="$(printf '%s' "$deployment_record" | validate_healthy_deployment)" || fail "healthy A rollout evidence is incomplete"
  mapfile -t good_fields <<< "$good_values"
  [ "${#good_fields[@]}" -eq 10 ] || fail "healthy A rollout evidence field count is invalid"
  GOOD_DIGEST="${good_fields[0]}"
  GOOD_KNOWN_ID="${good_fields[1]}"
  GOOD_KNOWN_HASH="${good_fields[2]}"
  GOOD_READINESS_HASH="${good_fields[3]}"
  export OPSI_E2E_RUNTIME_ID="${good_fields[5]}"
  [ "$service_id" = "${good_fields[4]}" ] || fail "healthy A service identity drifted"
  WORKLOAD_NAMESPACE="${good_fields[8]}"
  SERVICE_NAME="${good_fields[9]}"
  export OPSI_E2E_SERVICE_ID="$service_id"
  log "step 3/11 healthy A rollout succeeded: service=$service_id deployment=$good_deploy_id digest=$GOOD_DIGEST known_good=$GOOD_KNOWN_ID readiness=$GOOD_READINESS_HASH runtime=${good_fields[5]} node=${good_fields[6]} agent=${good_fields[7]}"
  verify_runtime "$GOOD_DIGEST" healthy-a
  log "step 4/11 healthy A K3s readiness and exact image/imageID verified"

  export OPSI_E2E_EXPOSURE_BASE_DEPLOYMENT_ID="$good_deploy_id"
  exposure_deploy_id="dep-$(printf '%s' "$RUN_ID-$good_deploy_id" | sha256sum | awk '{print substr($1,1,24)}')"
  export OPSI_E2E_EXPOSURE_DEPLOYMENT_ID="$exposure_deploy_id"
  f="$(mktemp)"; write_json "$f" exposure
  exposure_preview="$(api_file POST "/api/local/projects/$PROJECT_ID/exposures/preview" "$f" exposure-preview 0)" || fail "exposure preview failed"
  rm -f "$f"
  export OPSI_E2E_EXPOSURE_STATE_HASH="$(printf '%s' "$exposure_preview" | validate_exposure_preview "$good_deploy_id" "$PUBLIC_HOSTNAME" "$ENVIRONMENT_ID" "$OPSI_E2E_RUNTIME_ID" "$SERVICE_KEY" "$CONTAINER_PORT")" || fail "exposure preview contract validation failed"
  f="$(mktemp)"; write_json "$f" exposure
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/exposures" "$f" exposure-apply 1)" || fail "exposure apply failed"
  rm -f "$f"
  exposure_deploy_id="$(printf '%s' "$body" | json_get id)" || fail "exposure apply response missing id"
  [ "$exposure_deploy_id" = "$OPSI_E2E_EXPOSURE_DEPLOYMENT_ID" ] || fail "exposure apply deployment identity drifted"
  wait_deployment_status "$exposure_deploy_id" succeeded
  exposure_record="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$exposure_deploy_id" - exposure-evidence 0)" || fail "exposure rollout evidence fetch failed"
  exposure_values="$(printf '%s' "$exposure_record" | validate_exposure_rollout "$good_deploy_id" "$PUBLIC_HOSTNAME" "$ENVIRONMENT_ID" "$OPSI_E2E_RUNTIME_ID" "$SERVICE_KEY" "$CONTAINER_PORT" "$WORKLOAD_NAMESPACE" "$SERVICE_NAME")" || fail "exposure rollout resource identities are incomplete"
  mapfile -t exposure_fields <<< "$exposure_values"
  [ "${#exposure_fields[@]}" -eq 4 ] || fail "exposure rollout identity field count is invalid"
  verify_ingress "${exposure_fields[0]}" "${exposure_fields[1]}" "${exposure_fields[2]}" "${exposure_fields[3]}" "$PUBLIC_HOSTNAME" "/"
  PUBLIC_A_HASH="$(public_endpoint_hash public-a)"
  log "step 5/14 exposure preview/apply, Traefik backend and direct public routing verified: job=$exposure_deploy_id ingress=${exposure_fields[1]} public_hash=$PUBLIC_A_HASH"

  f="$(mktemp)"; write_json "$f" secret
  api_file POST "/api/local/projects/$PROJECT_ID/secrets" "$f" secret-create 1 >/dev/null || fail "secret create failed"
  write_json "$f" second_factor
  api_file POST "/api/local/projects/$PROJECT_ID/secrets/$SECRET_NAME/rotate" "$f" secret-rotate 1 >/dev/null || fail "secret rotate failed"
  if api_file POST "/api/local/projects/$PROJECT_ID/secrets/$SECRET_NAME/reveal" "$f" secret-reveal 1 | grep -q "$APP_SECRET_VALUE"; then
    fail "secret value leaked into reveal output"
  fi
  rm -f "$f"
  log "step 6/14 secret create/rotate/reveal path ran via local Agent facade"

  api_file GET "/api/local/projects/$PROJECT_ID/telemetry/summary?service_id=$service_id" - telemetry-summary 0 >/dev/null || fail "telemetry summary failed"
  api_file GET "/api/local/projects/$PROJECT_ID/logs?service_id=$service_id&limit=50" - logs 0 >/dev/null || fail "logs failed"
  log "step 7/14 sanitized telemetry/logs fetched through local backend"

  f="$(mktemp)"; write_json "$f" bad_deployment
  bad_deployment_started_at="$(date +%s)"
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/deployments" "$f" bad-deploy-create 1)" || fail "bad immutable deployment create failed"
  rm -f "$f"
  bad_deploy_id="$(printf '%s' "$body" | json_get id)" || fail "bad deployment response missing id"
  wait_deployment_status "$bad_deploy_id" rolled_back
  deployment_record="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$bad_deploy_id" - deployment-b-evidence 0)" || fail "broken B deployment evidence fetch failed"
  bad_digest="$(printf '%s' "$deployment_record" | validate_rolled_back_deployment "$GOOD_DIGEST" "$GOOD_KNOWN_ID" "$GOOD_KNOWN_HASH" "$WORKLOAD_NAMESPACE" "$SERVICE_NAME")" || fail "broken B rollback evidence did not restore exact A"
  verify_runtime "$GOOD_DIGEST" restored-a
  verify_ingress "${exposure_fields[0]}" "${exposure_fields[1]}" "${exposure_fields[2]}" "${exposure_fields[3]}" "$PUBLIC_HOSTNAME" "/"
  restored_hash="$(public_endpoint_hash public-restored)"
  validate_restored_response_hash "$restored_hash" "$PUBLIC_A_HASH" || fail "public endpoint response hash changed after exact rollback"
  deployment_events="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$bad_deploy_id/events" - deployment-b-events 0)" || fail "broken B rollout events fetch failed"
  printf '%s' "$deployment_events" | validate_rollback_events || fail "broken B rollout event sequence is invalid"
  log "step 8/14 broken B rolled back and K3s restored healthy A: deployment=$bad_deploy_id desired_digest=$bad_digest restored_digest=$GOOD_DIGEST public_hash=$restored_hash"
  incidents="$(api_file GET "/api/local/projects/$PROJECT_ID/incidents?status=open&limit=10" - incidents 0)" || fail "incident list failed"
  incident_id="$(printf '%s' "$incidents" | select_fresh_incident "$service_id" "$bad_deployment_started_at")"
  [ -n "$incident_id" ] || fail "no fresh controlled incident found; E2E does not resolve incidents created before broken deployment B"
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
  log "step 9/14 factual incident list/detail/resolve lifecycle verified: incident=$incident_id"

  deployment_events="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$good_deploy_id/events" - deployment-events 0)" || fail "deployment events fetch failed"
  printf '%s' "$deployment_events" | grep -q '"step":"succeeded"' || fail "deployment success event missing"
  audit="$(api_file GET "/api/local/projects/$PROJECT_ID/audit" - audit 0)" || fail "audit fetch failed"
  printf '%s' "$audit" | grep -q 'IMMUTABLE_DEPLOYMENT_CREATED' || fail "immutable deployment audit event missing"
  deployment_record="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments/$good_deploy_id" - deployment-evidence 0)" || fail "canonical deployment evidence fetch failed"
  for evidence in "$good_deploy_id" "$BUILD_RECORD_ID" "$GOOD_DIGEST" "$GOOD_KNOWN_ID" "$GOOD_KNOWN_HASH" "$GOOD_READINESS_HASH" 'runtime_id' 'node_id' 'agent_id' 'resources' 'succeeded'; do
    printf '%s' "$deployment_record" | grep -q "$evidence" || fail "canonical deployment evidence missing $evidence"
  done
  log "step 10/14 BuildRecord rollout evidence verified: job=$good_deploy_id build_record=$BUILD_RECORD_ID"
  check_artifacts_clean || fail "redaction failed: artifact contains sensitive value"
  log "step 11/14 artifacts verified without sensitive payloads"
  manual_cleanup
  log "step 12/14 cleanup instructions written"
  verify_runtime "$GOOD_DIGEST" final-a
  log "step 13/14 final A readiness and matching public response hash verified"
  log "PASS B1: healthy A -> exposure -> broken B -> restored A; PUBLIC_DNS_TLS_PENDING"
}

self_test() {
  local key_public fixture match original_key forbidden pem_marker expected request incident_fixture
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
  cat > "$SELF_TEST_DIR/ssh-keyscan" <<'SH'
#!/usr/bin/env sh
printf '%s\n' "${OPSI_SELFTEST_SCAN_CALLED:?}" > "${OPSI_SELFTEST_SCAN_MARKER:?}"
case "${OPSI_SELFTEST_SCAN_MODE:-correct}" in
  zero) exit 0 ;;
  duplicate) printf '%s\n%s\n' "$OPSI_SELFTEST_HOST_LINE" "$OPSI_SELFTEST_HOST_LINE" ;;
  *) printf '%s\n' "$OPSI_SELFTEST_HOST_LINE" ;;
esac
SH
  chmod 700 "$SELF_TEST_DIR/ssh-keyscan"
  OPSI_SELFTEST_SCAN_MARKER="$SELF_TEST_DIR/scan-called" OPSI_SELFTEST_SCAN_CALLED=local OPSI_SELFTEST_HOST_LINE="fixture $key_public" PATH="$SELF_TEST_DIR:$PATH" TARGET_HOST=fixture TARGET_SSH_PORT=22 HOST_KEY_SHA256="$expected" pin_host_identity || fail "self-test host-key pin rejected correct fingerprint"
  [ -s "$SELF_TEST_DIR/scan-called" ] || fail "self-test ssh-keyscan stub was not exercised"
  rm -f -- "$KNOWN_HOSTS_FILE"
  OPSI_SELFTEST_SCAN_MODE=correct HOST_KEY_SHA256="SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" PATH="$SELF_TEST_DIR:$PATH" TARGET_HOST=fixture TARGET_SSH_PORT=22 pin_host_identity && fail "self-test host-key pin accepted wrong fingerprint"
  rm -f -- "$KNOWN_HOSTS_FILE"
  OPSI_SELFTEST_SCAN_MODE=zero HOST_KEY_SHA256="$expected" PATH="$SELF_TEST_DIR:$PATH" TARGET_HOST=fixture TARGET_SSH_PORT=22 pin_host_identity && fail "self-test host-key pin accepted zero matches"
  rm -f -- "$KNOWN_HOSTS_FILE"
  OPSI_SELFTEST_SCAN_MODE=duplicate HOST_KEY_SHA256="$expected" PATH="$SELF_TEST_DIR:$PATH" TARGET_HOST=fixture TARGET_SSH_PORT=22 pin_host_identity && fail "self-test host-key pin accepted duplicate matches"
  rm -f -- "$KNOWN_HOSTS_FILE"
  hash_a="$(printf '%064d' 0 | tr 0 a)"
  digest_a="sha256:$hash_a"
  python3 - "$SELF_TEST_DIR" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
hash_a = "a" * 64
digest_a = "sha256:" + hash_a
digest_b = "sha256:" + "b" * 64
resource = {"kind":"Deployment","namespace":"opsi","name":"api","uid":"uid-api","resource_version":"1","functional_hash":"c" * 64}
service = {"kind":"Service","namespace":"opsi","name":"api","uid":"uid-service","resource_version":"1","functional_hash":"b" * 64}
def base(status, digest, current="", previous="", known_id="", known_hash="", failure=""):
    intent = {"desired":{"deployment_job_id":"dep-fixture","image":{"digest":digest},"target":{"runtime_id":"runtime-1","node_id":"node-1","agent_id":"agent-1"}}}
    terminal = {"rollout_state":status,"desired_digest":digest,"current_digest":current,"previous_digest":previous,"known_good_id":known_id,"known_good_hash":known_hash,"readiness_evidence_hash":"d" * 64,"resources":[resource,service]}
    return {"id":"dep-fixture","mode":"rollout","status":status,"rollout_state":status,"desired_digest":digest,"current_digest":current,"previous_digest":previous,"known_good_id":known_id,"known_good_hash":known_hash,"readiness_evidence_hash":"d" * 64,"service_id":"service-1","runtime_id":"runtime-1","node_id":"node-1","agent_id":"agent-1","rollout_intent":intent,"terminal_result":terminal,"failure_code":failure}
healthy = base("succeeded", digest_a, digest_a, known_id="known-a", known_hash=hash_a)
rolled = base("rolled_back", digest_b, digest_a, digest_a, "known-a", hash_a, "READINESS_FAILED")
fixtures = {"healthy-a.json":healthy,"rolled-back-b.json":rolled,"failed.json":{"status":"failed"},"rollback-failed.json":{"status":"rollback_failed"},"cancelled.json":{"status":"cancelled"}}
for name, value in fixtures.items():
    (root / name).write_text(json.dumps(value))
PY
  printf '%s' "$(<"$SELF_TEST_DIR/healthy-a.json")" | validate_healthy_deployment >/dev/null || fail "self-test healthy A fixture was rejected"
  printf '%s' "$(<"$SELF_TEST_DIR/rolled-back-b.json")" | validate_rolled_back_deployment "$digest_a" known-a "$hash_a" opsi api >/dev/null || fail "self-test rolled-back B fixture was rejected"
  python3 - "$SELF_TEST_DIR" <<'PY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
spec = {"schema_version":"opsi.exposure_spec/v1","project_id":"self-project","environment_id":"env-1","runtime_id":"runtime-1","service_key":"api","deployment_job_id":"dep-exposure-self","hostname":"self.example","path":"/","service_port":8080,"tls":{"mode":"disabled"},"spec_hash":""}
spec["spec_hash"] = hashlib.sha256(json.dumps(spec, separators=(",", ":")).encode()).hexdigest()
preview = {"schema_version":"opsi.exposure_preview/v1","base_deployment_job_id":"dep-fixture","desired":spec,"state_hash":"b" * 64,"eligible":True,"decision_code":"EXPOSURE_READY"}
resources = [{"kind":"Deployment","namespace":"opsi","name":"api","uid":"uid-deploy","resource_version":"1","functional_hash":"a" * 64},{"kind":"Service","namespace":"opsi","name":"api","uid":"uid-service","resource_version":"1","functional_hash":"b" * 64},{"kind":"Ingress","namespace":"opsi","name":"opsi-ingress-api-runtime-1","uid":"uid-ingress","resource_version":"1","functional_hash":"c" * 64}]
rollout = {"status":"succeeded","rollout_state":"succeeded","base_deployment_id":"dep-fixture","exposure_spec":spec,"terminal_result":{"resources":resources}}
(root / "exposure-preview.json").write_text(json.dumps(preview))
(root / "exposure-rollout.json").write_text(json.dumps(rollout))
(root / "exposure-no-ingress.json").write_text(json.dumps({**rollout,"terminal_result":{"resources":resources[:2]}}))
PY
  printf '%s' "$(<"$SELF_TEST_DIR/exposure-preview.json")" | OPSI_E2E_PROJECT_ID=self-project validate_exposure_preview dep-fixture self.example env-1 runtime-1 api 8080 >/dev/null || fail "self-test valid exposure preview was rejected"
  for mutation in hostname path service_port; do
    python3 - "$SELF_TEST_DIR/exposure-preview.json" "$mutation" <<'PY'
import json, sys
path, mutation = sys.argv[1:]
data = json.load(open(path))
field = {"hostname":"hostname","path":"path","service_port":"service_port"}[mutation]
data["desired"][field] = {"hostname":"","path":"/bad","service_port":9090}[mutation]
json.dump(data, open(path + ".bad", "w"))
PY
    if printf '%s' "$(<"$SELF_TEST_DIR/exposure-preview.json.bad")" | OPSI_E2E_PROJECT_ID=self-project validate_exposure_preview dep-fixture self.example env-1 runtime-1 api 8080 >/dev/null 2>&1; then
      fail "self-test accepted malformed exposure $mutation"
    fi
  done
  printf '%s' "$(<"$SELF_TEST_DIR/exposure-rollout.json")" | validate_exposure_rollout dep-fixture self.example env-1 runtime-1 api 8080 opsi api >/dev/null || fail "self-test valid exposure rollout was rejected"
  if printf '%s' "$(<"$SELF_TEST_DIR/exposure-no-ingress.json")" | validate_exposure_rollout dep-fixture self.example env-1 runtime-1 api 8080 opsi api >/dev/null 2>&1; then
    fail "self-test accepted exposure rollout with missing Ingress identity"
  fi
  response_hash="$(printf '%064d' 0 | tr 0 d)"
  validate_restored_response_hash "$response_hash" "$response_hash" || fail "self-test accepted no restored response hash"
  if validate_restored_response_hash "$response_hash" "$(printf '%064d' 0 | tr 0 e)"; then
    fail "self-test accepted a changed restored response hash"
  fi
  export OPSI_E2E_PROJECT_ID=self-project OPSI_E2E_ENVIRONMENT_ID=env-1 OPSI_E2E_RUNTIME_ID=runtime-1 OPSI_E2E_SERVICE_KEY=api OPSI_E2E_CONTAINER_PORT=8080 OPSI_E2E_PUBLIC_HOSTNAME=self.example OPSI_E2E_EXPOSURE_BASE_DEPLOYMENT_ID=dep-fixture OPSI_E2E_EXPOSURE_DEPLOYMENT_ID=dep-exposure-self OPSI_E2E_EXPOSURE_STATE_HASH="$(printf '%064d' 0 | tr 0 b)"
  request="$SELF_TEST_DIR/exposure-request.json"
  write_json "$request" exposure || fail "self-test valid exposure apply request generation failed"
  python3 - "$request" <<'PY' || fail "self-test exposure apply request was malformed"
import json, re, sys
d = json.load(open(sys.argv[1]))
if d.get("schema_version") != "opsi.exposure_mutation/v1" or not re.fullmatch(r"[0-9a-f]{64}", d.get("expected_state_hash", "")) or d["exposure"].get("tls", {}).get("mode") != "disabled":
    raise SystemExit(1)
PY
  for field in current_digest previous_digest known_good_id known_good_hash; do
    python3 - "$SELF_TEST_DIR/rolled-back-b.json" "$field" "$digest_a" "$hash_a" <<'PY' || fail "self-test rollback fixture mutation setup failed"
import json, sys
path, field, digest_a, hash_a = sys.argv[1:]
data = json.load(open(path))
data.update({"current_digest": digest_a, "previous_digest": digest_a, "known_good_id": "known-a", "known_good_hash": hash_a})
data[field] = {"current_digest":"sha256:" + "e" * 64, "previous_digest":"sha256:" + "f" * 64, "known_good_id":"wrong-known-good", "known_good_hash":"e" * 64}[field]
json.dump(data, open(path, "w"))
PY
    if printf '%s' "$(<"$SELF_TEST_DIR/rolled-back-b.json")" | validate_rolled_back_deployment "$digest_a" known-a "$hash_a" opsi api >/dev/null 2>&1; then
      fail "self-test accepted rolled-back record with wrong $field"
    fi
  done
  deployment_wait_decision "$(<"$SELF_TEST_DIR/healthy-a.json")" succeeded || fail "self-test did not accept succeeded A"
  deployment_wait_decision "$(<"$SELF_TEST_DIR/rolled-back-b.json")" rolled_back || fail "self-test did not accept rolled_back B"
  printf '%s' '{"events":[{"step":"failed"},{"step":"rolling_back"},{"step":"rolled_back"}]}' | validate_rollback_events || fail "self-test rollback event sequence was rejected"
  if printf '%s' '{"events":[{"step":"failed"},{"step":"rolled_back"}]}' | validate_rollback_events >/dev/null 2>&1; then
    fail "self-test accepted incomplete rollback event sequence"
  fi
  for fixture in healthy-a failed rollback-failed cancelled; do
    if deployment_wait_decision "$(<"$SELF_TEST_DIR/$fixture.json")" rolled_back; then
      fail "self-test accepted terminal $fixture while waiting for rollback"
    else
      decision=$?
    fi
    [ "$decision" -eq 2 ] || fail "self-test did not fail closed for terminal $fixture"
  done
  pem_marker='-----BEGIN OPENSSH '"PRIVATE KEY-----"
  printf '%s\n' "$pem_marker" > "$ARTIFACT_DIR/pem-leak.txt"
  if check_artifacts_clean >/dev/null 2>&1; then fail "self-test artifact validation accepted a PEM marker"; fi
  rm -f -- "$ARTIFACT_DIR/pem-leak.txt"
  OPSI_E2E_SSH_KEY_PATH="$original_key"
  printf '{"incident":{"incident_id":"inc-self-test","status":"open"}}' | verify_incident_detail inc-self-test || fail "self-test factual incident detail failed"
  if printf '{"incident":{"incident_id":"inc-self-test","action_%s":"legacy"}}' hash | verify_incident_detail inc-self-test >/dev/null 2>&1; then
    fail "self-test legacy incident field was accepted"
  fi
  incident_fixture='{"incidents":[{"incident_id":"inc-old","service_id":"service-1","created_at_unix":99},{"incident_id":"inc-fresh","service_id":"service-1","created_at_unix":101},{"incident_id":"inc-other","service_id":"service-2","created_at_unix":102}]}'
  [ "$(printf '%s' "$incident_fixture" | select_fresh_incident service-1 100)" = "inc-fresh" ] || fail "self-test did not select the fresh controlled incident"
  if printf '%s' '{"incidents":[{"incident_id":"inc-old","service_id":"service-1","created_at_unix":99}]}' | select_fresh_incident service-1 100 | grep -q .; then
    fail "self-test accepted an incident created before broken deployment B"
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
