#!/usr/bin/env bash
set -euo pipefail

for tool in docker curl htpasswd go python3; do
	command -v "$tool" >/dev/null
done

suffix="$$"
network="opsi-private-registry-${suffix}"
registry_container="opsi-registry-${suffix}"
k3s_container="opsi-k3s-${suffix}"
work_dir="$(mktemp -d)"
export DOCKER_CONFIG="$work_dir/docker"
username="opsi-pull"
password="opsi-private-${suffix}"
local_image=""
generic_image=""
wrong_image=""

cleanup() {
	status=$?
	trap - EXIT
	cleanup_status=0
	for container in "$k3s_container" "$registry_container"; do
		if docker inspect "$container" >/dev/null 2>&1; then
			docker rm -f "$container" >/dev/null 2>&1 || cleanup_status=1
		fi
	done
	for image in "$local_image" "$generic_image" "$wrong_image"; do
		if [ -n "$image" ] && docker image inspect "$image" >/dev/null 2>&1; then
			docker image rm -f "$image" >/dev/null 2>&1 || cleanup_status=1
		fi
	done
	if docker network inspect "$network" >/dev/null 2>&1; then
		docker network rm "$network" >/dev/null 2>&1 || cleanup_status=1
	fi
	rm -rf "$work_dir"
	if [ "$cleanup_status" -eq 0 ] && [ ! -e "$work_dir" ]; then
		printf 'cleanup=PASS\n'
	elif [ "$status" -eq 0 ]; then
		status=1
		printf 'cleanup=FAIL\n' >&2
	fi
	exit "$status"
}
trap cleanup EXIT

mkdir -p "$work_dir/auth" "$work_dir/bin" "$work_dir/fixture" "$DOCKER_CONFIG"
htpasswd -Bbn "$username" "$password" >"$work_dir/auth/htpasswd"
chmod 755 "$work_dir" "$work_dir/auth"
chmod 644 "$work_dir/auth/htpasswd"
docker network create "$network" >/dev/null
registry_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
docker run -d --name "$registry_container" --network "$network" --network-alias registry \
	-p "127.0.0.1:${registry_port}:5000" \
	-v "$work_dir/auth:/auth:ro,Z" \
	-e REGISTRY_AUTH=htpasswd \
	-e REGISTRY_AUTH_HTPASSWD_REALM='Opsi private registry' \
	-e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd \
	registry:2 >/dev/null

registry_host="127.0.0.1:${registry_port}"
status=""
for _ in $(seq 1 60); do
	status="$(curl -sS -o /dev/null -w '%{http_code}' "http://${registry_host}/v2/" || :)"
	if [ "$status" = 401 ]; then
		break
	fi
	sleep 1
done
[[ "$status" == 401 ]]

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$work_dir/fixture/p07b2-application" ./agent/integration/fixtures/p07b2-application
local_image="${registry_host}/opsi/p07b2-acceptance:fixture-${suffix}"
docker build -q -f agent/integration/fixtures/p07b2-application/Dockerfile -t "$local_image" "$work_dir/fixture" >/dev/null
printf '%s' "$password" | docker login "$registry_host" --username "$username" --password-stdin >/dev/null
docker push "$local_image" >/dev/null || { echo 'fixture push failed' >&2; exit 1; }
docker pull nginx:1.27-alpine >/dev/null
generic_image="${registry_host}/opsi/e2e:seed"
docker tag nginx:1.27-alpine "$generic_image"
docker push "$generic_image" >/dev/null || { echo 'generic image push failed' >&2; exit 1; }
wrong_image="${registry_host}/opsi/wrong:seed"
docker tag nginx:1.27-alpine "$wrong_image"
docker push "$wrong_image" >/dev/null || { echo 'wrong-credential image push failed' >&2; exit 1; }

accept='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'
http_status() {
	status="$(curl -sS -o /dev/null -w '%{http_code}' "$@")"
}
http_status -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/p07b2-acceptance/manifests/fixture-${suffix}"
anonymous_status="$status"
printf 'anonymous_manifest_status=%s\n' "$anonymous_status"
http_status -u "${username}:wrong" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/p07b2-acceptance/manifests/fixture-${suffix}"
wrong_status="$status"
printf 'wrong_manifest_status=%s\n' "$wrong_status"
headers="$work_dir/headers"
http_status -D "$headers" -u "${username}:${password}" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/p07b2-acceptance/manifests/fixture-${suffix}"
correct_status="$status"
printf 'authenticated_manifest_status=%s\n' "$correct_status"
digest="$(awk 'BEGIN{IGNORECASE=1} /^Docker-Content-Digest:/ {gsub("\r", "", $2); print $2}' "$headers")"
http_status -u "${username}:${password}" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/p07b2-acceptance/manifests/${digest}"
digest_status="$status"
generic_headers="$work_dir/generic-headers"
http_status -D "$generic_headers" -u "${username}:${password}" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/e2e/manifests/seed"
generic_status="$status"
generic_digest="$(awk 'BEGIN{IGNORECASE=1} /^Docker-Content-Digest:/ {gsub("\r", "", $2); print $2}' "$generic_headers")"
[[ "$anonymous_status" == 401 ]]
[[ "$wrong_status" == 401 ]]
[[ "$correct_status" == 200 ]]
[[ "$digest_status" == 200 ]]
[[ "$generic_status" == 200 ]]
[[ "${digest#sha256:}" != "$digest" ]]
[[ "${generic_digest#sha256:}" != "$generic_digest" ]]
printf 'registry_manifest_checks=PASS\n'

printf '%s\n' \
	'mirrors:' \
	'  "registry:5000":' \
	'    endpoint:' \
	'      - "http://registry:5000"' >"$work_dir/registries.yaml"

docker run -d --privileged --name "$k3s_container" --network "$network" \
	-v "$work_dir/registries.yaml:/etc/rancher/k3s/registries.yaml:ro,Z" \
	rancher/k3s:v1.33.1-k3s1 server --disable traefik --disable servicelb >/dev/null
for _ in $(seq 1 120); do
	if docker exec "$k3s_container" kubectl get nodes >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
docker exec "$k3s_container" kubectl get nodes >/dev/null
printf 'k3s_registry_cluster=PASS\n'

printf '#!/usr/bin/env bash\nexec docker exec -i %q kubectl "$@"\n' "$k3s_container" >"$work_dir/bin/kubectl"
chmod 700 "$work_dir/bin/kubectl"

PATH="$work_dir/bin:$PATH" \
	OPSI_PRIVATE_REGISTRY_E2E_IMAGE="registry:5000/opsi/e2e@${generic_digest}" \
	OPSI_P07B2_ACCEPTANCE_E2E_IMAGE="registry:5000/opsi/p07b2-acceptance@${digest}" \
	OPSI_PRIVATE_REGISTRY_E2E_WRONG_IMAGE="registry:5000/opsi/wrong@${generic_digest}" \
	OPSI_PRIVATE_REGISTRY_E2E_USERNAME="$username" \
	OPSI_PRIVATE_REGISTRY_E2E_PASSWORD="$password" \
	go test -count=1 -run '^Test(PrivateRegistryK3s.*|P07B2AcceptanceFixtureImagePull)Integration$' -v ./agent/internal/deploy
printf 'registry_pull_tests=PASS\n'

printf 'fixture_reference=registry:5000/opsi/p07b2-acceptance@%s anonymous_pull=%s wrong_credential=%s authenticated_tag_lookup=%s digest_lookup=%s\n' \
	"$digest" "$anonymous_status" "$wrong_status" "$correct_status" "$digest_status"
