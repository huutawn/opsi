#!/usr/bin/env bash
set -euo pipefail

NS="opsi-proj-219cc5584acc1-env-a55c5984ddddf2-be11c2ed7a"

get_cgroup_cpu_stat() {
  local container_id="$1"
  if [[ -z "$container_id" || "$container_id" == "null" ]]; then
    echo "usage_usec 0 nr_periods 0 nr_throttled 0 throttled_usec 0"
    return
  fi
  local scope_path
  scope_path=$(find /sys/fs/cgroup/kubepods.slice -name "*${container_id}*.scope" 2>/dev/null | head -n 1)
  if [[ -n "$scope_path" && -f "$scope_path/cpu.stat" ]]; then
    cat "$scope_path/cpu.stat"
  else
    echo "usage_usec 0 nr_periods 0 nr_throttled 0 throttled_usec 0"
  fi
}

echo "[OBSERVER_START] $(date -u +"%Y-%m-%dT%H:%M:%SZ")"

# Identify container IDs via jq
containers_json=$(crictl ps -o json)
FE_CID=$(echo "$containers_json" | jq -r '.containers[] | select(.labels["io.kubernetes.pod.namespace"]=="'"$NS"'" and (.labels["io.kubernetes.pod.name"] | startswith("opsi-learn-asp-net-tcip"))) | .id' | head -n 1)
BE_CID=$(echo "$containers_json" | jq -r '.containers[] | select(.labels["io.kubernetes.pod.namespace"]=="'"$NS"'" and (.labels["io.kubernetes.pod.name"] | startswith("opsi-learn-asp-net-be"))) | .id' | head -n 1)
REDIS_CID=$(echo "$containers_json" | jq -r '.containers[] | select(.labels["io.kubernetes.pod.namespace"]=="'"$NS"'" and (.labels["io.kubernetes.pod.name"] | startswith("omr-res-780136fe1c5551"))) | .id' | head -n 1)
PG_CID=$(echo "$containers_json" | jq -r '.containers[] | select(.labels["io.kubernetes.pod.namespace"]=="'"$NS"'" and (.labels["io.kubernetes.pod.name"] | startswith("omr-res-dfd2875f81722d"))) | .id' | head -n 1)
TRAEFIK_CID=$(echo "$containers_json" | jq -r '.containers[] | select(.metadata.name=="traefik") | .id' | head -n 1)

echo "[CONTAINER_MAP] FE=${FE_CID} BE=${BE_CID} REDIS=${REDIS_CID} PG=${PG_CID} TRAEFIK=${TRAEFIK_CID}"

# Loop every 1 second until killed
while true; do
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

  # Memory & Swap
  mem_free=$(free -b | awk '/^Mem:/{printf "{\"total\":%s,\"used\":%s,\"free\":%s,\"shared\":%s,\"buff_cache\":%s,\"available\":%s}", $2,$3,$4,$5,$6,$7}')
  swap_free=$(free -b | awk '/^Swap:/{printf "{\"total\":%s,\"used\":%s,\"free\":%s}", $2,$3,$4}')

  # vmstat
  vm_line=$(vmstat 1 1 | tail -n 1 | awk '{printf "{\"r\":%s,\"b\":%s,\"swpd\":%s,\"free\":%s,\"buff\":%s,\"cache\":%s,\"si\":%s,\"so\":%s,\"bi\":%s,\"bo\":%s,\"in\":%s,\"cs\":%s,\"us\":%s,\"sy\":%s,\"id\":%s,\"wa\":%s,\"st\":%s}", $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17}')

  # Pod top metrics
  pod_top=$(kubectl top pods -n "$NS" --containers --no-headers 2>/dev/null | awk '{printf "{\"pod\":\"%s\",\"container\":\"%s\",\"cpu\":\"%s\",\"mem\":\"%s\"},", $1,$2,$3,$4}' | sed 's/,$//')

  # Node top
  node_top=$(kubectl top nodes --no-headers 2>/dev/null | awk '{printf "{\"node\":\"%s\",\"cpu\":\"%s\",\"cpu_pct\":\"%s\",\"mem\":\"%s\",\"mem_pct\":\"%s\"}", $1,$2,$3,$4,$5}')

  # Pod status & restarts
  pod_status=$(kubectl get pods -n "$NS" -o jsonpath='{range .items[*]}{"{\"name\":\""}{.metadata.name}{"\",\"phase\":\""}{.status.phase}{"\",\"restarts\":"}{.status.containerStatuses[0].restartCount}{"},"}{end}' | sed 's/,$//')

  # CPU throttling snapshots
  fe_stat=$(get_cgroup_cpu_stat "${FE_CID}" | awk '{printf "\"%s\":%s,", $1,$2}' | sed 's/,$//')
  be_stat=$(get_cgroup_cpu_stat "${BE_CID}" | awk '{printf "\"%s\":%s,", $1,$2}' | sed 's/,$//')
  redis_stat=$(get_cgroup_cpu_stat "${REDIS_CID}" | awk '{printf "\"%s\":%s,", $1,$2}' | sed 's/,$//')
  pg_stat=$(get_cgroup_cpu_stat "${PG_CID}" | awk '{printf "\"%s\":%s,", $1,$2}' | sed 's/,$//')
  traefik_stat=$(get_cgroup_cpu_stat "${TRAEFIK_CID}" | awk '{printf "\"%s\":%s,", $1,$2}' | sed 's/,$//')

  # Combine into single JSON line
  printf '{"ts":"%s","mem":%s,"swap":%s,"vmstat":%s,"node":%s,"pods":[%s],"pod_status":[%s],"cpu_stat":{"fe":{%s},"be":{%s},"redis":{%s},"pg":{%s},"traefik":{%s}}}\n' \
    "$ts" "$mem_free" "$swap_free" "$vm_line" "${node_top:-{}}" "$pod_top" "$pod_status" "$fe_stat" "$be_stat" "$redis_stat" "$pg_stat" "$traefik_stat"

  sleep 1
done
