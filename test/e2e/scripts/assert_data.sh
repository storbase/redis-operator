#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  assert_data.sh \
    --mode <cluster|failover> \
    --action <put|check> \
    --namespace <namespace> \
    --name <redis-name> \
    --key-prefix <prefix> \
    [--key-count <count>] \
    [--redis-password <password>] \
    [--sentinel-password <password>] \
    [--master-name <name>]

Defaults:
  --key-count: 1000
  --master-name: mymaster
EOF
}

mode=""
action=""
namespace=""
name=""
key_prefix=""
key_count="1000"
redis_password=""
sentinel_password=""
master_name="mymaster"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode)
      mode="${2:-}"
      shift 2
      ;;
    --action)
      action="${2:-}"
      shift 2
      ;;
    --namespace)
      namespace="${2:-}"
      shift 2
      ;;
    --name)
      name="${2:-}"
      shift 2
      ;;
    --key-prefix)
      key_prefix="${2:-}"
      shift 2
      ;;
    --key-count)
      key_count="${2:-}"
      shift 2
      ;;
    --redis-password)
      redis_password="${2:-}"
      shift 2
      ;;
    --sentinel-password)
      sentinel_password="${2:-}"
      shift 2
      ;;
    --master-name)
      master_name="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1"
      usage
      exit 1
      ;;
  esac
done

require_non_empty() {
  local key="$1"
  local value="$2"
  if [ -z "$value" ]; then
    echo "missing required argument: ${key}"
    usage
    exit 1
  fi
}

require_non_empty "--mode" "$mode"
require_non_empty "--action" "$action"
require_non_empty "--namespace" "$namespace"
require_non_empty "--name" "$name"
require_non_empty "--key-prefix" "$key_prefix"

if [ "$mode" != "cluster" ] && [ "$mode" != "failover" ]; then
  echo "--mode must be cluster or failover"
  exit 1
fi

if [ "$action" != "put" ] && [ "$action" != "check" ]; then
  echo "--action must be put or check"
  exit 1
fi

if ! [[ "$key_count" =~ ^[0-9]+$ ]] || [ "$key_count" -le 0 ]; then
  echo "--key-count must be a positive integer"
  exit 1
fi

run_in_pod() {
  local pod="$1"
  shift
  kubectl -n "$namespace" exec "$pod" -- "$@"
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

tls_enabled="false"
tls_secret="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.tls.secretName}' 2>/dev/null || true)"
if [ -n "$tls_secret" ]; then
  tls_enabled="true"
fi
tls_flags=()
if [ "$tls_enabled" = "true" ]; then
  tls_flags=(--tls --cacert /etc/redis-tls/ca.crt)
fi

run_cluster_action() {
  local seed_pod="${name}-shard-0-0"
  local seed_host="${seed_pod}.${name}-shard-0.${namespace}.svc.cluster.local"
  local env_args=(
    "REDIS_HOST=${seed_host}"
    "KEY_PREFIX=${key_prefix}"
    "KEY_COUNT=${key_count}"
    "ACTION=${action}"
    "TLS_ENABLED=${tls_enabled}"
  )
  if [ -n "$redis_password" ]; then
    env_args+=("REDIS_PASSWORD=${redis_password}")
  fi

  kubectl -n "$namespace" wait --for=condition=Ready "pod/${seed_pod}" --timeout=300s >/dev/null
  run_in_pod "$seed_pod" env "${env_args[@]}" sh -ceu '
tls_args=""
if [ "${TLS_ENABLED}" = "true" ]; then
  tls_args="--tls --cacert /etc/redis-tls/ca.crt"
fi
if [ -n "${REDIS_PASSWORD:-}" ]; then
  export REDISCLI_AUTH="${REDIS_PASSWORD}"
fi

cluster_cmd() {
  cmd_deadline=$(( $(date +%s) + 300 ))
  while true; do
    output="$(redis-cli -c $tls_args --raw -h "$REDIS_HOST" -p 6379 "$@" 2>&1)" && {
      printf "%s\n" "$output"
      return 0
    }
    case "$output" in
      *TRYAGAIN*|*MOVED*|*ASK*|*CLUSTERDOWN*|*LOADING*)
        if [ "$(date +%s)" -ge "$cmd_deadline" ]; then
          echo "$output" >&2
          return 1
        fi
        sleep 2
        ;;
      *)
        echo "$output" >&2
        return 1
        ;;
    esac
  done
}

meta_value="version=v1,count=${KEY_COUNT}"

if [ "$ACTION" = "put" ]; then
  i=0
  while [ "$i" -lt "$KEY_COUNT" ]; do
    key="${KEY_PREFIX}:k:${i}"
    value="v-${i}"
    cluster_cmd SET "$key" "$value" >/dev/null
    i=$((i + 1))
  done
  cluster_cmd SET "${KEY_PREFIX}:meta" "$meta_value" >/dev/null
  echo "data assertions passed"
  exit 0
fi

meta_read="$(cluster_cmd GET "${KEY_PREFIX}:meta" | tr -d "\r")"
if [ -z "$meta_read" ]; then
  echo "missing metadata key ${KEY_PREFIX}:meta" >&2
  exit 1
fi
if [ "$meta_read" != "$meta_value" ]; then
  echo "metadata mismatch: expected=${meta_value} actual=${meta_read}" >&2
  exit 1
fi

missing_count=0
missing_preview=""
i=0
while [ "$i" -lt "$KEY_COUNT" ]; do
  key="${KEY_PREFIX}:k:${i}"
  exists="$(cluster_cmd EXISTS "$key" | tr -d "\r")"
  if [ "$exists" != "1" ]; then
    missing_count=$((missing_count + 1))
    if [ "$missing_count" -le 10 ]; then
      missing_preview="${missing_preview} ${key}"
    fi
  fi
  i=$((i + 1))
done
if [ "$missing_count" -gt 0 ]; then
  echo "missing keys count=${missing_count} preview:${missing_preview}" >&2
  exit 1
fi

for sample_idx in 0 1 2 10 100 500 999; do
  if [ "$sample_idx" -ge "$KEY_COUNT" ]; then
    continue
  fi
  key="${KEY_PREFIX}:k:${sample_idx}"
  actual="$(cluster_cmd GET "$key" | tr -d "\r")"
  expected="v-${sample_idx}"
  if [ "$actual" != "$expected" ]; then
    echo "sample value mismatch key=${key} expected=${expected} actual=${actual}" >&2
    exit 1
  fi
done

echo "data assertions passed"
'
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

resolve_failover_target() {
  expected_redis="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.redisReplicas}')"
  if ! [[ "$expected_redis" =~ ^[0-9]+$ ]] || [ "$expected_redis" -le 0 ]; then
    echo "invalid failover redis replicas from spec: ${expected_redis}"
    exit 1
  fi

  sentinel_pod="${name}-sentinel-0"
  sentinel_host="${sentinel_pod}.${name}-sentinel.${namespace}.svc.cluster.local"
  sentinel_env=()
  if [ -n "$sentinel_password" ]; then
    sentinel_env+=("REDISCLI_AUTH=${sentinel_password}")
  fi

  kubectl -n "$namespace" wait --for=condition=Ready "pod/${sentinel_pod}" --timeout=300s >/dev/null

  local deadline=$((SECONDS + 180))
  local endpoint=""
  while true; do
    endpoint="$(run_in_pod "$sentinel_pod" env "${sentinel_env[@]}" redis-cli "${tls_flags[@]}" -h "$sentinel_host" -p 26379 --raw SENTINEL get-master-addr-by-name "$master_name" 2>&1 || true)"
    if printf '%s\n' "$endpoint" | grep -Eiq 'NOAUTH|WRONGPASS'; then
      echo "sentinel authentication failed: ${endpoint}"
      exit 1
    fi
    reported_host="$(printf '%s\n' "$endpoint" | sed -n '1p' | tr -d '\r')"
    reported_port="$(printf '%s\n' "$endpoint" | sed -n '2p' | tr -d '\r')"
    if [ -n "$reported_host" ] && [ -n "$reported_port" ]; then
      break
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "failed to resolve sentinel master endpoint: ${endpoint}"
      exit 1
    fi
    sleep 2
  done

  if is_ip_like_host "$reported_host"; then
    echo "sentinel returned IP-like master host, expected DNS hostname: ${reported_host}"
    exit 1
  fi

  master_pod="$(normalize_redis_host_to_pod "$reported_host" || true)"
  if [ -z "$master_pod" ]; then
    echo "failed to map sentinel master host to StatefulSet pod: ${reported_host}"
    exit 1
  fi
  master_host="$(redis_pod_host "$master_pod")"
  master_port="$reported_port"
}

run_failover_action() {
  resolve_failover_target

  local env_args=(
    "REDIS_HOST=${master_host}"
    "REDIS_PORT=${master_port}"
    "KEY_PREFIX=${key_prefix}"
    "KEY_COUNT=${key_count}"
    "ACTION=${action}"
    "TLS_ENABLED=${tls_enabled}"
  )
  if [ -n "$redis_password" ]; then
    env_args+=("REDIS_PASSWORD=${redis_password}")
  fi

  run_in_pod "$sentinel_pod" env "${env_args[@]}" sh -ceu '
tls_args=""
if [ "${TLS_ENABLED}" = "true" ]; then
  tls_args="--tls --cacert /etc/redis-tls/ca.crt"
fi
if [ -n "${REDIS_PASSWORD:-}" ]; then
  export REDISCLI_AUTH="${REDIS_PASSWORD}"
fi

redis_cmd() {
  redis-cli $tls_args --raw -h "$REDIS_HOST" -p "$REDIS_PORT" "$@"
}

meta_value="version=v1,count=${KEY_COUNT}"

if [ "$ACTION" = "put" ]; then
  i=0
  while [ "$i" -lt "$KEY_COUNT" ]; do
    key="${KEY_PREFIX}:k:${i}"
    value="v-${i}"
    redis_cmd SET "$key" "$value" >/dev/null
    i=$((i + 1))
  done
  redis_cmd SET "${KEY_PREFIX}:meta" "$meta_value" >/dev/null
  echo "data assertions passed"
  exit 0
fi

meta_read="$(redis_cmd GET "${KEY_PREFIX}:meta" | tr -d "\r")"
if [ -z "$meta_read" ]; then
  echo "missing metadata key ${KEY_PREFIX}:meta" >&2
  exit 1
fi
if [ "$meta_read" != "$meta_value" ]; then
  echo "metadata mismatch: expected=${meta_value} actual=${meta_read}" >&2
  exit 1
fi

missing_count=0
missing_preview=""
i=0
while [ "$i" -lt "$KEY_COUNT" ]; do
  key="${KEY_PREFIX}:k:${i}"
  exists="$(redis_cmd EXISTS "$key" | tr -d "\r")"
  if [ "$exists" != "1" ]; then
    missing_count=$((missing_count + 1))
    if [ "$missing_count" -le 10 ]; then
      missing_preview="${missing_preview} ${key}"
    fi
  fi
  i=$((i + 1))
done
if [ "$missing_count" -gt 0 ]; then
  echo "missing keys count=${missing_count} preview:${missing_preview}" >&2
  exit 1
fi

for sample_idx in 0 1 2 10 100 500 999; do
  if [ "$sample_idx" -ge "$KEY_COUNT" ]; then
    continue
  fi
  key="${KEY_PREFIX}:k:${sample_idx}"
  actual="$(redis_cmd GET "$key" | tr -d "\r")"
  expected="v-${sample_idx}"
  if [ "$actual" != "$expected" ]; then
    echo "sample value mismatch key=${key} expected=${expected} actual=${actual}" >&2
    exit 1
  fi
done

echo "data assertions passed"
'
}

if [ "$mode" = "cluster" ]; then
  run_cluster_action
  exit 0
fi

run_failover_action
