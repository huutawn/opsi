#!/usr/bin/env bash
set -euo pipefail

ROOT="${OPSI_STAGING_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
DEPLOY="$ROOT/deploy/staging-control-plane"
HOST="${OPSI_STAGING_DIAGNOSTIC_HOST:-127.0.0.1}"
HTTP_PORT="${OPSI_STAGING_DIAGNOSTIC_HTTP_PORT:-18080}"
HTTPS_PORT="${OPSI_STAGING_DIAGNOSTIC_HTTPS_PORT:-18443}"
PUBLIC_HOST="${OPSI_STAGING_DIAGNOSTIC_PUBLIC_HOST:-opsidev.site}"
CA_FILE="${OPSI_STAGING_ORIGIN_CA_FILE:-}"
PROXY="opsi-staging-reverse-proxy-1"
COMPOSE=(docker compose --env-file "$DEPLOY/.env" -f "$DEPLOY/compose.yaml")
if [[ -n "${OPSI_STAGING_COMPOSE_EXTRA_FILE:-}" ]]; then
  COMPOSE+=( -f "$OPSI_STAGING_COMPOSE_EXTRA_FILE" )
fi

if [[ -z "$CA_FILE" || ! -s "$CA_FILE" ]]; then
  echo "OPSI_STAGING_ORIGIN_CA_FILE must reference the approved non-empty origin CA bundle" >&2
  exit 2
fi
if [[ -n "$("${COMPOSE[@]}" ps -q)" ]]; then
  echo "staging Compose project must be stopped before the isolated smoke" >&2
  exit 1
fi

cleanup() {
  env \
    OPSI_STAGING_BIND_ADDRESS="$HOST" \
    OPSI_STAGING_HTTP_PORT="$HTTP_PORT" \
    OPSI_STAGING_HTTPS_PORT="$HTTPS_PORT" \
    "${COMPOSE[@]}" down >/dev/null
}
trap cleanup EXIT

env \
  OPSI_STAGING_BIND_ADDRESS="$HOST" \
  OPSI_STAGING_HTTP_PORT="$HTTP_PORT" \
  OPSI_STAGING_HTTPS_PORT="$HTTPS_PORT" \
  "${COMPOSE[@]}" up -d >/dev/null

services=(postgres cloud bootstrap-worker reverse-proxy)
for _ in $(seq 1 60); do
  all_healthy=true
  for service in "${services[@]}"; do
    container="$(${COMPOSE[@]} ps -q "$service")"
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container")"
    if [[ "$health" == "unhealthy" || "$health" == "exited" || "$health" == "dead" ]]; then
      echo "$service entered $health during isolated staging smoke" >&2
      exit 1
    fi
    [[ "$health" == "healthy" ]] || all_healthy=false
  done
  if [[ "$all_healthy" == "true" ]]; then
    break
  fi
  sleep 2
done

for service in "${services[@]}"; do
  container="$(${COMPOSE[@]} ps -q "$service")"
  health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container")"
  [[ "$health" == "healthy" ]] || { echo "$service did not become healthy" >&2; exit 1; }
done

raw_health="$(docker exec -i "$PROXY" sh -c 'printf "GET /health HTTP/1.1\r\nHost: 127.0.0.1:8080\r\nConnection: close\r\n\r\n" | nc -w 3 127.0.0.1 8080')"
grep -q '^HTTP/1.1 200 ' <<<"$raw_health"
if grep -qi '^Location:' <<<"$raw_health"; then
  echo "container-local health unexpectedly redirects" >&2
  exit 1
fi

public_health_headers="$(curl --silent --show-error --max-redirs 0 --resolve "$PUBLIC_HOST:$HTTP_PORT:$HOST" --dump-header - --output /dev/null "http://$PUBLIC_HOST:$HTTP_PORT/health")"
grep -q '^HTTP/1.1 308 ' <<<"$public_health_headers"
grep -qi "^Location: https://$PUBLIC_HOST/health" <<<"$public_health_headers"

public_root_headers="$(curl --silent --show-error --max-redirs 0 --resolve "$PUBLIC_HOST:$HTTP_PORT:$HOST" --dump-header - --output /dev/null "http://$PUBLIC_HOST:$HTTP_PORT/")"
grep -q '^HTTP/1.1 308 ' <<<"$public_root_headers"
grep -qi "^Location: https://$PUBLIC_HOST/" <<<"$public_root_headers"

curl --fail --silent --show-error \
  --cacert "$CA_FILE" \
  --resolve "$PUBLIC_HOST:$HTTPS_PORT:$HOST" \
  "https://$PUBLIC_HOST:$HTTPS_PORT/health" >/dev/null

protected_paths=(
  "/internal"
  "/internal/"
  "/internal/bootstrap-worker/lease?wait=1"
  "/api/internal"
  "/api/internal/worker/"
  "/metrics"
  "/metrics/"
  "/%69nternal/bootstrap-worker/lease"
  "/api/%69nternal/alerts"
  "/public/../internal/bootstrap-worker/lease"
)
for path in "${protected_paths[@]}"; do
  status="$(curl --silent --show-error --path-as-is --output /dev/null --write-out '%{http_code}' \
    --cacert "$CA_FILE" \
    --resolve "$PUBLIC_HOST:$HTTPS_PORT:$HOST" \
    "https://$PUBLIC_HOST:$HTTPS_PORT$path")"
  [[ "$status" == "404" ]] || { echo "protected path returned $status: $path" >&2; exit 1; }
done

user="$(docker inspect --format '{{.Config.User}}' "$PROXY")"
readonly="$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$PROXY")"
security="$(docker inspect --format '{{json .HostConfig.SecurityOpt}}' "$PROXY")"
cap_add="$(docker inspect --format '{{json .HostConfig.CapAdd}}' "$PROXY")"
cap_drop="$(docker inspect --format '{{json .HostConfig.CapDrop}}' "$PROXY")"
[[ "$user" == "1000:1000" ]]
[[ "$readonly" == "true" ]]
grep -q 'no-new-privileges:true' <<<"$security"
[[ "$cap_add" == '["CAP_NET_BIND_SERVICE"]' ]]
[[ "$cap_drop" == '["ALL"]' ]]

if "${COMPOSE[@]}" logs --no-color 2>&1 | grep -Eiq 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|github_pat_|ghp_|ghs_|Authorization:[[:space:]]*Bearer|EXAMPLE_SECRET|CHANGE_ME|REPLACE_WITH_'; then
  echo "staging logs contain a forbidden secret or placeholder marker" >&2
  exit 1
fi

echo "isolated staging Caddy smoke passed on $HOST:$HTTP_PORT and $HOST:$HTTPS_PORT"
