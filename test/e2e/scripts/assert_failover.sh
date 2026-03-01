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

redis_labels="app.kubernetes.io/instance=${name},app.kubernetes.io/component=redis"
sentinel_labels="app.kubernetes.io/instance=${name},app.kubernetes.io/component=sentinel"
expected_redis="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.redisReplicas}')"
expected_sentinel="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.sentinelReplicas}')"

trim_cr() {
  local value="$1"
  printf '%s' "$value" | tr -d '\r'
}

is_ip_like_host() {
  local host="$1"
  if [[ "$host" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    return 0
  fi
  if [[ "$host" == *:* ]]; then
    return 0
  fi
  return 1
}

redis_pod_name() {
  local ordinal="$1"
  echo "${name}-redis-${ordinal}"
}

redis_pod_host() {
  local pod="$1"
  echo "${pod}.${name}-redis-headless.${namespace}.svc.cluster.local"
}

normalize_redis_host_to_pod() {
  local raw_host="$1"
  local host="${raw_host%.}"
  host="$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')"
  if [ -z "$host" ]; then
    return 1
  fi
  if is_ip_like_host "$host"; then
    return 1
  fi

  local ordinal
  for ordinal in $(seq 0 $((expected_redis - 1))); do
    local pod
    pod="$(redis_pod_name "$ordinal")"
    local host_fqdn="${pod}.${name}-redis-headless.${namespace}.svc.cluster.local"
    local host_svc="${pod}.${name}-redis-headless.${namespace}.svc"
    local host_ns="${pod}.${name}-redis-headless.${namespace}"
    local host_short="${pod}.${name}-redis-headless"
    if [ "$host" = "$pod" ] || [ "$host" = "$host_short" ] || [ "$host" = "$host_ns" ] || [ "$host" = "$host_svc" ] || [ "$host" = "$host_fqdn" ]; then
      echo "$pod"
      return 0
    fi
  done
  return 1
}

query_replication_field() {
  local pod="$1"
  local field="$2"
  local host
  host="$(redis_pod_host "$pod")"
  kubectl -n "$namespace" exec "$pod" -- redis-cli "${redis_tls_flags[@]}" -h "$host" -p 6379 "${redis_cli_auth[@]}" --raw INFO replication 2>/dev/null \
    | sed -n "s/^${field}://p" | tr -d '\r' | head -n1 || true
}

assert_replica_topology() {
  local expected_master_pod="$1"
  local expected_master_port="$2"
  local deadline=$((SECONDS + 180))

  while true; do
    local master_count=0
    local failure_reason=""
    local ordinal
    for ordinal in $(seq 0 $((expected_redis - 1))); do
      local pod
      pod="$(redis_pod_name "$ordinal")"
      local role
      role="$(query_replication_field "$pod" "role")"
      if [ "$role" = "master" ]; then
        master_count=$((master_count + 1))
        if [ "$pod" != "$expected_master_pod" ]; then
          failure_reason="unexpected master pod=${pod} expected=${expected_master_pod}"
          break
        fi
        continue
      fi
      if [ "$role" != "slave" ] && [ "$role" != "replica" ]; then
        failure_reason="pod=${pod} has unexpected role=${role:-<empty>}"
        break
      fi
      local link_status
      link_status="$(query_replication_field "$pod" "master_link_status")"
      if [ "$link_status" != "up" ]; then
        failure_reason="pod=${pod} has master_link_status=${link_status:-<empty>}"
        break
      fi
      local replica_master_host
      replica_master_host="$(query_replication_field "$pod" "master_host")"
      local replica_master_port
      replica_master_port="$(query_replication_field "$pod" "master_port")"
      local mapped_master_pod
      mapped_master_pod="$(normalize_redis_host_to_pod "$replica_master_host" || true)"
      if [ -z "$mapped_master_pod" ]; then
        failure_reason="pod=${pod} reports invalid master_host=${replica_master_host:-<empty>}"
        break
      fi
      if [ "$mapped_master_pod" != "$expected_master_pod" ]; then
        failure_reason="pod=${pod} follows ${mapped_master_pod} expected=${expected_master_pod}"
        break
      fi
      if [ "$replica_master_port" != "$expected_master_port" ]; then
        failure_reason="pod=${pod} reports master_port=${replica_master_port:-<empty>} expected=${expected_master_port}"
        break
      fi
    done
    if [ -z "$failure_reason" ] && [ "$master_count" -eq 1 ]; then
      echo "replica topology is healthy: master=${expected_master_pod} replicas=$((expected_redis - 1))"
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "timeout waiting replica topology healthy: ${failure_reason:-unexpected master_count=${master_count}}"
      return 1
    fi
    sleep 2
  done
}

deadline=$((SECONDS + 600))
while true; do
  redis_count="$(kubectl -n "$namespace" get pods -l "$redis_labels" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  sentinel_count="$(kubectl -n "$namespace" get pods -l "$sentinel_labels" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$redis_count" -ge "$expected_redis" ] && [ "$sentinel_count" -ge "$expected_sentinel" ]; then
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting failover pods to be created: redis=$redis_count/$expected_redis sentinel=$sentinel_count/$expected_sentinel"
    exit 1
  fi
  sleep 2
done

kubectl -n "$namespace" wait --for=condition=Ready pod -l "$redis_labels" --timeout=600s
kubectl -n "$namespace" wait --for=condition=Ready pod -l "$sentinel_labels" --timeout=600s

sentinel_pod="${name}-sentinel-0"
sentinel_host="${sentinel_pod}.${name}-sentinel.${namespace}.svc.cluster.local"
master_host=""
master_port=""
deadline=$((SECONDS + 300))
while true; do
  ckquorum_output="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$sentinel_host" -p 26379 "${sentinel_cli_auth[@]}" SENTINEL CKQUORUM "$master_name" 2>/dev/null || true)"
  if printf '%s\n' "$ckquorum_output" | grep -Eq '^OK'; then
    master_endpoint="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$sentinel_host" -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL get-master-addr-by-name "$master_name" 2>/dev/null || true)"
    master_host="$(printf '%s\n' "$master_endpoint" | sed -n '1p' | tr -d '\r')"
    master_port="$(printf '%s\n' "$master_endpoint" | sed -n '2p' | tr -d '\r')"
    if [ -n "$master_host" ] && [ -n "$master_port" ]; then
      if kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${redis_tls_flags[@]}" -h "$master_host" -p "$master_port" "${redis_cli_auth[@]}" PING >/dev/null 2>&1; then
        break
      fi
    fi
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting sentinel quorum/master resolution"
    printf '%s\n' "$ckquorum_output"
    exit 1
  fi
  sleep 2
done

master_pod=""
deadline=$((SECONDS + 180))
while true; do
  master_count=0
  detected_master=""
  for ordinal in $(seq 0 $((expected_redis - 1))); do
    candidate="$(redis_pod_name "$ordinal")"
    candidate_role="$(query_replication_field "$candidate" "role")"
    if [ "$candidate_role" = "master" ]; then
      master_count=$((master_count + 1))
      detected_master="$candidate"
    fi
  done
  if [ "$master_count" -eq 1 ]; then
    master_pod="$detected_master"
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "failed to determine a single master pod for failover assertions (master_count=${master_count})"
    exit 1
  fi
  sleep 2
done

role_info=""
deadline=$((SECONDS + 180))
while true; do
  if role_info="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${redis_tls_flags[@]}" -h "$master_host" -p "$master_port" "${redis_cli_auth[@]}" INFO replication 2>/dev/null || true)"; then
    if printf '%s\n' "$role_info" | grep -q "role:master"; then
      break
    fi
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting master role info via sentinel endpoint"
    printf '%s\n' "$role_info"
    exit 1
  fi
  sleep 2
done

sentinel_master_pod="$(normalize_redis_host_to_pod "$master_host" || true)"
if [ -z "$sentinel_master_pod" ]; then
  echo "sentinel returned invalid master host (ip or unknown): ${master_host}"
  exit 1
fi
if [ "$sentinel_master_pod" != "$master_pod" ]; then
  echo "sentinel master (${sentinel_master_pod}) does not match actual master (${master_pod})"
  exit 1
fi

assert_replica_topology "$master_pod" "$(trim_cr "$master_port")"

key="${name}:steady:e2e"
kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${redis_tls_flags[@]}" -h "$master_host" -p "$master_port" "${redis_cli_auth[@]}" SET "$key" "ok" >/dev/null
value="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${redis_tls_flags[@]}" -h "$master_host" -p "$master_port" "${redis_cli_auth[@]}" GET "$key" | tr -d '\r')"
if [ "$value" != "ok" ]; then
  echo "unexpected value: $value"
  exit 1
fi

echo "failover steady-state assertions passed"
