#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-redis-e2e}"
name="${2:-redis-failover}"
master_name="${3:-mymaster}"
redis_password="${4:-}"
sentinel_password="${5:-}"

redis_cli_auth=()
if [ -n "$redis_password" ]; then
  redis_cli_auth=(-a "$redis_password")
fi

sentinel_cli_auth=()
if [ -n "$sentinel_password" ]; then
  sentinel_cli_auth=(-a "$sentinel_password")
fi

redis_tls_flags=()
sentinel_tls_flags=()
tls_secret="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.tls.secretName}' 2>/dev/null || true)"
if [ -n "$tls_secret" ]; then
  redis_tls_flags=(--tls --cacert /etc/redis-tls/ca.crt)
  sentinel_tls_flags=(--tls --cacert /etc/redis-tls/ca.crt)
fi

expected_redis="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.redisReplicas}')"
expected_sentinel="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.sentinelReplicas}')"

canonical_endpoint() {
  local host="$1"
  local port="$2"
  if [[ "$host" == *:* ]] && [[ "$host" != \[*\] ]]; then
    printf '[%s]:%s' "$host" "$port"
    return
  fi
  printf '%s:%s' "$host" "$port"
}

fetch_nodes() {
  local component="$1"
  kubectl -n "$namespace" get redis "$name" -o jsonpath="{range .spec.externalAccess.failover.${component}.nodes[*]}{.ordinal}{'|'}{.host}{'|'}{.port}{'\n'}{end}" \
    | sed '/^$/d' \
    | sort -t'|' -k1,1n
}

mapfile -t redis_nodes < <(fetch_nodes "redis")
mapfile -t sentinel_nodes < <(fetch_nodes "sentinel")

if [ "${#redis_nodes[@]}" -ne "$expected_redis" ]; then
  echo "unexpected redis external node count: got=${#redis_nodes[@]} expected=${expected_redis}"
  exit 1
fi
if [ "${#sentinel_nodes[@]}" -ne "$expected_sentinel" ]; then
  echo "unexpected sentinel external node count: got=${#sentinel_nodes[@]} expected=${expected_sentinel}"
  exit 1
fi

expected_redis_endpoints=()
expected_sentinel_endpoints=()
redis_ordinal_endpoint_pairs=()

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

ordinal_for_endpoint() {
  local target="$1"
  shift || true
  local pair
  for pair in "$@"; do
    local ordinal
    local endpoint
    ordinal="${pair%%|*}"
    endpoint="${pair#*|}"
    if [ "$endpoint" = "$target" ]; then
      printf '%s' "$ordinal"
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

trim_cr() {
  local value="$1"
  printf '%s' "$value" | tr -d '\r'
}

redis_pod_name() {
  local ordinal="$1"
  echo "${name}-redis-${ordinal}"
}

redis_pod_host() {
  local pod="$1"
  echo "${pod}.${name}-redis-headless.${namespace}.svc.cluster.local"
}

sentinel_master_field() {
  local field="$1"
  kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$sentinel_host" -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL master "$master_name" 2>/dev/null \
    | awk -v key="$field" 'previous == key {print; exit} {previous = $0}' \
    | tr -d '\r' || true
}

compute_pause_duration_ms() {
  local down_after
  down_after="$(sentinel_master_field "down-after-milliseconds")"
  if ! [[ "$down_after" =~ ^[0-9]+$ ]]; then
    down_after=5000
  fi
  local failover_timeout
  failover_timeout="$(sentinel_master_field "failover-timeout")"
  if ! [[ "$failover_timeout" =~ ^[0-9]+$ ]]; then
    failover_timeout=60000
  fi
  local pause_ms=$((down_after + failover_timeout + 30000))
  if [ "$pause_ms" -lt 120000 ]; then
    pause_ms=120000
  fi
  echo "$pause_ms"
}

inject_master_pause() {
  local ordinal="$1"
  local pause_ms="$2"
  local pod
  pod="$(redis_pod_name "$ordinal")"
  local host
  host="$(redis_pod_host "$pod")"
  local out
  out="$(kubectl -n "$namespace" exec "$pod" -- redis-cli "${redis_tls_flags[@]}" -h "$host" -p 6379 "${redis_cli_auth[@]}" --raw CLIENT PAUSE "$pause_ms" ALL 2>/dev/null || true)"
  out="$(trim_cr "$out")"
  if [ "$out" != "OK" ]; then
    echo "failed to inject CLIENT PAUSE on ${pod}: output=${out:-<empty>}"
    exit 1
  fi
}

for line in "${redis_nodes[@]}"; do
  ordinal="${line%%|*}"
  rest="${line#*|}"
  host="${rest%%|*}"
  port="${rest##*|}"
  endpoint="$(canonical_endpoint "$host" "$port")"
  expected_redis_endpoints+=("$endpoint")
  redis_ordinal_endpoint_pairs+=("${ordinal}|${endpoint}")

  svc_name="${name}-redis-external-${ordinal}"
  svc_type="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.type}')"
  svc_node_port="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.ports[0].nodePort}')"
  if [ "$svc_type" != "NodePort" ] || [ "$svc_node_port" != "$port" ]; then
    echo "redis external service mismatch: service=${svc_name} type=${svc_type} nodePort=${svc_node_port} expectedNodePort=${port}"
    exit 1
  fi
done

for line in "${sentinel_nodes[@]}"; do
  ordinal="${line%%|*}"
  rest="${line#*|}"
  host="${rest%%|*}"
  port="${rest##*|}"
  endpoint="$(canonical_endpoint "$host" "$port")"
  expected_sentinel_endpoints+=("$endpoint")

  svc_name="${name}-sentinel-external-${ordinal}"
  svc_type="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.type}')"
  svc_node_port="$(kubectl -n "$namespace" get svc "$svc_name" -o jsonpath='{.spec.ports[0].nodePort}')"
  if [ "$svc_type" != "NodePort" ] || [ "$svc_node_port" != "$port" ]; then
    echo "sentinel external service mismatch: service=${svc_name} type=${svc_type} nodePort=${svc_node_port} expectedNodePort=${port}"
    exit 1
  fi
done

mapfile -t status_external_endpoints < <(kubectl -n "$namespace" get redis "$name" -o jsonpath='{range .status.endpoints.external[*]}{.host}{"|"}{.port}{"\n"}{end}' | sed '/^$/d')
if [ "${#status_external_endpoints[@]}" -ne "$expected_sentinel" ]; then
  echo "unexpected status external endpoint count: got=${#status_external_endpoints[@]} expected=${expected_sentinel}"
  exit 1
fi
for line in "${status_external_endpoints[@]}"; do
  host="${line%%|*}"
  port="${line##*|}"
  endpoint="$(canonical_endpoint "$host" "$port")"
  if ! contains_endpoint "$endpoint" "${expected_sentinel_endpoints[@]}"; then
    echo "unexpected status external endpoint: ${endpoint}"
    exit 1
  fi
done

sentinel_pod="${name}-sentinel-0"
sentinel_host="${sentinel_pod}.${name}-sentinel.${namespace}.svc.cluster.local"

fetch_master_endpoint() {
  local endpoint_raw
  endpoint_raw="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$sentinel_host" -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL get-master-addr-by-name "$master_name" 2>/dev/null || true)"
  local host
  local port
  host="$(printf '%s\n' "$endpoint_raw" | sed -n '1p' | tr -d '\r')"
  port="$(printf '%s\n' "$endpoint_raw" | sed -n '2p' | tr -d '\r')"
  if [ -z "$host" ] || [ -z "$port" ]; then
    return 1
  fi
  canonical_endpoint "$host" "$port"
}

deadline=$((SECONDS + 300))
current_master=""
while true; do
  ckquorum_output="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$sentinel_host" -p 26379 "${sentinel_cli_auth[@]}" SENTINEL CKQUORUM "$master_name" 2>/dev/null || true)"
  if printf '%s\n' "$ckquorum_output" | grep -Eq '^OK'; then
    if current_master="$(fetch_master_endpoint)"; then
      if contains_endpoint "$current_master" "${expected_redis_endpoints[@]}"; then
        break
      fi
    fi
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting sentinel to report external master endpoint"
    exit 1
  fi
  sleep 2
done

current_ordinal="$(ordinal_for_endpoint "$current_master" "${redis_ordinal_endpoint_pairs[@]}" || true)"
if [ -z "$current_ordinal" ]; then
  echo "cannot map current external master endpoint to redis ordinal: ${current_master}"
  exit 1
fi

master_host_port="$(split_endpoint "$current_master")"
master_host="${master_host_port%%|*}"
master_port="${master_host_port##*|}"
if ! kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${redis_tls_flags[@]}" -h "${master_host}" -p "${master_port}" "${redis_cli_auth[@]}" PING >/dev/null 2>&1; then
  echo "cannot connect to current external master endpoint: ${current_master}"
  exit 1
fi

pause_duration_ms="$(compute_pause_duration_ms)"
echo "pausing current external master via CLIENT PAUSE: ordinal=${current_ordinal} duration_ms=${pause_duration_ms}"
inject_master_pause "$current_ordinal" "$pause_duration_ms"

deadline=$((SECONDS + 420))
while true; do
  next_master="$(fetch_master_endpoint || true)"
  if [ -n "$next_master" ] && [ "$next_master" != "$current_master" ] && contains_endpoint "$next_master" "${expected_redis_endpoints[@]}"; then
    echo "nodeport external failover assertions passed: old=${current_master} new=${next_master}"
    exit 0
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting master endpoint change after pausing master: old=${current_master} last=${next_master:-<none>} pause_ms=${pause_duration_ms}"
    exit 1
  fi
  sleep 2
done
