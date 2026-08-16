#!/usr/bin/env bash
set -euo pipefail

for tool in docker curl htpasswd go python3; do
	command -v "$tool" >/dev/null
done

suffix="$$"
network="opsi-private-registry-${suffix}"
registry_container="opsi-registry-${suffix}"
k3s_container="opsi-k3s-${suffix}"
minio_container="opsi-minio-${suffix}"
cloud_postgres_container="opsi-cloud-postgres-${suffix}"
cloud_pid=""
work_dir="$(mktemp -d)"
export DOCKER_CONFIG="$work_dir/docker"
username="opsi-pull"
password="opsi-private-${suffix}"
local_image=""
generic_image=""
wrong_image=""
postgres_image=""
evidence_dir="${OPSI_K3S_EVIDENCE_DIR:-$PWD/.tmp/evidence/p07b3b1-postgres-binding-$(date -u +%Y%m%dT%H%M%SZ)}"
backup_evidence_dir="${OPSI_P07B3C1_EVIDENCE_DIR:-$PWD/.tmp/evidence/p07b3c1-postgres-backup-$(date -u +%Y%m%dT%H%M%SZ)}"
restore_evidence_dir="${OPSI_P07B3C2A_EVIDENCE_DIR:-$PWD/.tmp/evidence/p07b3c2a-postgres-restore-$(date -u +%Y%m%dT%H%M%SZ)}"
minio_image="minio/minio@sha256:f6efb212cad3b62f78ca02339f16d8bc28d5bb2fbe792dfc21225c6037d2af8b"
minio_access="opsi-minio-${suffix}"
minio_secret="opsi-minio-secret-${suffix}"
minio_bucket="opsi-p07b3c1"

cleanup() {
	status=$?
	trap - EXIT
	cleanup_status=0
	if [ -n "$cloud_pid" ]; then
		kill "$cloud_pid" >/dev/null 2>&1 || :
		wait "$cloud_pid" >/dev/null 2>&1 || :
	fi
	for container in "$k3s_container" "$registry_container" "$minio_container" "$cloud_postgres_container"; do
		if docker inspect "$container" >/dev/null 2>&1; then
			docker rm -f "$container" >/dev/null 2>&1 || cleanup_status=1
		fi
	done
	for image in "$local_image" "$generic_image" "$wrong_image" "$postgres_image" "$minio_image"; do
		if [ -n "$image" ] && docker image inspect "$image" >/dev/null 2>&1; then
			docker image rm -f "$image" >/dev/null 2>&1 || cleanup_status=1
		fi
	done
	if docker network inspect "$network" >/dev/null 2>&1; then
		docker network rm "$network" >/dev/null 2>&1 || cleanup_status=1
	fi
	if [ "$status" -ne 0 ] && [ -f "$work_dir/cloud.log" ]; then
		cp "$work_dir/cloud.log" "$restore_evidence_dir/cloud.log" 2>/dev/null || :
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

mkdir -p "$work_dir/auth" "$work_dir/bin" "$work_dir/fixture" "$work_dir/postgres-fixture" "$DOCKER_CONFIG" "$evidence_dir" "$backup_evidence_dir" "$restore_evidence_dir"
htpasswd -Bbn "$username" "$password" >"$work_dir/auth/htpasswd"
chmod 755 "$work_dir" "$work_dir/auth"
chmod 644 "$work_dir/auth/htpasswd"
docker network create "$network" >/dev/null
registry_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
minio_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
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

docker pull "$minio_image" >/dev/null
docker run -d --name "$minio_container" --network "$network" --network-alias minio \
	-p "127.0.0.1:${minio_port}:9000" \
	-e MINIO_ROOT_USER="$minio_access" \
	-e MINIO_ROOT_PASSWORD="$minio_secret" \
	"$minio_image" server /data --address :9000 >/dev/null
for _ in $(seq 1 60); do
	if curl -fsS "http://127.0.0.1:${minio_port}/minio/health/ready" >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
curl -fsS "http://127.0.0.1:${minio_port}/minio/health/ready" >/dev/null
OPSI_E2E_MINIO_ENDPOINT="http://127.0.0.1:${minio_port}" \
	OPSI_E2E_MINIO_ACCESS_KEY="$minio_access" \
	OPSI_E2E_MINIO_SECRET_KEY="$minio_secret" \
	OPSI_E2E_MINIO_BUCKET="$minio_bucket" \
	go test -count=1 -run '^TestS3StoreRealMinIO$' -v ./agent/internal/backup
printf 's3_compatible_backup_store=PASS\n'

docker run -d --name "$cloud_postgres_container" --network "$network" \
	-e POSTGRES_USER=opsi -e POSTGRES_PASSWORD=opsi -e POSTGRES_DB=opsi \
	-p 127.0.0.1::5432 postgres:16 >/dev/null
for _ in $(seq 1 60); do
	if docker exec "$cloud_postgres_container" pg_isready -U opsi -d opsi >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
docker exec "$cloud_postgres_container" pg_isready -U opsi -d opsi >/dev/null
cloud_postgres_port="$(docker port "$cloud_postgres_container" 5432/tcp | awk -F: '{print $2}')"
cloud_database_url="postgres://opsi:opsi@127.0.0.1:${cloud_postgres_port}/opsi?sslmode=disable"
cloud_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
cloud_url="http://127.0.0.1:${cloud_port}"
printf '%s' "$minio_access" >"$work_dir/minio-access"
printf '%s' "$minio_secret" >"$work_dir/minio-secret"
chmod 600 "$work_dir/minio-access" "$work_dir/minio-secret"
python3 - "$work_dir/cloud.json" "$cloud_database_url" "$cloud_url" "$work_dir/minio-access" "$work_dir/minio-secret" "http://127.0.0.1:${minio_port}" "$minio_bucket" <<'PY'
import json, sys
path, database_url, public_url, access_file, secret_file, endpoint, bucket = sys.argv[1:]
with open(path, "w", encoding="utf-8") as handle:
    json.dump({
        "database_url": database_url,
        "public_base_url": public_url,
        "bootstrap_secret_key": "p07b3c2a-bootstrap-secret-key-0001",
        "backup_store": {
            "id": "minio-p07b3c2a",
            "endpoint": endpoint,
            "bucket": bucket,
            "region": "us-east-1",
            "access_key_file": access_file,
            "secret_key_file": secret_file,
            "allow_insecure": True,
        },
    }, handle)
PY
chmod 600 "$work_dir/cloud.json"
(cd cloud && CGO_ENABLED=0 go build -trimpath -o "$work_dir/bin/opsi-cloud" ./cmd/opsi-cloud)
"$work_dir/bin/opsi-cloud" admin bootstrap-owner --config "$work_dir/cloud.json" \
	--email p07b3c2a@example.test --org-name P07B3C2A --org-slug p07b3c2a \
	--project-name P07B3C2A --project-slug p07b3c2a --pat-output-file "$work_dir/owner.pat" --json >"$work_dir/admin.json"
cloud_project_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["project_id"])' "$work_dir/admin.json")"
cloud_pat="$(<"$work_dir/owner.pat")"
"$work_dir/bin/opsi-cloud" --addr "127.0.0.1:${cloud_port}" --config "$work_dir/cloud.json" >"$work_dir/cloud.log" 2>&1 &
cloud_pid=$!
for _ in $(seq 1 60); do
	if curl -fsS "$cloud_url/health" >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
curl -fsS "$cloud_url/health" >/dev/null
cloud_environment_id="$(docker exec "$cloud_postgres_container" psql -U opsi -d opsi -qAt -c "SELECT id FROM environments WHERE project_id='${cloud_project_id}' ORDER BY created_at LIMIT 1")"
node_response="$(curl -sS -w '\n%{http_code}' -X POST "$cloud_url/api/projects/$cloud_project_id/nodes" \
	-H "Authorization: Bearer $cloud_pat" -H 'Content-Type: application/json' \
	-H 'Idempotency-Key: p07b3c2a-node' -H 'X-Request-ID: req-p07b3c2a-node' \
	--data '{"name":"p07b3c2a-node","role":"server","status":"healthy","public_host":"203.0.113.10"}')"
node_status="${node_response##*$'\n'}"
node_json="${node_response%$'\n'*}"
[ "$node_status" = 201 ] || { echo "node registration status=$node_status body=$node_json" >&2; exit 1; }
cloud_node_id="$(python3 -c 'import json,sys; v=json.load(sys.stdin); print(v.get("id") or v["node"]["id"])' <<<"$node_json")"
agent_response="$(curl -sS -w '\n%{http_code}' -X POST "$cloud_url/api/projects/$cloud_project_id/agents" \
	-H "Authorization: Bearer $cloud_pat" -H 'Content-Type: application/json' \
	-H 'Idempotency-Key: p07b3c2a-agent' -H 'X-Request-ID: req-p07b3c2a-agent' \
	--data "{\"node_id\":\"$cloud_node_id\",\"public_key_fingerprint\":\"sha256:p07b3c2a\",\"version\":\"test\",\"capabilities\":{\"managed_resources\":true,\"postgres_logical_backup\":true,\"postgres_logical_restore\":true},\"agent_endpoint\":\"203.0.113.10\",\"agent_port\":9443,\"agent_tls_server_name\":\"203.0.113.10\",\"agent_cert_sha256\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}")"
agent_status="${agent_response##*$'\n'}"
agent_json="${agent_response%$'\n'*}"
[ "$agent_status" = 201 ] || { echo "agent registration status=$agent_status body=$agent_json" >&2; exit 1; }
cloud_agent_id="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["agent"]["id"])' <<<"$agent_json")"
cloud_agent_token="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["agent_token"])' <<<"$agent_json")"
curl -fsS -X POST "$cloud_url/v1/agents/$cloud_node_id/heartbeat?project_id=$cloud_project_id" \
	-H "Authorization: Bearer $cloud_agent_token" -H 'Content-Type: application/json' \
	--data '{"version":"test","k3s_status":"ready","node_ready":true,"capacity":{"cpu_cores":4,"memory_mb":8192,"disk_total_gb":80},"capabilities":{"managed_resources":true,"postgres_logical_backup":true,"postgres_logical_restore":true}}' >/dev/null
printf 'cloud_restore_authority=PASS\n'

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$work_dir/fixture/p07b2-application" ./agent/integration/fixtures/p07b2-application
local_image="${registry_host}/opsi/p07b2-acceptance:fixture-${suffix}"
docker build -q -f agent/integration/fixtures/p07b2-application/Dockerfile -t "$local_image" "$work_dir/fixture" >/dev/null
printf '%s' "$password" | docker login "$registry_host" --username "$username" --password-stdin >/dev/null
docker push "$local_image" >/dev/null || { echo 'fixture push failed' >&2; exit 1; }
(cd cloud && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$work_dir/postgres-fixture/p07b3b1-application" ./integration/fixtures/p07b3b1-application)
postgres_image="${registry_host}/opsi/p07b3b1-acceptance:fixture-${suffix}"
docker build -q -f cloud/integration/fixtures/p07b3b1-application/Dockerfile -t "$postgres_image" "$work_dir/postgres-fixture" >/dev/null
docker push "$postgres_image" >/dev/null || { echo 'PostgreSQL fixture push failed' >&2; exit 1; }
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
postgres_headers="$work_dir/postgres-headers"
http_status -D "$postgres_headers" -u "${username}:${password}" -H "Accept: ${accept}" "http://${registry_host}/v2/opsi/p07b3b1-acceptance/manifests/fixture-${suffix}"
postgres_status="$status"
postgres_digest="$(awk 'BEGIN{IGNORECASE=1} /^Docker-Content-Digest:/ {gsub("\r", "", $2); print $2}' "$postgres_headers")"
[[ "$anonymous_status" == 401 ]]
[[ "$wrong_status" == 401 ]]
[[ "$correct_status" == 200 ]]
[[ "$digest_status" == 200 ]]
[[ "$generic_status" == 200 ]]
[[ "$postgres_status" == 200 ]]
[[ "${digest#sha256:}" != "$digest" ]]
[[ "${generic_digest#sha256:}" != "$generic_digest" ]]
[[ "${postgres_digest#sha256:}" != "$postgres_digest" ]]
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
	if docker exec "$k3s_container" kubectl get nodes -o name 2>/dev/null | rg -q '^node/' &&
		docker exec "$k3s_container" kubectl get deployment coredns -n kube-system >/dev/null 2>&1 &&
		docker exec "$k3s_container" kubectl get deployment local-path-provisioner -n kube-system >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
docker exec "$k3s_container" kubectl get nodes -o name | rg -q '^node/'
docker exec "$k3s_container" kubectl wait --for=condition=Ready node --all --timeout=4m >/dev/null
docker exec "$k3s_container" kubectl wait --for=condition=Available deployment/coredns -n kube-system --timeout=4m >/dev/null
docker exec "$k3s_container" kubectl wait --for=condition=Available deployment/local-path-provisioner -n kube-system --timeout=4m >/dev/null
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

export PATH="$work_dir/bin:$PATH" OPSI_E2E_K3S_POSTGRES=1 OPSI_E2E_K3S_POSTGRES_BINDING=1 OPSI_E2E_K3S_POSTGRES_BACKUP=1 OPSI_E2E_K3S_NATS=1 OPSI_E2E_K3S_VALKEY=1 \
	OPSI_E2E_MINIO_ENDPOINT="http://127.0.0.1:${minio_port}" OPSI_E2E_MINIO_ACCESS_KEY="$minio_access" OPSI_E2E_MINIO_SECRET_KEY="$minio_secret" OPSI_E2E_MINIO_BUCKET="$minio_bucket" \
	OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE="registry:5000/opsi/p07b3b1-acceptance@${postgres_digest}" OPSI_PRIVATE_REGISTRY_E2E_USERNAME="$username" OPSI_PRIVATE_REGISTRY_E2E_PASSWORD="$password" \
	OPSI_K3S_EVIDENCE_DIR="$evidence_dir" OPSI_P07B3C1_EVIDENCE_DIR="$backup_evidence_dir" OPSI_P07B3C2A_EVIDENCE_DIR="$restore_evidence_dir" \
	OPSI_P07B3C2A_CLOUD_URL="$cloud_url" OPSI_P07B3C2A_CLOUD_PROJECT_ID="$cloud_project_id" OPSI_P07B3C2A_CLOUD_ENVIRONMENT_ID="$cloud_environment_id" \
	OPSI_P07B3C2A_CLOUD_NODE_ID="$cloud_node_id" OPSI_P07B3C2A_CLOUD_AGENT_ID="$cloud_agent_id" OPSI_P07B3C2A_CLOUD_PAT="$cloud_pat" OPSI_P07B3C2A_CLOUD_AGENT_TOKEN="$cloud_agent_token" OPSI_P07B3C2A_POSTGRES_CONTAINER="$cloud_postgres_container"
if [ "${OPSI_P07B3C2A_ONLY:-}" = "1" ]; then
	go test -count=1 -run '^TestManagedResourceRealK3sPostgresLogicalBackup$' -v ./agent/internal/svcatalog
else
	go test -count=1 -run '^TestManagedResourceRealK3s(NATS|Valkey)$' -v ./agent/internal/svcatalog
	go test -count=1 -run '^TestManagedResourceRealK3sPostgresLogicalBackup$' -v ./agent/internal/svcatalog
	go test -count=1 -run '^TestManagedResourceRealK3sPostgres(Persistence|ApplicationBinding)$' -v ./agent/internal/svcatalog
fi

authority_file="$restore_evidence_dir/restore-authority.json"
manifest_file="$restore_evidence_dir/supplemental-gate-results.json"
[ -s "$authority_file" ] || { echo 'supplemental authority evidence missing' >&2; exit 1; }
[ -s "$manifest_file" ] || { echo 'supplemental gate manifest missing' >&2; exit 1; }
review_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["review_id"])' "$authority_file")"
restore_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["restore_id"])' "$authority_file")"
before_file="$work_dir/authority-before.json"
cp "$authority_file" "$before_file"
kill "$cloud_pid" >/dev/null 2>&1
wait "$cloud_pid" >/dev/null 2>&1 || :
"$work_dir/bin/opsi-cloud" --addr "127.0.0.1:${cloud_port}" --config "$work_dir/cloud.json" >"$work_dir/cloud.log" 2>&1 &
cloud_pid=$!
for _ in $(seq 1 60); do
	if curl -fsS "$cloud_url/health" >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
curl -fsS "$cloud_url/health" >/dev/null
curl -fsS -H "Authorization: Bearer $cloud_pat" "$cloud_url/api/projects/$cloud_project_id/restore-reviews/$review_id" >"$work_dir/review-after.json"
curl -fsS -H "Authorization: Bearer $cloud_pat" "$cloud_url/api/projects/$cloud_project_id/restores/$restore_id" >"$work_dir/restore-after.json"
succeeded_restores="$(docker exec "$cloud_postgres_container" psql -U opsi -d opsi -qAt -c "SELECT count(*) FROM restores WHERE project_id='${cloud_project_id}' AND id='${restore_id}' AND lifecycle='succeeded'")"
[ "$succeeded_restores" = 1 ] || { echo "expected one succeeded Restore authority, got $succeeded_restores" >&2; exit 1; }
python3 - "$before_file" "$work_dir/review-after.json" "$work_dir/restore-after.json" "$manifest_file" <<'PY'
import json, sys
before = json.load(open(sys.argv[1], encoding="utf-8"))
review = json.load(open(sys.argv[2], encoding="utf-8"))
restore = json.load(open(sys.argv[3], encoding="utf-8"))
manifest_path = sys.argv[4]
review = review.get("review", review)
restore = restore.get("restore", restore)
before_review = before["review"]
before_restore = before["restore"]
review_fields = ("id", "backup_id", "target_resource_id", "target_spec_hash", "target_storage_hash", "pristine_evidence_hash", "lifecycle")
restore_fields = ("id", "backup_id", "target_resource_id", "artifact_sha256", "artifact_size", "lifecycle", "failure_code")
if any(review.get(k) != before_review.get(k) for k in review_fields):
    raise SystemExit("Cloud review authority changed after restart")
if any(restore.get(k) != before_restore.get(k) for k in restore_fields):
    raise SystemExit("Cloud restore authority changed after restart")
manifest = json.load(open(manifest_path, encoding="utf-8"))
manifest["cloud_review_durability"] = "PASS"
manifest["cloud_restore_durability"] = "PASS"
manifest["succeeded_immutability"] = "PASS"
with open(manifest_path, "w", encoding="utf-8") as handle:
    json.dump(manifest, handle, indent=2)
    handle.write("\n")
PY
python3 - "$manifest_file" <<'PY'
import json, sys
manifest = json.load(open(sys.argv[1], encoding="utf-8"))
required = ("active_binding", "non_empty_target", "same_resource", "transactional_rollback", "agent_pre_mutation_recovery", "cloud_review_durability", "cloud_restore_durability", "succeeded_immutability")
bad = [key for key in required if manifest.get(key) != "PASS"]
if bad:
    raise SystemExit("mandatory supplemental gates not PASS: " + ", ".join(bad))
if manifest.get("agent_in_transaction_recovery") not in ("PASS", "NOT_EXERCISED_REAL"):
    raise SystemExit("invalid agent_in_transaction_recovery result")
PY
printf 'postgres_application_binding_tests=PASS evidence=%s\n' "$evidence_dir"
printf 'postgres_logical_backup_test=PASS evidence=%s\n' "$backup_evidence_dir"
printf 'postgres_restore_test=PASS evidence=%s\n' "$restore_evidence_dir"

printf 'fixture_reference=registry:5000/opsi/p07b2-acceptance@%s anonymous_pull=%s wrong_credential=%s authenticated_tag_lookup=%s digest_lookup=%s\n' \
	"$digest" "$anonymous_status" "$wrong_status" "$correct_status" "$digest_status"
printf 'postgres_fixture_reference=registry:5000/opsi/p07b3b1-acceptance@%s\n' "$postgres_digest"
