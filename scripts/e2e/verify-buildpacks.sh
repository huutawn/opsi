#!/usr/bin/env bash
set -euo pipefail

for tool in curl docker go htpasswd python3 sha256sum tar; do
	command -v "$tool" >/dev/null
done

suffix="$$"
registry_container="opsi-buildpack-registry-${suffix}"
postgres_container="opsi-buildpack-postgres-${suffix}"
work_dir="$(mktemp -d)"
export DOCKER_CONFIG="$work_dir/docker-config"
export OPSI_BUILDPACK_EVIDENCE_DIR="$work_dir/evidence"
mkdir -p "$DOCKER_CONFIG" "$OPSI_BUILDPACK_EVIDENCE_DIR" "$work_dir/auth" "$work_dir/bin"

cleanup() {
	docker rm -f "$registry_container" "$postgres_container" >/dev/null 2>&1 || :
	rm -rf "$work_dir"
}
trap cleanup EXIT

pack_archive="$work_dir/pack-v0.40.9-linux.tgz"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
	https://github.com/buildpacks/pack/releases/download/v0.40.9/pack-v0.40.9-linux.tgz \
	-o "$pack_archive"
echo "dc0ee1e931cf8a106d7555a01a214864f9acb60b77adf15d69b74df4404758e9  $pack_archive" | sha256sum --check
tar -xzf "$pack_archive" -C "$work_dir/bin" pack
export PATH="$work_dir/bin:$PATH"
pack version

export OPSI_TEST_REGISTRY_USERNAME="opsi-build"
export OPSI_TEST_REGISTRY_PASSWORD="opsi-buildpack-${suffix}"
htpasswd -Bbn "$OPSI_TEST_REGISTRY_USERNAME" "$OPSI_TEST_REGISTRY_PASSWORD" >"$work_dir/auth/htpasswd"
chmod 755 "$work_dir" "$work_dir/auth"
chmod 644 "$work_dir/auth/htpasswd"
registry_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
docker run -d --rm --name "$registry_container" \
	-p "127.0.0.1:${registry_port}:5000" \
	-v "$work_dir/auth:/auth:ro,Z" \
	-e REGISTRY_AUTH=htpasswd \
	-e REGISTRY_AUTH_HTPASSWD_REALM='Opsi Buildpacks Acceptance' \
	-e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd \
	registry:2 >/dev/null
export OPSI_TEST_REGISTRY_HOST="127.0.0.1:${registry_port}"
export OPSI_TEST_REGISTRY_API="http://${OPSI_TEST_REGISTRY_HOST}"
for attempt in $(seq 1 60); do
	status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' "$OPSI_TEST_REGISTRY_API/v2/" || :)"
	if [ "$status" = 401 ]; then
		break
	fi
	test "$attempt" -eq 60 || sleep 1
done
test "$status" = 401

postgres_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
docker run -d --rm --name "$postgres_container" \
	-e POSTGRES_USER=opsi -e POSTGRES_PASSWORD=opsi -e POSTGRES_DB=opsi \
	-p "127.0.0.1:${postgres_port}:5432" postgres:16 >/dev/null
for attempt in $(seq 1 60); do
	if docker exec "$postgres_container" pg_isready -U opsi -d opsi >/dev/null 2>&1; then
		break
	fi
	test "$attempt" -eq 60 || sleep 1
done
docker exec "$postgres_container" pg_isready -U opsi -d opsi >/dev/null
export OPSI_TEST_DATABASE_URL="postgres://opsi:opsi@127.0.0.1:${postgres_port}/opsi?sslmode=disable"
export OPSI_REQUIRE_POSTGRES_TESTS=1

cd cloud
go test -tags buildkitintegration -timeout 30m -count=1 -run '^TestBuildpack' -v ./internal/buildexecutor
go test -tags postgresintegration -timeout 10m -count=1 -run '^TestPostgresBuildpack' -v ./internal/buildjob
