#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-redis-e2e}"
name="${2:-redis-cluster}"
redis_password="${3:-}"

canonical_endpoint() {
  local host="$1"
  local port="$2"
  if [[ "$host" == *:* ]] && [[ "$host" != \[*\] ]]; then
    printf '[%s]:%s' "$host" "$port"
    return
  fi
  printf '%s:%s' "$host" "$port"
}

contains_endpoint() {
  local target="$1"
  shift || true
  local endpoint
  for endpoint in "$@"; do
    if [ "$endpoint" = "$target" ]; then
      return 0
    fi
  done
  return 1
}

split_endpoint() {
  local endpoint="$1"
  local host
  local port
  if [[ "$endpoint" == \[*\]:* ]]; then
    host="${endpoint%%]*}"
    host="${host#\[}"
    port="${endpoint##*:}"
  else
    host="${endpoint%:*}"
    port="${endpoint##*:}"
  fi
  printf '%s|%s' "$host" "$port"
}

redis_tls_flags=()
tls_secret="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.tls.secretName}' 2>/dev/null || true)"
if [ -n "$tls_secret" ]; then
  redis_tls_flags=(--tls --cacert /etc/redis-tls/ca.crt)
fi

expected_shards="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.cluster.shards}')"
replicas_per_shard="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.cluster.replicasPerShard}')"
expected_nodes=$((expected_shards * (replicas_per_shard + 1)))

mapfile -t cluster_nodes < <(
  kubectl -n "$namespace" get redis "$name" -o jsonpath='{range .spec.externalAccess.cluster.nodes[*]}{.shard}{"|"}{.ordinal}{"|"}{.host}{"|"}{.port}{"|"}{.busPort}{"\n"}{end}' \
    | sed '/^$/d' \
    | sort -t'|' -k1,1n -k2,2n
)

if [ "${#cluster_nodes[@]}" -ne "$expected_nodes" ]; then
  echo "unexpected cluster external node count: got=${#cluster_nodes[@]} expected=${expected_nodes}"
  exit 1
fi

expected_seed_endpoints=()
for line in "${cluster_nodes[@]}"; do
  shard="${line%%|*}"
  rest="${line#*|}"
  ordinal="${rest%%|*}"
  rest="${rest#*|}"
  host="${rest%%|*}"
  rest="${rest#*|}"
  port="${rest%%|*}"
  bus_port="${rest##*|}"

  svc_name="${name}-shard-${shard}-external-${ordinal}"
  svc_type="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.type}')"
  redis_node_port="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.ports[?(@.name=="redis")].nodePort}')"
  bus_node_port="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.ports[?(@.name=="cluster-bus")].nodePort}')"
  service_port_count="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.ports[*].name}' | awk '{print NF}')"
  if [ "$svc_type" != "NodePort" ] || [ "$service_port_count" -ne 2 ]; then
    echo "cluster external service mismatch: service=${svc_name} type=${svc_type} portCount=${service_port_count}"
    exit 1
  fi
  if [ "$redis_node_port" != "$port" ]; then
    echo "cluster external redis nodePort mismatch: service=${svc_name} got=${redis_node_port} want=${port}"
    exit 1
  fi
  if [ "$bus_node_port" != "$bus_port" ]; then
    echo "cluster external bus nodePort mismatch: service=${svc_name} got=${bus_node_port} want=${bus_port}"
    exit 1
  fi

  if [ "$ordinal" = "0" ]; then
    expected_seed_endpoints+=("$(canonical_endpoint "$host" "$port")")
  fi
done

mapfile -t status_external_endpoints < <(
  kubectl -n "$namespace" get redis "$name" -o jsonpath='{range .status.endpoints.external[*]}{.host}{"|"}{.port}{"\n"}{end}' | sed '/^$/d'
)
if [ "${#status_external_endpoints[@]}" -ne "$expected_shards" ]; then
  echo "unexpected status external endpoint count: got=${#status_external_endpoints[@]} expected=${expected_shards}"
  exit 1
fi

for line in "${status_external_endpoints[@]}"; do
  host="${line%%|*}"
  port="${line##*|}"
  endpoint="$(canonical_endpoint "$host" "$port")"
  if ! contains_endpoint "$endpoint" "${expected_seed_endpoints[@]}"; then
    echo "unexpected status external endpoint: ${endpoint}"
    exit 1
  fi
done

seed_pod="${name}-shard-0-0"
seed_host="${seed_pod}.${name}-shard-0.${namespace}.svc.cluster.local"
kubectl -n "$namespace" wait --for=condition=Ready "pod/${seed_pod}" --timeout=300s >/dev/null

cluster_nodes_output="$(
  kubectl -n "$namespace" exec "$seed_pod" -- env \
    REDISCLI_AUTH="$redis_password" \
    SEED_HOST="$seed_host" \
    bash -lc '
      set -euo pipefail
      redis_args=(redis-cli -h "$SEED_HOST" -p 6379)
      if [ -f /etc/redis-tls/ca.crt ]; then
        redis_args+=(--tls --cacert /etc/redis-tls/ca.crt)
      fi
      "${redis_args[@]}" CLUSTER NODES
    '
)"
if [ -z "$cluster_nodes_output" ]; then
  echo "empty cluster nodes output"
  exit 1
fi

first_seed="${expected_seed_endpoints[0]}"
seed_host_port="$(split_endpoint "$first_seed")"
target_host="${seed_host_port%%|*}"
target_port="${seed_host_port##*|}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
slot_tag="$(
  kubectl -n "$namespace" exec "$seed_pod" -- env \
    REDISCLI_AUTH="$redis_password" \
    TARGET_HOST="$target_host" \
    TARGET_PORT="$target_port" \
    bash -lc '
      set -euo pipefail
      redis_args=(redis-cli -h "$TARGET_HOST" -p "$TARGET_PORT")
      if [ -f /etc/redis-tls/ca.crt ]; then
        redis_args+=(--tls --cacert /etc/redis-tls/ca.crt)
      fi
      for i in $(seq 0 2048); do
        tag="cluster-ext-$i"
        slot="$("${redis_args[@]}" --raw CLUSTER KEYSLOT "{$tag}" | tr -d "\r")"
        if [ "$slot" -ge 0 ] && [ "$slot" -le 5461 ]; then
          echo "$tag"
          exit 0
        fi
      done
      exit 1
    '
)"
if [ -z "$slot_tag" ]; then
  echo "failed to determine hash tag mapped to shard-0 slot range"
  exit 1
fi
key_prefix="{${slot_tag}}:${name}:e2e:cluster-external:v1"
key_count="300"
bash "${script_dir}/assert_data.sh" "$namespace" "$name" put "$key_prefix" "$key_count" "$redis_password" "$target_host" "$target_port"
bash "${script_dir}/assert_data.sh" "$namespace" "$name" check "$key_prefix" "$key_count" "$redis_password" "$target_host" "$target_port"

echo "cluster nodeport external assertions passed"
