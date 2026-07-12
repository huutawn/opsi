#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly DEPLOY_DIR="$REPO_ROOT/deploy/dev-control-plane"
readonly ENV_FILE="$DEPLOY_DIR/.env"
readonly CLOUD_CONFIG="$DEPLOY_DIR/config/cloud.json"
readonly WORKER_CONFIG="$DEPLOY_DIR/config/bootstrap-worker.json"
readonly SECRETS_DIR="$DEPLOY_DIR/secrets"
readonly PAT_FILE="$SECRETS_DIR/initial-owner.pat"
readonly EXPECTED_BRANCH="${OPSI_V3_013_BRANCH:-developer}"
readonly REQUIRED_BASE_COMMIT="${OPSI_V3_013_BASE_COMMIT:-a26ecc8}"
readonly HTTP_PORT="${OPSI_DEV_HTTP_PORT:-18080}"
readonly DEADLINE_SECONDS="${OPSI_V3_013_DEADLINE_SECONDS:-120}"
readonly COMPOSE=(docker compose --env-file "$ENV_FILE" -f "$DEPLOY_DIR/compose.yaml")
readonly SERVICES=(postgres cloud bootstrap-worker reverse-proxy)
readonly RESTART_ORDER=(reverse-proxy bootstrap-worker cloud postgres)

mode=""
evidence_path=""
tmp_dir=""
safe_output=""
compose_logs=""
evidence_tmp=""
postgres_password=""
worker_token=""
bootstrap_key=""
alert_token=""
run_start=""

usage() {
  echo "usage: $0 --preflight | --evidence PATH" >&2
}

fail() {
  echo "V3-013 verification failed: $1" >&2
  exit 1
}

cleanup() {
  if [[ -n "$evidence_tmp" && -e "$evidence_tmp" ]]; then
    rm -f -- "$evidence_tmp"
  fi
  if [[ -n "$tmp_dir" && -d "$tmp_dir" ]]; then
    rm -rf -- "$tmp_dir"
  fi
}
trap cleanup EXIT

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

compose() {
  "${COMPOSE[@]}" "$@"
}

record_command() {
  local label="$1"
  shift
  printf '%s\n' "$label" >>"$safe_output"
  "$@" >>"$safe_output" 2>&1 || fail "$label"
}

assert_supported_host() {
  local os_id version_id arch cpu_count memory_kib disk_kib
  # shellcheck disable=SC1091
  source /etc/os-release
  os_id="${ID:-}"
  version_id="${VERSION_ID:-}"
  case "$os_id:$version_id" in
    ubuntu:24.04|fedora:44) ;;
    *) fail "supported OS required: Ubuntu 24.04 or Fedora 44" ;;
  esac
  arch="$(uname -m)"
  [[ "$arch" == "x86_64" ]] || fail "x86_64 architecture required"
  cpu_count="$(getconf _NPROCESSORS_ONLN)"
  (( cpu_count >= 2 )) || fail "at least 2 CPUs required"
  memory_kib="$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)"
  (( memory_kib >= 3900000 )) || fail "at least 4 GiB RAM required"
  disk_kib="$(df -Pk / | awk 'NR == 2 { print $2 }')"
  (( disk_kib >= 19000000 )) || fail "at least 20 GiB disk required"
}

assert_clean_git() {
  [[ -z "$(git -C "$REPO_ROOT" status --porcelain)" ]] || fail "repository must be clean"
  [[ "$(git -C "$REPO_ROOT" branch --show-current)" == "$EXPECTED_BRANCH" ]] ||
    fail "expected branch $EXPECTED_BRANCH"
  git -C "$REPO_ROOT" merge-base --is-ancestor "$REQUIRED_BASE_COMMIT" HEAD ||
    fail "required base commit $REQUIRED_BASE_COMMIT is not an ancestor of HEAD"
}

assert_deployment_files() {
  local file
  for file in \
    "$REPO_ROOT/cloud/Dockerfile" \
    "$DEPLOY_DIR/compose.yaml" \
    "$DEPLOY_DIR/Caddyfile" \
    "$DEPLOY_DIR/.env.example" \
    "$DEPLOY_DIR/config/cloud.example.json" \
    "$DEPLOY_DIR/config/bootstrap-worker.example.json"; do
    [[ -f "$file" ]] || fail "missing deployment file: ${file#"$REPO_ROOT/"}"
  done
}

assert_no_runtime_config() {
  [[ ! -e "$ENV_FILE" ]] || fail "old runtime file exists: ${ENV_FILE#"$REPO_ROOT/"}"
  [[ ! -e "$CLOUD_CONFIG" ]] || fail "old runtime file exists: ${CLOUD_CONFIG#"$REPO_ROOT/"}"
  [[ ! -e "$WORKER_CONFIG" ]] || fail "old runtime file exists: ${WORKER_CONFIG#"$REPO_ROOT/"}"
  [[ ! -e "$SECRETS_DIR" ]] || fail "old runtime secrets directory exists"
}

assert_clean_docker_baseline() {
  local regex='^(opsi-|opsi_dev|opsi-dev)'
  if docker ps -a --format '{{.Names}}' | grep -E "$regex" >/dev/null; then
    fail "old Opsi container exists"
  fi
  if docker volume ls --format '{{.Name}}' | grep -E "$regex" >/dev/null; then
    fail "old Opsi volume exists"
  fi
  if docker image ls --format '{{.Repository}}' | grep -E "$regex" >/dev/null; then
    fail "old Opsi image exists"
  fi
}

assert_port_available() {
  python3 - "$HTTP_PORT" <<'PY'
import socket
import sys

port = int(sys.argv[1])
if not 1 <= port <= 65535:
    raise SystemExit("invalid HTTP port")
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    sock.bind(("127.0.0.1", port))
finally:
    sock.close()
PY
}

preflight() {
  local command
  for command in git make curl openssl python3 docker; do
    require_command "$command"
  done
  assert_supported_host
  assert_clean_git
  assert_deployment_files
  assert_no_runtime_config
  docker info >/dev/null 2>&1 || fail "Docker daemon is unavailable"
  docker compose version >/dev/null 2>&1 || fail "Docker Compose plugin is unavailable"
  assert_clean_docker_baseline
  assert_port_available
  echo "V3-013 clean-VM preflight passed"
}

create_temp_state() {
  mkdir -p "$REPO_ROOT/.tmp"
  tmp_dir="$(mktemp -d "$REPO_ROOT/.tmp/v3-013.XXXXXX")"
  chmod 0700 "$tmp_dir"
  safe_output="$tmp_dir/safe-output.txt"
  compose_logs="$tmp_dir/compose.log"
  : >"$safe_output"
  : >"$compose_logs"
  chmod 0600 "$safe_output" "$compose_logs"
}

generate_runtime_config() {
  umask 077
  postgres_password="$(openssl rand -hex 32)"
  worker_token="$(openssl rand -hex 32)"
  bootstrap_key="$(openssl rand -hex 32)"
  alert_token="$(openssl rand -hex 32)"
  mkdir -p "$SECRETS_DIR"
  chmod 0700 "$SECRETS_DIR"
  V3_POSTGRES_PASSWORD="$postgres_password" \
  V3_WORKER_TOKEN="$worker_token" \
  V3_BOOTSTRAP_KEY="$bootstrap_key" \
  V3_ALERT_TOKEN="$alert_token" \
  python3 - "$DEPLOY_DIR/.env.example" "$ENV_FILE" \
    "$DEPLOY_DIR/config/cloud.example.json" "$CLOUD_CONFIG" \
    "$DEPLOY_DIR/config/bootstrap-worker.example.json" "$WORKER_CONFIG" \
    "$HTTP_PORT" <<'PY'
import json
import os
import pathlib
import sys

env_src, env_dst, cloud_src, cloud_dst, worker_src, worker_dst = map(pathlib.Path, sys.argv[1:7])
port = sys.argv[7]
postgres_password = os.environ["V3_POSTGRES_PASSWORD"]
worker_token = os.environ["V3_WORKER_TOKEN"]
bootstrap_key = os.environ["V3_BOOTSTRAP_KEY"]
alert_token = os.environ["V3_ALERT_TOKEN"]

env_text = env_src.read_text()
env_text = env_text.replace("OPSI_DEV_HTTP_PORT=8080", f"OPSI_DEV_HTTP_PORT={port}")
env_text = env_text.replace("REPLACE_WITH_RANDOM_PASSWORD", postgres_password)
env_dst.write_text(env_text)

cloud = json.loads(cloud_src.read_text())
cloud["database_url"] = f"postgres://opsi:{postgres_password}@postgres:5432/opsi?sslmode=disable"
cloud["public_base_url"] = f"http://127.0.0.1:{port}"
cloud["bootstrap_worker_token"] = worker_token
cloud["bootstrap_secret_key"] = bootstrap_key
cloud["alerts"]["internal_token"] = alert_token
cloud["auth"]["provider"] = "github"
cloud_dst.write_text(json.dumps(cloud, indent=2) + "\n")

worker = json.loads(worker_src.read_text())
worker["bootstrap_worker_token"] = worker_token
worker["agent_install_url"] = "https://example.invalid/opsi-agent"
worker["agent_install_sha256"] = "0" * 64
worker_dst.write_text(json.dumps(worker, indent=2) + "\n")
PY
  chmod 0600 "$ENV_FILE" "$CLOUD_CONFIG" "$WORKER_CONFIG"
  if grep -E -q 'REPLACE_WITH_|CHANGE_ME|EXAMPLE_SECRET' "$ENV_FILE" "$CLOUD_CONFIG" "$WORKER_CONFIG"; then
    fail "runtime configuration still contains a placeholder"
  fi
  [[ "$(stat -c '%a' "$SECRETS_DIR")" == "700" ]] || fail "secrets directory mode is not 0700"
}

assert_service_list() {
  local service_file="$tmp_dir/services.txt"
  compose config --services >"$service_file"
  python3 - "$service_file" <<'PY'
import pathlib
import sys

actual = pathlib.Path(sys.argv[1]).read_text().splitlines()
expected = ["postgres", "cloud", "bootstrap-worker", "reverse-proxy"]
if actual != expected:
    raise SystemExit(f"unexpected Compose services: {actual}")
PY
}

container_id() {
  local id
  id="$(compose ps -q "$1")"
  [[ -n "$id" && "$id" != *$'\n'* ]] || fail "expected one container for $1"
  printf '%s' "$id"
}

service_state() {
  docker inspect --format '{{.Name}}|{{.Id}}|{{.State.StartedAt}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$1"
}

wait_all_healthy() {
  local deadline=$((SECONDS + DEADLINE_SECONDS)) service id state status health all_ready
  while (( SECONDS < deadline )); do
    all_ready=1
    for service in "${SERVICES[@]}"; do
      id="$(compose ps -q "$service")"
      if [[ -z "$id" ]]; then
        all_ready=0
        continue
      fi
      state="$(service_state "$id")"
      IFS='|' read -r _ _ _ status health <<<"$state"
      if [[ "$status" != "running" || "$health" != "healthy" ]]; then
        all_ready=0
      fi
    done
    (( all_ready == 1 )) && return 0
    sleep 2
  done
  fail "four services did not become running and healthy within ${DEADLINE_SECONDS}s"
}

verify_external_health() {
  curl --fail --silent --show-error "http://127.0.0.1:${HTTP_PORT}/health" \
    >/dev/null 2>>"$safe_output"
}

parse_owner_json() {
  local json_file="$1" metadata_file="$2"
  python3 - "$json_file" "$metadata_file" <<'PY'
import json
import pathlib
import sys

data = json.loads(pathlib.Path(sys.argv[1]).read_text())
def pick(*names):
    for name in names:
        if name in data:
            return data[name]
    raise SystemExit(f"missing owner field: {names[0]}")

values = [
    pick("user_id", "UserID", "userId"),
    pick("organization_id", "OrganizationID", "organizationId"),
    pick("project_id", "ProjectID", "projectId"),
    pick("membership_role", "MembershipRole", "membershipRole", "role"),
    pick("reused", "Reused"),
    pick("pat_created", "PATCreated", "patCreated"),
]
if any(not str(value) for value in values[:4]):
    raise SystemExit("empty owner metadata")
pathlib.Path(sys.argv[2]).write_text("\n".join(str(v).lower() if isinstance(v, bool) else str(v) for v in values) + "\n")
PY
}

bootstrap_owner() {
  local issue_pat="$1" output_file="$2" metadata_file="$3"
  local args=(run --rm cloud admin bootstrap-owner --config /etc/opsi/cloud.json
    --email cleanvm-owner@example.invalid --org-name "Opsi Clean VM"
    --project-name "Control Plane Evidence" --oauth-provider github
    --oauth-subject v3-013-clean-vm-owner)
  if [[ "$issue_pat" == "yes" ]]; then
    args+=(--pat-output-file /run/opsi-secrets/initial-owner.pat)
  fi
  args+=(--json)
  compose "${args[@]}" >"$output_file" 2>>"$safe_output" || fail "bootstrap first Owner"
  parse_owner_json "$output_file" "$metadata_file"
}

verify_pat() {
  local project_id="$1" expected_user_id="$2" response_file
  response_file="$(mktemp "$tmp_dir/pat-response.XXXXXX.json")"
  python3 - "$PAT_FILE" "$project_id" <<'PY' |
import json
import pathlib
import sys

token = pathlib.Path(sys.argv[1]).read_text().strip()
json.dump({"token": token, "project_id": sys.argv[2]}, sys.stdout)
PY
    curl --fail --silent --show-error -H 'Content-Type: application/json' \
      --data-binary @- "http://127.0.0.1:${HTTP_PORT}/v1/auth/pat/verify" \
      >"$response_file" 2>>"$safe_output"
  python3 - "$response_file" "$project_id" "$expected_user_id" <<'PY'
import json
import pathlib
import sys

data = json.loads(pathlib.Path(sys.argv[1]).read_text())
def find(names, value):
    if isinstance(value, dict):
        for key, item in value.items():
            if key in names:
                return item
            found = find(names, item)
            if found is not None:
                return found
    elif isinstance(value, list):
        for item in value:
            found = find(names, item)
            if found is not None:
                return found
    return None

project = find({"project_id", "ProjectID", "projectId"}, data)
user = find({"user_id", "UserID", "userId"}, data)
role = find({"role", "membership_role", "MembershipRole"}, data)
if project != sys.argv[2] or user != sys.argv[3] or str(role).lower() != "owner":
    raise SystemExit("PAT verification metadata mismatch")
PY
}

capture_snapshot() {
  local output="$1" service id
  : >"$output"
  for service in "${SERVICES[@]}"; do
    id="$(container_id "$service")"
    printf '%s|%s\n' "$service" "$(service_state "$id")" >>"$output"
  done
}

assert_independent_restart() {
  local target="$1" before="$tmp_dir/before-$target" after="$tmp_dir/after-$target"
  capture_snapshot "$before"
  compose restart "$target" >>"$safe_output" 2>&1 || fail "restart $target"
  wait_all_healthy
  verify_external_health
  capture_snapshot "$after"
  python3 - "$target" "$before" "$after" <<'PY'
import pathlib
import sys

target = sys.argv[1]
def load(path):
    result = {}
    for line in pathlib.Path(path).read_text().splitlines():
        service, name, cid, started, status, health = line.split("|", 5)
        result[service] = (name, cid, started, status, health)
    return result

before, after = load(sys.argv[2]), load(sys.argv[3])
for service in before:
    if after[service][3:] != ("running", "healthy"):
        raise SystemExit(f"{service} is not running and healthy")
    if before[service][1] != after[service][1]:
        raise SystemExit(f"{service} container was recreated")
    if service == target:
        if before[service][2] == after[service][2]:
            raise SystemExit(f"{service} StartedAt did not change")
    elif before[service][2] != after[service][2]:
        raise SystemExit(f"non-target service restarted: {service}")
PY
}

assert_owner_reuse() {
  local expected_user="$1" expected_org="$2" expected_project="$3"
  local json_file metadata_file
  local user org project role reused pat_created
  local -a values
  json_file="$(mktemp "$tmp_dir/owner-reuse.XXXXXX.json")"
  metadata_file="$(mktemp "$tmp_dir/owner-reuse.XXXXXX.meta")"
  bootstrap_owner no "$json_file" "$metadata_file"
  mapfile -t values <"$metadata_file"
  user="${values[0]}"; org="${values[1]}"; project="${values[2]}"
  role="${values[3]}"; reused="${values[4]}"; pat_created="${values[5]}"
  [[ "$user" == "$expected_user" && "$org" == "$expected_org" && "$project" == "$expected_project" ]] ||
    fail "bootstrap-owner reuse changed domain IDs"
  [[ "${role,,}" == "owner" && "$reused" == "true" && "$pat_created" == "false" ]] ||
    fail "bootstrap-owner reuse metadata mismatch"
}

volume_identity() {
  local volume_name
  volume_name="$(compose config --volumes | awk '$0 == "postgres-data" { print; found=1 } END { if (!found) exit 1 }')"
  volume_name="${COMPOSE_PROJECT_NAME:-opsi-dev}_${volume_name}"
  docker volume inspect --format '{{.Name}}|{{.Mountpoint}}|{{.CreatedAt}}' "$volume_name"
}

collect_image_evidence() {
  local output="$1" service id image_ref image_id digests
  : >"$output"
  for service in "${SERVICES[@]}"; do
    id="$(container_id "$service")"
    image_ref="$(docker inspect --format '{{.Config.Image}}' "$id")"
    image_id="$(docker inspect --format '{{.Image}}' "$id")"
    digests="$(docker image inspect --format '{{join .RepoDigests ","}}' "$image_ref")"
    printf '%s|%s|%s\n' "$service" "$image_id" "${digests:-none}" >>"$output"
  done
}

capture_compose_logs() {
  compose logs --no-color >>"$compose_logs" 2>&1 || fail "capture Compose logs"
}

scan_secrets() {
  local git_diff="$tmp_dir/git.diff" git_status="$tmp_dir/git.status"
  capture_compose_logs
  git -C "$REPO_ROOT" diff >"$git_diff"
  git -C "$REPO_ROOT" status --short >"$git_status"
  python3 - "$ENV_FILE" "$CLOUD_CONFIG" "$PAT_FILE" "$tmp_dir" "$evidence_tmp" <<'PY'
import json
import pathlib
import sys

env_path, cloud_path, pat_path, tmp_dir, evidence_path = map(pathlib.Path, sys.argv[1:])
env_values = dict(line.split("=", 1) for line in env_path.read_text().splitlines() if "=" in line)
cloud = json.loads(cloud_path.read_text())
secrets = {
    "postgres_password": env_values["POSTGRES_PASSWORD"].encode(),
    "bootstrap_worker_token": cloud["bootstrap_worker_token"].encode(),
    "bootstrap_secret_key": cloud["bootstrap_secret_key"].encode(),
    "alert_token": cloud["alerts"]["internal_token"].encode(),
    "initial_pat": pat_path.read_bytes().strip(),
}
targets = [path for path in tmp_dir.rglob("*") if path.is_file()] + [evidence_path]
for label, secret in secrets.items():
    if not secret:
        raise SystemExit(f"empty secret during leak scan: {label}")
    for path in targets:
        if secret in path.read_bytes():
            print(f"SECRET_LEAK_DETECTED: {label}", file=sys.stderr)
            raise SystemExit(1)
PY
}

write_evidence() {
  local run_end="$1" commit_sha="$2" owner_user="$3" owner_org="$4" owner_project="$5"
  local volume_name="$6" image_file="$7" restart_table="$8"
  local os_pretty kernel arch cpu_count memory_kib docker_version compose_version run_id project_name
  os_pretty="$(. /etc/os-release && printf '%s' "$PRETTY_NAME")"
  kernel="$(uname -r)"; arch="$(uname -m)"; cpu_count="$(getconf _NPROCESSORS_ONLN)"
  memory_kib="$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)"
  docker_version="$(docker version --format '{{.Server.Version}}')"
  compose_version="$(docker compose version --short)"
  run_id="v3-013-$(date -u +%Y%m%dT%H%M%SZ)-${commit_sha:0:12}"
  project_name="$(awk -F= '$1 == "COMPOSE_PROJECT_NAME" { print $2 }' "$ENV_FILE")"
  mkdir -p "$(dirname "$evidence_path")"
  evidence_tmp="$(mktemp "$(dirname "$evidence_path")/.v3-013.XXXXXX")"
  chmod 0600 "$evidence_tmp"
  {
    echo "# V3-013 Clean-VM Control-Plane Evidence"
    echo
    echo "| Metadata | Value |"
    echo "|---|---|"
    echo "| Run ID | \`$run_id\` |"
    echo "| UTC start | \`$run_start\` |"
    echo "| UTC end | \`$run_end\` |"
    echo "| Git commit SHA | \`$commit_sha\` |"
    echo "| OS/version | $os_pretty |"
    echo "| Kernel | \`$kernel\` |"
    echo "| Architecture | \`$arch\` |"
    echo "| CPU/RAM | $cpu_count vCPU / $((memory_kib / 1024)) MiB |"
    echo "| Docker version | \`$docker_version\` |"
    echo "| Compose version | \`$compose_version\` |"
    echo "| Compose project name | \`$project_name\` |"
    echo "| Service list | \`postgres, cloud, bootstrap-worker, reverse-proxy\` |"
    echo "| Named PostgreSQL volume | \`$volume_name\` |"
    echo
    echo "## Service and image identities"
    echo
    echo '| Service | Image ID | Repository digest |'
    echo '|---|---|---|'
    while IFS='|' read -r service image digests; do
      echo "| \`$service\` | \`$image\` | \`$digests\` |"
    done <"$image_file"
    echo
    echo "## First Owner safe identifiers"
    echo
    echo "| Object | ID |"
    echo "|---|---|"
    echo "| User | \`$owner_user\` |"
    echo "| Organization | \`$owner_org\` |"
    echo "| Project | \`$owner_project\` |"
    echo "| Role | Owner |"
    echo
    echo "## Test results"
    echo
    echo "| Check | Result |"
    echo "|---|---|"
    for check in "Clean baseline" "Compose validation" "Image build" "Four services healthy" \
      "External health" "First Owner created" "PAT permission 0600" "PAT verify through Caddy" \
      "Exact Owner reuse" "Reverse proxy restart" "Bootstrap Worker restart" "Cloud restart" \
      "PostgreSQL restart" "Full stack down/up" "PostgreSQL persistence" "Secret leak scan"; do
      echo "| $check | PASS |"
    done
    echo
    echo "## Independent restart results"
    echo
    echo "| Service | Target StartedAt changed | Other IDs/StartedAt unchanged | Health and PAT |"
    echo "|---|---|---|---|"
    cat "$restart_table"
    echo
    echo "## Full-stack persistence"
    echo
    echo "Compose down/up retained the named PostgreSQL volume and the same user, organization, and project IDs. The original PAT continued to verify through Caddy and the running Cloud service."
    echo
    echo "## Secret leak result"
    echo
    echo "Exact-value scanning of service logs, safe command output, this evidence, Git diff, and Git status passed. No configured secret value was recorded."
    echo
    echo "No target VPS bootstrap job was executed in V3-013."
    echo
    echo "## Final verdict"
    echo
    echo "CLEAN-VM CONTROL-PLANE: PROVEN"
    echo
    echo "INDEPENDENT RESTART: PROVEN"
    echo
    echo "M1 — DEPLOYABLE CONTROL PLANE: PASSED"
  } >"$evidence_tmp"
}

run_verification() {
  local commit_sha owner_json owner_meta user_id org_id project_id role reused pat_created
  local volume_before volume_after volume_name image_file restart_table service
  local -a owner_values
  run_start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  preflight
  create_temp_state
  generate_runtime_config
  assert_service_list
  record_command "Compose validation" make dev-control-plane-validate
  record_command "Image build" make dev-control-plane-build
  record_command "Initial start" make dev-control-plane-up
  wait_all_healthy
  verify_external_health
  volume_before="$(volume_identity)"
  image_file="$tmp_dir/images.txt"
  collect_image_evidence "$image_file"

  owner_json="$tmp_dir/owner-first.json"; owner_meta="$tmp_dir/owner-first.meta"
  bootstrap_owner yes "$owner_json" "$owner_meta"
  mapfile -t owner_values <"$owner_meta"
  user_id="${owner_values[0]}"; org_id="${owner_values[1]}"; project_id="${owner_values[2]}"
  role="${owner_values[3]}"; reused="${owner_values[4]}"; pat_created="${owner_values[5]}"
  [[ "${role,,}" == "owner" && "$reused" == "false" && "$pat_created" == "true" ]] ||
    fail "first Owner metadata mismatch"
  [[ -s "$PAT_FILE" && "$(stat -c '%a' "$PAT_FILE")" == "600" ]] || fail "initial PAT file invalid"
  verify_pat "$project_id" "$user_id"
  assert_owner_reuse "$user_id" "$org_id" "$project_id"
  verify_pat "$project_id" "$user_id"

  restart_table="$tmp_dir/restarts.md"; : >"$restart_table"
  for service in "${RESTART_ORDER[@]}"; do
    assert_independent_restart "$service"
    verify_pat "$project_id" "$user_id"
    if [[ "$service" == "cloud" || "$service" == "postgres" ]]; then
      assert_owner_reuse "$user_id" "$org_id" "$project_id"
    fi
    printf '| `%s` | PASS | PASS | PASS |\n' "$service" >>"$restart_table"
  done

  capture_compose_logs
  record_command "Full stack down" make dev-control-plane-down
  [[ "$(volume_identity)" == "$volume_before" ]] || fail "PostgreSQL volume changed after down"
  record_command "Full stack up" make dev-control-plane-up
  wait_all_healthy
  verify_external_health
  volume_after="$(volume_identity)"
  [[ "$volume_after" == "$volume_before" ]] || fail "PostgreSQL volume identity changed after down/up"
  assert_owner_reuse "$user_id" "$org_id" "$project_id"
  verify_pat "$project_id" "$user_id"

  commit_sha="$(git -C "$REPO_ROOT" rev-parse HEAD)"
  volume_name="${volume_after%%|*}"
  write_evidence "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$commit_sha" "$user_id" "$org_id" "$project_id" \
    "$volume_name" "$image_file" "$restart_table"
  scan_secrets
  mv -f -- "$evidence_tmp" "$evidence_path"
  evidence_tmp=""
  echo "V3-013 clean-VM verification passed; evidence: ${evidence_path#"$REPO_ROOT/"}"
}

while (( $# > 0 )); do
  case "$1" in
    --preflight)
      [[ -z "$mode" ]] || { usage; exit 2; }
      mode="preflight"
      shift
      ;;
    --evidence)
      [[ -z "$mode" && $# -ge 2 ]] || { usage; exit 2; }
      mode="run"
      evidence_path="$2"
      shift 2
      ;;
    *) usage; exit 2 ;;
  esac
done

cd "$REPO_ROOT"
case "$mode" in
  preflight) preflight ;;
  run)
    [[ "$evidence_path" = /* ]] || evidence_path="$REPO_ROOT/$evidence_path"
    [[ "$evidence_path" == "$REPO_ROOT/docs/evidence/v3-013-clean-vm.md" ]] ||
      fail "evidence path must be docs/evidence/v3-013-clean-vm.md"
    [[ ! -e "$evidence_path" ]] || fail "evidence file already exists"
    run_verification
    ;;
  *) usage; exit 2 ;;
esac
