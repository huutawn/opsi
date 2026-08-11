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

cleanup() {
	docker rm -f "$k3s_container" "$registry_container" >/dev/null 2>&1 || :
	docker network rm "$network" >/dev/null 2>&1 || :
	rm -rf "$work_dir"
}
trap cleanup EXIT

mkdir -p "$work_dir/auth" "$work_dir/bin" "$DOCKER_CONFIG"
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

docker pull nginx:1.27-alpine >/dev/null
local_image="${registry_host}/opsi/e2e:seed"
docker tag nginx:1.27-alpine "$local_image"
printf '%s' "$password" | docker login "$registry_host" --username "$username" --password-stdin >/dev/null
docker push "$local_image" >/dev/null
wrong_image="${registry_host}/opsi/wrong:seed"
docker tag nginx:1.27-alpine "$wrong_image"
docker push "$wrong_image" >/dev/null

accept='application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'
anonymous_status="$(curl -sS -o /dev/null -w '%{http_code}' -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/e2e/manifests/seed")"
wrong_status="$(curl -sS -o /dev/null -w '%{http_code}' -u "${username}:wrong" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/e2e/manifests/seed")"
headers="$work_dir/headers"
correct_status="$(curl -sS -D "$headers" -o /dev/null -w '%{http_code}' -u "${username}:${password}" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/e2e/manifests/seed")"
digest="$(awk 'BEGIN{IGNORECASE=1} /^Docker-Content-Digest:/ {gsub("\r", "", $2); print $2}' "$headers")"
digest_status="$(curl -sS -o /dev/null -w '%{http_code}' -u "${username}:${password}" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/e2e/manifests/${digest}")"
[[ "$anonymous_status" == 401 ]]
[[ "$wrong_status" == 401 ]]
[[ "$correct_status" == 200 ]]
[[ "$digest_status" == 200 ]]
[[ "${digest#sha256:}" != "$digest" ]]

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

printf '#!/usr/bin/env bash\nexec docker exec -i %q kubectl "$@"\n' "$k3s_container" >"$work_dir/bin/kubectl"
chmod 700 "$work_dir/bin/kubectl"

PATH="$work_dir/bin:$PATH" \
	OPSI_PRIVATE_REGISTRY_E2E_IMAGE="registry:5000/opsi/e2e@${digest}" \
	OPSI_PRIVATE_REGISTRY_E2E_WRONG_IMAGE="registry:5000/opsi/wrong@${digest}" \
	OPSI_PRIVATE_REGISTRY_E2E_USERNAME="$username" \
	OPSI_PRIVATE_REGISTRY_E2E_PASSWORD="$password" \
	go test -count=1 -run '^TestPrivateRegistryK3s.*Integration$' -v ./agent/internal/deploy

printf 'anonymous_pull=%s wrong_credential=%s authenticated_tag_lookup=%s digest_lookup=%s digest=%s\n' \
	"$anonymous_status" "$wrong_status" "$correct_status" "$digest_status" "$digest"
