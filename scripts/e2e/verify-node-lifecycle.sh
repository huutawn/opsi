#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-run}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="${OPSI_E2E_RUN_ID:-node-life-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
ARTIFACT_DIR="${OPSI_E2E_ARTIFACT_DIR:-$ROOT/.tmp/e2e-node-lifecycle/$RUN_ID}"
LOCAL_URL="${OPSI_E2E_LOCAL_URL:-http://127.0.0.1:9780}"
PROJECT_ID="${OPSI_E2E_PROJECT_ID:-}"
TARGET_NODE_ID="${OPSI_E2E_NODE_LIFECYCLE_TARGET_NODE_ID:-}"
TARGET_NODE_NAME="${OPSI_E2E_NODE_LIFECYCLE_TARGET_NODE_NAME:-}"
FAILURE_NODE_ID="${OPSI_E2E_NODE_LIFECYCLE_FAILURE_NODE_ID:-}"
REMOVE_NODE_ID="${OPSI_E2E_REMOVE_NODE_ID:-}"
REMOVE_NODE_NAME="${OPSI_E2E_REMOVE_NODE_NAME:-}"
ALLOW_REMOVE="${OPSI_E2E_ALLOW_NODE_REMOVE:-0}"
REMOVE_CONFIRM="${OPSI_E2E_REMOVE_CONFIRM:-}"
USER_ID="${OPSI_E2E_USER_ID:-e2e-owner@example.com}"
POLL_SECONDS="${OPSI_E2E_POLL_SECONDS:-600}"
KUBECTL="${OPSI_E2E_KUBECTL:-kubectl}"

usage() {
  cat <<'EOF'
Usage:
  make verify-e2e-node-lifecycle-preflight
  make verify-e2e-node-lifecycle
  make verify-e2e-node-lifecycle-selfcheck

Required env for default drain proof:
  OPSI_E2E_PROJECT_ID
  OPSI_E2E_NODE_LIFECYCLE_TARGET_NODE_ID
  OPSI_E2E_NODE_LIFECYCLE_TARGET_NODE_NAME

Optional truthful K3s failure proof:
  OPSI_E2E_NODE_LIFECYCLE_FAILURE_NODE_ID

Destructive remove proof:
  OPSI_E2E_ALLOW_NODE_REMOVE=1
  OPSI_E2E_REMOVE_NODE_ID
  OPSI_E2E_REMOVE_NODE_NAME
  OPSI_E2E_REMOVE_CONFIRM="REMOVE <node-name>"

The script drives Browser-equivalent /api/local requests. It uses kubectl only
for independent K3s verification, never for the lifecycle operation itself.
EOF
}

log() {
  mkdir -p "$ARTIFACT_DIR"
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$ARTIFACT_DIR/evidence.redacted.log"
}

fail() {
  log "FAIL: $*"
  exit 1
}

redact() {
  python3 -c 'import re, sys
data = sys.stdin.read()
patterns = [
    r"(?i)(authorization\s*[:=]\s*bearer\s+)[^\s\",}]+",
    r"(?i)((password|token|pat|private_key|kubeconfig|secret|otp|totp)\s*[\"=:]+\s*)(\"[^\"]*\"|[^,\s}]+)",
]
for pat in patterns:
    data = re.sub(pat, lambda m: m.group(1) + "[REDACTED]", data)
sys.stdout.write(data)'
}

need_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "missing tool: $1"
}

need_env() {
  [ -n "${!1:-}" ] || fail "missing env: $1"
}

json_get() {
  python3 -c 'import json, sys
data = json.load(sys.stdin)
for p in sys.argv[1].split("."):
    if not p:
        continue
    data = data[int(p)] if isinstance(data, list) else data[p]
print(data)' "$1"
}

json_find_node_field() {
  python3 -c 'import json, sys
data = json.load(sys.stdin)
node = data.get("node", data) if isinstance(data, dict) else {}
print(node.get(sys.argv[1], ""))' "$1"
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

api_expect_fail() {
  local method="$1" path="$2" body_file="$3" label="$4" code="$5"
  local out status headers=(-H "content-type: application/json" -H "X-Request-ID: $RUN_ID-$label" -H "Idempotency-Key: $RUN_ID-$label" -H "X-Local-Session: $LOCAL_SESSION")
  out="$(mktemp)"
  status="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "${headers[@]}" --data-binary "@$body_file" "$LOCAL_URL$path")" || status="000"
  redact < "$out" > "$ARTIFACT_DIR/$label.redacted.json"
  if [ "${status#2}" != "$status" ]; then
    rm -f "$out"
    fail "$label unexpectedly succeeded"
  fi
  grep -q "\"$code\"" "$ARTIFACT_DIR/$label.redacted.json" || fail "$label did not return $code"
  rm -f "$out"
}

body_file() {
  local file="$1" confirm="${2:-false}"
  python3 - "$file" "$USER_ID" "$confirm" <<'PY'
import json, sys
confirm = sys.argv[3].lower() == "true"
open(sys.argv[1], "w").write(json.dumps({"requested_by": sys.argv[2], "confirm_remove": confirm}))
PY
}

node_field() {
  local node_id="$1" field="$2"
  api_file GET "/api/local/projects/$PROJECT_ID/nodes/$node_id" - "node-$node_id-$field" 0 | json_find_node_field "$field"
}

wait_node_status() {
  local node_id="$1" expect="$2" label="$3" start now got
  start="$(date +%s)"
  while :; do
    got="$(node_field "$node_id" status || true)"
    [ "$got" = "$expect" ] && return 0
    now="$(date +%s)"
    [ $((now - start)) -lt "$POLL_SECONDS" ] || fail "timeout waiting node $node_id status=$expect, last=$got"
    sleep 5
  done
}

wait_audit_contains() {
  local job_id="$1" action="$2" label="$3" start now body
  start="$(date +%s)"
  while :; do
    body="$(api_file GET "/api/local/projects/$PROJECT_ID/audit" - "$label" 0 || true)"
    if printf '%s' "$body" | grep -q "$job_id" && printf '%s' "$body" | grep -q "$action"; then
      return 0
    fi
    now="$(date +%s)"
    [ $((now - start)) -lt "$POLL_SECONDS" ] || fail "timeout waiting audit $action for $job_id"
    sleep 5
  done
}

kubectl_node_json() {
  "$KUBECTL" get node "$1" -o json
}

kubectl_unschedulable() {
  kubectl_node_json "$1" | python3 -c 'import json, sys; print(str(json.load(sys.stdin).get("spec", {}).get("unschedulable", False)).lower())'
}

preflight() {
  mkdir -p "$ARTIFACT_DIR"
  log "preflight: artifact_dir=$ARTIFACT_DIR"
  for t in bash curl python3 grep "$KUBECTL"; do need_tool "$t"; done
  need_env OPSI_E2E_PROJECT_ID
  need_env OPSI_E2E_NODE_LIFECYCLE_TARGET_NODE_ID
  need_env OPSI_E2E_NODE_LIFECYCLE_TARGET_NODE_NAME
  curl -fsS "$LOCAL_URL/health" >/dev/null || fail "local backend unavailable at OPSI_E2E_LOCAL_URL"
  curl -fsS "$LOCAL_URL/api/local/status" | redact > "$ARTIFACT_DIR/agent-status.redacted.json" || fail "Agent status unavailable through local backend"
  kubectl_node_json "$TARGET_NODE_NAME" | redact > "$ARTIFACT_DIR/k3s-target-node-before.redacted.json" || fail "target node unavailable through real kubectl"
  log "preflight: ok"
}

request_lifecycle() {
  local action="$1" node_id="$2" label="$3" confirm="${4:-false}" force="${5:-}"
  local f path body status
  f="$(mktemp)"
  body_file "$f" "$confirm"
  path="/api/local/projects/$PROJECT_ID/nodes/$node_id/$action$force"
  body="$(api_file POST "$path" "$f" "$label" 1)" || { rm -f "$f"; return 1; }
  rm -f "$f"
  status="$(printf '%s' "$body" | json_get status)"
  if [ "$status" = "completed" ]; then
    fail "$action completed immediately from metadata"
  fi
  printf '%s' "$body"
}

prove_drain() {
  local body job_id action target status unsched
  body="$(request_lifecycle drain "$TARGET_NODE_ID" drain-request false "")" || fail "drain request failed"
  job_id="$(printf '%s' "$body" | json_get id)"
  action="$(printf '%s' "$body" | json_get action)"
  target="$(printf '%s' "$body" | json_get target_node_id)"
  status="$(printf '%s' "$body" | json_get status)"
  [ "$action" = "drain" ] || fail "drain job action mismatch: $action"
  [ "$target" = "$TARGET_NODE_ID" ] || fail "drain target mismatch: $target"
  [ "$status" != "completed" ] || fail "Cloud marked drain completed before Agent result"
  log "drain: Cloud accepted typed job $job_id without terminal success"
  wait_node_status "$TARGET_NODE_ID" draining drain-status
  unsched="$(kubectl_unschedulable "$TARGET_NODE_NAME")"
  [ "$unsched" = "true" ] || fail "K3s node was not cordoned after drain"
  wait_audit_contains "$job_id" NODE_LIFECYCLE_REQUESTED audit-drain-requested
  wait_audit_contains "$job_id" NODE_LIFECYCLE_COMPLETED audit-drain-completed
  log "drain: Agent-verified K3s drain proved for node $TARGET_NODE_NAME"
}

prove_failure_path() {
  local f bad_body bad_job
  f="$(mktemp)"
  body_file "$f" false
  api_expect_fail POST "/api/local/projects/$PROJECT_ID/nodes/$TARGET_NODE_ID/remove?force=true" "$f" remove-without-intent REMOVE_INTENT_REQUIRED
  rm -f "$f"
  if [ -n "$FAILURE_NODE_ID" ]; then
    bad_body="$(request_lifecycle drain "$FAILURE_NODE_ID" drain-k3s-failure false "")" || fail "failure target was rejected before Agent/K3s execution"
    bad_job="$(printf '%s' "$bad_body" | json_get id)"
    wait_audit_contains "$bad_job" NODE_LIFECYCLE_FAILED audit-k3s-failure
  fi
  log "failure: remove intent gate failed closed; optional K3s failure target checked when provided"
}

prove_remove() {
  local body job_id status
  if [ "$ALLOW_REMOVE" != "1" ]; then
    log "remove: skipped; set OPSI_E2E_ALLOW_NODE_REMOVE=1 for destructive proof"
    return 0
  fi
  need_env OPSI_E2E_REMOVE_NODE_ID
  need_env OPSI_E2E_REMOVE_NODE_NAME
  [ "$REMOVE_CONFIRM" = "REMOVE $REMOVE_NODE_NAME" ] || fail "missing destructive confirmation: OPSI_E2E_REMOVE_CONFIRM=\"REMOVE $REMOVE_NODE_NAME\""
  kubectl_node_json "$REMOVE_NODE_NAME" | redact > "$ARTIFACT_DIR/k3s-remove-node-before.redacted.json" || fail "remove target missing from K3s"
  body="$(request_lifecycle remove "$REMOVE_NODE_ID" remove-request true "?force=true")" || fail "remove request failed"
  job_id="$(printf '%s' "$body" | json_get id)"
  wait_node_status "$REMOVE_NODE_ID" removed remove-status
  if kubectl_node_json "$REMOVE_NODE_NAME" >/tmp/opsi-node-remove-check.json 2>/dev/null; then
    redact < /tmp/opsi-node-remove-check.json > "$ARTIFACT_DIR/k3s-remove-node-still-present.redacted.json"
    fail "K3s remove did not delete node $REMOVE_NODE_NAME"
  fi
  wait_audit_contains "$job_id" NODE_LIFECYCLE_COMPLETED audit-remove-completed
  status="$(node_field "$REMOVE_NODE_ID" status)"
  [ "$status" = "removed" ] || fail "Cloud/local status not removed after Agent result: $status"
  log "remove: Agent-verified K3s delete-node proved for node $REMOVE_NODE_NAME"
}

check_artifacts_clean() {
  if grep -RIE "(kubeconfig|private key|BEGIN .*PRIVATE|authorization: bearer|password=|token=|pat=|otp_code|totp_code)" "$ARTIFACT_DIR" >/tmp/opsi-node-life-grep.txt 2>/dev/null; then
    cat /tmp/opsi-node-life-grep.txt
    fail "artifact redaction check failed"
  fi
}

run_e2e() {
  preflight
  LOCAL_SESSION="$(session_token)"
  [ -n "$LOCAL_SESSION" ] || fail "local session token missing"
  prove_drain
  prove_failure_path
  prove_remove
  check_artifacts_clean
  log "PASS: real K3s node lifecycle E2E complete"
}

self_test() {
  mkdir -p "$ARTIFACT_DIR"
  if env -i PATH="$PATH" OPSI_E2E_ARTIFACT_DIR="$ARTIFACT_DIR/missing" "$0" --preflight >/tmp/opsi-node-life-preflight.out 2>&1; then
    fail "self-test missing prereq did not fail"
  fi
  grep -Eq "missing (env: OPSI_E2E_PROJECT_ID|tool:)" /tmp/opsi-node-life-preflight.out || fail "self-test preflight message unclear"
  grep -q "OPSI_E2E_ALLOW_NODE_REMOVE=1" "$0" || fail "self-test remove gate missing"
  grep -q "/api/local/projects" "$0" || fail "self-test local API path missing"
  grep -q 'KUBECTL" get node' "$0" || fail "self-test real kubectl check missing"
  grep -q "Cloud marked drain completed before Agent result" "$0" || fail "self-test metadata boundary check missing"
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
