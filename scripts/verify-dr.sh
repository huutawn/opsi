#!/usr/bin/env bash
set -euo pipefail

IMAGE="${OPSI_DR_POSTGRES_IMAGE:-postgres:16-alpine}"
SRC="opsi-dr-src-$$"
DST="opsi-dr-dst-$$"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cleanup() {
  docker stop "$SRC" "$DST" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --rm --name "$SRC" -e POSTGRES_PASSWORD=opsi -e POSTGRES_DB=opsi -p 0:5432 "$IMAGE" >/dev/null
docker run -d --rm --name "$DST" -e POSTGRES_PASSWORD=opsi -e POSTGRES_DB=opsi -p 0:5432 "$IMAGE" >/dev/null

src_port="$(docker port "$SRC" 5432/tcp | sed -n '1s/.*://p')"
dst_port="$(docker port "$DST" 5432/tcp | sed -n '1s/.*://p')"
for port in "$src_port" "$dst_port"; do
  for _ in $(seq 1 60); do
    if docker exec "$SRC" pg_isready -U postgres >/dev/null 2>&1 && docker exec "$DST" pg_isready -U postgres >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
done

cd "$ROOT/cloud"
env GOCACHE="${GOCACHE:-/tmp/opsi-go-cache}" GOTOOLCHAIN="${GOTOOLCHAIN:-local}" \
  OPSI_DR_SOURCE_DATABASE_URL="postgres://postgres:opsi@127.0.0.1:${src_port}/opsi?sslmode=disable" \
  OPSI_DR_RESTORE_DATABASE_URL="postgres://postgres:opsi@127.0.0.1:${dst_port}/opsi?sslmode=disable" \
  OPSI_DR_SOURCE_BACKUP_URL="postgres://postgres:opsi@127.0.0.1:5432/opsi?sslmode=disable" \
  OPSI_DR_RESTORE_BACKUP_URL="postgres://postgres:opsi@127.0.0.1:5432/opsi?sslmode=disable" \
  OPSI_DR_PGDUMP_CMD="docker exec $SRC pg_dump" \
  OPSI_DR_PGRESTORE_CMD="docker exec -i $DST pg_restore" \
  OPSI_DR_KEY_MATERIAL_CONFIRMED=1 \
  go test ./internal/dr -run TestCloudBackupRestoreDRProof -v

cd "$ROOT/agent"
env GOCACHE="${GOCACHE:-/tmp/opsi-go-cache}" GOTOOLCHAIN="${GOTOOLCHAIN:-local}" go test ./internal/dr
