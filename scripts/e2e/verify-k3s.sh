#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-run}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="${OPSI_E2E_RUN_ID:-e2e-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
ARTIFACT_DIR="${OPSI_E2E_ARTIFACT_DIR:-$ROOT/.tmp/e2e-k3s/$RUN_ID}"
LOCAL_URL="${OPSI_E2E_LOCAL_URL:-http://127.0.0.1:9780}"
PROJECT_ID="${OPSI_E2E_PROJECT_ID:-}"
USER_ID="${OPSI_E2E_USER_ID:-e2e-owner@example.com}"
USER_ROLE="${OPSI_E2E_USER_ROLE:-Owner}"
SERVICE_NAME="${OPSI_E2E_SERVICE_NAME:-opsi-e2e-sample}"
SERVICE_ID="${OPSI_E2E_SERVICE_ID:-$SERVICE_NAME}"
BAD_SERVICE_NAME="${OPSI_E2E_BAD_SERVICE_NAME:-opsi-e2e-sample-bad}"
BAD_SERVICE_ID="${OPSI_E2E_BAD_SERVICE_ID:-$BAD_SERVICE_NAME}"
SERVICE_BRANCH="${OPSI_E2E_SERVICE_BRANCH:-main}"
SERVICE_REPO="${OPSI_E2E_SERVICE_REPO:-}"
SERVICE_SHA="${OPSI_E2E_SERVICE_SHA:-}"
BAD_SERVICE_SHA="${OPSI_E2E_BAD_SERVICE_SHA:-}"
SERVICE_CONTEXT="${OPSI_E2E_SERVICE_CONTEXT:-test/e2e/sample-service}"
SERVICE_DOCKERFILE="${OPSI_E2E_SERVICE_DOCKERFILE:-test/e2e/sample-service/Dockerfile}"
SERVICE_MANIFEST="${OPSI_E2E_SERVICE_MANIFEST:-test/e2e/sample-service/k8s/deployment.yaml}"
BAD_SERVICE_CONTEXT="${OPSI_E2E_BAD_SERVICE_CONTEXT:-test/e2e/sample-service-bad}"
BAD_SERVICE_DOCKERFILE="${OPSI_E2E_BAD_SERVICE_DOCKERFILE:-test/e2e/sample-service-bad/Dockerfile}"
BAD_SERVICE_MANIFEST="${OPSI_E2E_BAD_SERVICE_MANIFEST:-test/e2e/sample-service-bad/k8s/deployment.yaml}"
TARGET_HOST="${OPSI_E2E_VPS_HOST:-}"
TARGET_SSH_USER="${OPSI_E2E_VPS_SSH_USER:-root}"
TARGET_SSH_PORT="${OPSI_E2E_VPS_SSH_PORT:-22}"
TARGET_SSH_PASSWORD="${OPSI_E2E_VPS_SSH_PASSWORD:-}"
SECRET_NAME="${OPSI_E2E_SECRET_NAME:-opsi-e2e-secret}"
TOTP_CODE="${OPSI_E2E_TOTP_CODE:-}"
OTP_REQUEST_ID="${OPSI_E2E_OTP_REQUEST_ID:-}"
OTP_CODE="${OPSI_E2E_OTP_CODE:-}"
APP_SECRET_VALUE="${OPSI_E2E_APP_SECRET_VALUE:-e2e-secret-value-$RUN_ID}"
APPROVE_MITIGATION="${OPSI_E2E_APPROVE_MITIGATION:-}"
POLL_SECONDS="${OPSI_E2E_POLL_SECONDS:-900}"

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
  OPSI_E2E_VPS_SSH_PASSWORD
  OPSI_E2E_SERVICE_REPO
  OPSI_E2E_SERVICE_SHA
  OPSI_E2E_TOTP_CODE or OPSI_E2E_OTP_REQUEST_ID + OPSI_E2E_OTP_CODE
  OPSI_E2E_APPROVE_MITIGATION=YES

The local URL must be the CLI local backend. This script never calls Cloud
directly for runtime workflows.
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
    r"(?i)((password|ssh_password|token|agent_token|registration_token|pat|private_key|kubeconfig|app_secret|otp_code|totp_code)\s*[\"=:]+\s*)(\"[^\"]*\"|[^,\s}]+)",
]
for pat in patterns:
    data = re.sub(pat, lambda m: m.group(1) + "[REDACTED]", data)
sys.stdout.write(data)' "$TARGET_SSH_PASSWORD" "$APP_SECRET_VALUE" "$TOTP_CODE" "$OTP_CODE"
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

preflight() {
  mkdir -p "$ARTIFACT_DIR"
  log "preflight: artifact_dir=$ARTIFACT_DIR"
  for t in bash curl python3 ssh go node npm kubectl; do need_tool "$t"; done
  need_env OPSI_E2E_PROJECT_ID
  need_env OPSI_E2E_VPS_HOST
  need_env OPSI_E2E_VPS_SSH_PASSWORD
  need_env OPSI_E2E_SERVICE_REPO
  need_env OPSI_E2E_SERVICE_SHA
  if [ -z "$TOTP_CODE" ] && { [ -z "$OTP_REQUEST_ID" ] || [ -z "$OTP_CODE" ]; }; then
    fail "missing second factor: set OPSI_E2E_TOTP_CODE or OPSI_E2E_OTP_REQUEST_ID + OPSI_E2E_OTP_CODE"
  fi
  [ "$APPROVE_MITIGATION" = "YES" ] || fail "missing explicit approval: set OPSI_E2E_APPROVE_MITIGATION=YES"
  curl -fsS "$LOCAL_URL/health" >/dev/null || fail "local backend unavailable at OPSI_E2E_LOCAL_URL"
  if command -v sshpass >/dev/null 2>&1 && [ "${OPSI_E2E_SKIP_SSH_AUTH_CHECK:-}" != "1" ]; then
    SSHPASS="$TARGET_SSH_PASSWORD" sshpass -e ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -p "$TARGET_SSH_PORT" "$TARGET_SSH_USER@$TARGET_HOST" 'test "$(uname -s)" = Linux && test -r /etc/os-release' >/dev/null || fail "SSH auth/preflight failed"
  else
    log "preflight: sshpass unavailable or auth check skipped; bootstrap worker will verify SSH"
  fi
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
  local file="$1" expr="$2"
  python3 - "$file" "$expr" <<'PY'
import json, os, sys
file, kind = sys.argv[1], sys.argv[2]
e = os.environ
if kind == "bootstrap":
    data = {"role":"first_server","public_host":e["OPSI_E2E_VPS_HOST"],"ssh_port":int(e.get("OPSI_E2E_VPS_SSH_PORT","22")),"ssh_username":e.get("OPSI_E2E_VPS_SSH_USER","root"),"auth_method":"password","ssh_password":e["OPSI_E2E_VPS_SSH_PASSWORD"]}
elif kind == "service":
    data = {"name":e.get("OPSI_E2E_SERVICE_NAME","opsi-e2e-sample"),"type":"application","source_type":"git","repo_url":e["OPSI_E2E_SERVICE_REPO"],"branch":e.get("OPSI_E2E_SERVICE_BRANCH","main"),"git_sha":e["OPSI_E2E_SERVICE_SHA"],"build_method":"dockerfile","build_context":e.get("OPSI_E2E_SERVICE_CONTEXT","test/e2e/sample-service"),"dockerfile":e.get("OPSI_E2E_SERVICE_DOCKERFILE","test/e2e/sample-service/Dockerfile"),"manifest_path":e.get("OPSI_E2E_SERVICE_MANIFEST","test/e2e/sample-service/k8s/deployment.yaml"),"watch_paths":[e.get("OPSI_E2E_SERVICE_CONTEXT","test/e2e/sample-service") + "/**"],"container_port":8080,"health_path":"/health","replicas":1,"resource_requests":{"cpu":"50m","memory":"64Mi"},"resource_limits":{"cpu":"250m","memory":"256Mi"}}
elif kind == "bad_service":
    data = {"name":e.get("OPSI_E2E_BAD_SERVICE_NAME","opsi-e2e-sample-bad"),"type":"application","source_type":"git","repo_url":e["OPSI_E2E_SERVICE_REPO"],"branch":e.get("OPSI_E2E_SERVICE_BRANCH","main"),"git_sha":e.get("OPSI_E2E_BAD_SERVICE_SHA") or e["OPSI_E2E_SERVICE_SHA"],"build_method":"dockerfile","build_context":e.get("OPSI_E2E_BAD_SERVICE_CONTEXT","test/e2e/sample-service-bad"),"dockerfile":e.get("OPSI_E2E_BAD_SERVICE_DOCKERFILE","test/e2e/sample-service-bad/Dockerfile"),"manifest_path":e.get("OPSI_E2E_BAD_SERVICE_MANIFEST","test/e2e/sample-service-bad/k8s/deployment.yaml"),"watch_paths":[e.get("OPSI_E2E_BAD_SERVICE_CONTEXT","test/e2e/sample-service-bad") + "/**"],"container_port":8080,"health_path":"/health","replicas":1,"resource_requests":{"cpu":"50m","memory":"64Mi"},"resource_limits":{"cpu":"250m","memory":"256Mi"}}
elif kind == "secret":
    data = {"service_id":e.get("OPSI_E2E_SERVICE_ID", e.get("OPSI_E2E_SERVICE_NAME","opsi-e2e-sample")),"name":e.get("OPSI_E2E_SECRET_NAME","opsi-e2e-secret"),"namespace":"default","user_id":e.get("OPSI_E2E_USER_ID","e2e-owner@example.com"),"role":e.get("OPSI_E2E_USER_ROLE","Owner")}
elif kind == "second_factor":
    data = {"service_id":e.get("OPSI_E2E_SERVICE_ID", e.get("OPSI_E2E_SERVICE_NAME","opsi-e2e-sample")),"name":e.get("OPSI_E2E_SECRET_NAME","opsi-e2e-secret"),"namespace":"default","user_id":e.get("OPSI_E2E_USER_ID","e2e-owner@example.com"),"role":e.get("OPSI_E2E_USER_ROLE","Owner"),"reveal":True}
    if e.get("OPSI_E2E_TOTP_CODE"): data["totp_code"] = e["OPSI_E2E_TOTP_CODE"]
    else: data.update({"otp_request_id":e["OPSI_E2E_OTP_REQUEST_ID"],"otp_code":e["OPSI_E2E_OTP_CODE"]})
elif kind == "incident_user":
    data = {"user_id":e.get("OPSI_E2E_USER_ID","e2e-owner@example.com"),"role":e.get("OPSI_E2E_USER_ROLE","Owner")}
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
    body="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments" - "deploy-list-$deploy_id" 0)" || true
    value="$(printf '%s' "$body" | python3 -c 'import json, sys
d = json.load(sys.stdin)
for item in d.get("deployments", []):
    if item.get("id") == sys.argv[1]:
        print(item.get("status", ""))
        raise SystemExit(0)' "$deploy_id" 2>/dev/null || true)"
    [ "$value" = "$expect" ] && return 0
    case "$value" in
      failed|failed_after_rollback|rolled_back|blocked|dead_letter) [ "$expect" = "$value" ] && return 0 ;;
    esac
    now="$(date +%s)"
    [ $((now - start)) -lt "$POLL_SECONDS" ] || fail "timeout waiting for deployment $deploy_id=$expect, last=$value"
    sleep 10
  done
}

wait_deployment_terminal() {
  local deploy_id="$1" start now body value
  start="$(date +%s)"
  while :; do
    body="$(api_file GET "/api/local/projects/$PROJECT_ID/deployments" - "deploy-list-$deploy_id" 0)" || true
    value="$(printf '%s' "$body" | python3 -c 'import json, sys
d = json.load(sys.stdin)
for item in d.get("deployments", []):
    if item.get("id") == sys.argv[1]:
        print(item.get("status", ""))
        raise SystemExit(0)' "$deploy_id" 2>/dev/null || true)"
    case "$value" in
      failed|failed_after_rollback|rolled_back|blocked|dead_letter|succeeded) return 0 ;;
    esac
    now="$(date +%s)"
    [ $((now - start)) -lt "$POLL_SECONDS" ] || fail "timeout waiting for deployment $deploy_id terminal state, last=$value"
    sleep 10
  done
}

check_artifacts_clean() {
  python3 - "$ARTIFACT_DIR" "$TARGET_SSH_PASSWORD" "$APP_SECRET_VALUE" "$TOTP_CODE" "$OTP_CODE" <<'PY'
import pathlib, sys
root = pathlib.Path(sys.argv[1])
secrets = [s for s in sys.argv[2:] if s]
for path in root.rglob("*"):
    if not path.is_file():
        continue
    text = path.read_text(errors="ignore")
    for secret in secrets:
        if secret and secret in text:
            print(path)
            raise SystemExit(1)
PY
}

remote_k3s() {
  SSHPASS="$TARGET_SSH_PASSWORD" sshpass -e ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 -p "$TARGET_SSH_PORT" "$TARGET_SSH_USER@$TARGET_HOST" "$@"
}

verify_runtime() {
  need_tool sshpass
  remote_k3s "sudo k3s kubectl -n default rollout status deployment/$SERVICE_NAME --timeout=120s" | redact > "$ARTIFACT_DIR/k3s-rollout.redacted.log" || fail "K3s rollout status failed"
  remote_k3s "sudo k3s kubectl -n default get deploy,svc,pods -l app.kubernetes.io/name=$SERVICE_NAME -o wide" | redact > "$ARTIFACT_DIR/k3s-runtime.redacted.log" || fail "K3s runtime state failed"
}

manual_cleanup() {
  mkdir -p "$ARTIFACT_DIR"
  cat > "$ARTIFACT_DIR/cleanup.txt" <<EOF
Manual cleanup for run $RUN_ID:
  ssh $TARGET_SSH_USER@$TARGET_HOST 'sudo k3s kubectl -n default delete deploy,svc -l app.kubernetes.io/name=$SERVICE_NAME --ignore-not-found'
  ssh $TARGET_SSH_USER@$TARGET_HOST 'sudo bash /tmp/opsi/scripts/vps-reset.sh --dry-run'
  Review $LOCAL_URL via local UI and revoke/remove E2E project resources created with idempotency prefix $RUN_ID.
EOF
}

run_e2e() {
  preflight
  LOCAL_SESSION="$(session_token)"
  [ -n "$LOCAL_SESSION" ] || fail "local session token missing"
  log "step 1/15 clean VPS target accepted: $TARGET_HOST"
  local f body id deploy_id incidents incident_id analyze action_id action_hash approve_body audit
  f="$(mktemp)"; write_json "$f" bootstrap
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/nodes/bootstrap" "$f" bootstrap 1)" || fail "bootstrap session create failed"
  rm -f "$f"
  id="$(printf '%s' "$body" | json_get id)" || fail "bootstrap response missing id"
  log "step 2-6 bootstrap/session created through local backend: session=$id"
  wait_json_field "/api/local/projects/$PROJECT_ID/bootstrap-sessions/$id" status completed bootstrap-session
  wait_json_field "/api/local/projects/$PROJECT_ID/readiness" status ready readiness
  log "step 6 Agent heartbeat/readiness verified"

  f="$(mktemp)"; write_json "$f" service
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/services" "$f" service-create 1)" || fail "service create failed"
  rm -f "$f"
  SERVICE_ID="$(printf '%s' "$body" | json_get id)" || fail "service response missing id"
  export OPSI_E2E_SERVICE_ID="$SERVICE_ID"
  log "step 7 service draft created: service=$SERVICE_ID"
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/services/$SERVICE_ID/deployments" <(printf '{"requested_by":"%s"}' "$USER_ID") deploy-start 1)" || fail "deployment start failed"
  deploy_id="$(printf '%s' "$body" | json_get id)" || fail "deployment response missing id"
  wait_deployment_status "$deploy_id" succeeded
  verify_runtime
  log "step 8 rollout/runtime verified"

  f="$(mktemp)"; write_json "$f" secret
  api_file POST "/api/local/projects/$PROJECT_ID/secrets" "$f" secret-create 1 >/dev/null || fail "secret create failed"
  write_json "$f" second_factor
  api_file POST "/api/local/projects/$PROJECT_ID/secrets/$SECRET_NAME/rotate" "$f" secret-rotate 1 >/dev/null || fail "secret rotate failed"
  if api_file POST "/api/local/projects/$PROJECT_ID/secrets/$SECRET_NAME/reveal" "$f" secret-reveal 1 | grep -q "$APP_SECRET_VALUE"; then
    fail "secret value leaked into reveal output"
  fi
  rm -f "$f"
  log "step 9 secret create/rotate/reveal path ran via local Agent facade"

  api_file GET "/api/local/projects/$PROJECT_ID/telemetry/summary?service_id=$SERVICE_ID" - telemetry-summary 0 >/dev/null || fail "telemetry summary failed"
  api_file GET "/api/local/projects/$PROJECT_ID/logs?service_id=$SERVICE_ID&limit=50" - logs 0 >/dev/null || fail "logs failed"
  log "step 10 sanitized telemetry/logs fetched through local backend"

  f="$(mktemp)"; write_json "$f" bad_service
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/services" "$f" bad-service-create 1)" || fail "bad service create failed"
  rm -f "$f"
  BAD_SERVICE_ID="$(printf '%s' "$body" | json_get id)" || fail "bad service response missing id"
  body="$(api_file POST "/api/local/projects/$PROJECT_ID/services/$BAD_SERVICE_ID/deployments" <(printf '{"requested_by":"%s"}' "$USER_ID") bad-deploy-start 1)" || fail "bad deployment start failed"
  deploy_id="$(printf '%s' "$body" | json_get id)" || fail "bad deployment response missing id"
  wait_deployment_terminal "$deploy_id"
  log "step 11 controlled incident trigger executed via failing rollout: deployment=$deploy_id"
  incidents="$(api_file GET "/api/local/projects/$PROJECT_ID/incidents?user_id=$USER_ID&role=$USER_ROLE&status=open&limit=10" - incidents 0)" || fail "incident list failed"
  incident_id="$(printf '%s' "$incidents" | python3 -c 'import json,sys; d=json.load(sys.stdin); a=d.get("incidents", d if isinstance(d,list) else []); print(a[0]["id"] if a else "")')"
  [ -n "$incident_id" ] || fail "no controlled incident found; E2E does not pass without a real Agent incident"
  f="$(mktemp)"; write_json "$f" incident_user
  analyze="$(api_file POST "/api/local/projects/$PROJECT_ID/incidents/$incident_id/analyze" "$f" incident-analyze 1)" || fail "RCA analyze failed"
  action_id="$(printf '%s' "$analyze" | python3 -c 'import json,sys; d=json.load(sys.stdin); a=d.get("actions") or d.get("recommended_actions") or []; print(a[0].get("id","") if a else "")')"
  action_hash="$(printf '%s' "$analyze" | python3 -c 'import json,sys; d=json.load(sys.stdin); a=d.get("actions") or d.get("recommended_actions") or []; print(a[0].get("action_hash","") if a else "")')"
  [ -n "$action_id" ] && [ -n "$action_hash" ] || fail "RCA produced no typed allowlisted mitigation"
  api_file POST "/api/local/projects/$PROJECT_ID/incidents/$incident_id/actions/$action_id/approve" "$f" incident-approval-denied 1 >/dev/null && fail "mitigation approved without explicit approval"
  approve_body="$(mktemp)"
  python3 - "$approve_body" "$action_hash" <<'PY'
import json, os, sys
open(sys.argv[1],"w").write(json.dumps({"user_id":os.environ.get("OPSI_E2E_USER_ID","e2e-owner@example.com"),"role":os.environ.get("OPSI_E2E_USER_ROLE","Owner"),"approved":True,"action_hash":sys.argv[2]}))
PY
  api_file POST "/api/local/projects/$PROJECT_ID/incidents/$incident_id/actions/$action_id/approve" "$approve_body" incident-approve 1 >/dev/null || fail "approved mitigation failed"
  rm -f "$f" "$approve_body"
  log "step 12-13 RCA sanitized context + explicit allowlisted mitigation verified"

  audit="$(api_file GET "/api/local/projects/$PROJECT_ID/audit" - audit 0)" || fail "audit fetch failed"
  printf '%s' "$audit" | grep -E 'BOOTSTRAP|DEPLOYMENT|SERVICE|AGENT|SECRET|INCIDENT|MITIGATION|AUDIT' >/dev/null || fail "audit trail missing expected events"
  check_artifacts_clean || fail "redaction failed: artifact contains sensitive value"
  log "step 14 audit verified without sensitive payloads"
  manual_cleanup
  log "step 15 cleanup instructions written"
  log "PASS: clean VPS/K3s E2E proof complete"
}

self_test() {
  mkdir -p "$ARTIFACT_DIR"
  OPSI_E2E_VPS_SSH_PASSWORD="secret-password" OPSI_E2E_APP_SECRET_VALUE="app-secret" OPSI_E2E_TOTP_CODE="123456" OPSI_E2E_OTP_CODE="" \
    bash -c 'printf "password=secret-password token=abc kubeconfig=raw app-secret 123456" | '"$0"' --redact-only' > "$ARTIFACT_DIR/redaction-test.txt"
  grep -q '\[REDACTED\]' "$ARTIFACT_DIR/redaction-test.txt" || fail "self-test redaction failed"
  if env -i PATH="$PATH" OPSI_E2E_ARTIFACT_DIR="$ARTIFACT_DIR/missing" "$0" --preflight >/tmp/opsi-e2e-preflight.out 2>&1; then
    fail "self-test missing prereq did not fail"
  fi
  grep -Eq "missing (env|tool):" /tmp/opsi-e2e-preflight.out || fail "self-test missing prereq message unclear"
  grep -q "/api/local/projects" "$0" || fail "self-test local backend path missing"
  grep -q "k3s kubectl" "$0" || fail "self-test real K3s check missing"
  grep -q "X-Local-Session" "$0" || fail "self-test local session guard missing"
  grep -q "explicit approval" "$0" || fail "self-test mitigation approval guard missing"
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
