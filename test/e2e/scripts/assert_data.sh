#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-redis-e2e}"
name="${2:-redis-cluster}"
action="${3:-check}"
key_prefix="${4:-${name}:e2e:cluster:v1}"
key_count="${5:-1000}"
redis_password="${6:-}"
seed_host_override="${7:-}"
seed_port_override="${8:-6379}"

if [ "$action" != "put" ] && [ "$action" != "check" ] && [ "$action" != "count" ]; then
  echo "unsupported action: $action (expected put|check|count)"
  exit 1
fi

if ! [[ "$key_count" =~ ^[0-9]+$ ]] || [ "$key_count" -le 0 ]; then
  echo "invalid key_count: $key_count"
  exit 1
fi

seed_pod="${name}-shard-0-0"
seed_host="${seed_pod}.${name}-shard-0.${namespace}.svc.cluster.local"
seed_port="6379"

if [ -n "$seed_host_override" ]; then
  seed_host="$seed_host_override"
  seed_port="$seed_port_override"
fi

if ! [[ "$seed_port" =~ ^[0-9]+$ ]] || [ "$seed_port" -le 0 ] || [ "$seed_port" -gt 65535 ]; then
  echo "invalid seed_port: $seed_port"
  exit 1
fi

tls_secret="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.tls.secretName}' 2>/dev/null || true)"
use_tls="0"
if [ -n "$tls_secret" ]; then
  use_tls="1"
fi

kubectl -n "$namespace" wait --for=condition=Ready "pod/${seed_pod}" --timeout=300s >/dev/null

run_in_pod() {
  local script="$1"
  kubectl -n "$namespace" exec "$seed_pod" -- env \
    SEED_HOST="$seed_host" \
    SEED_PORT="$seed_port" \
    ACTION="$action" \
    KEY_PREFIX="$key_prefix" \
    KEY_COUNT="$key_count" \
    REDIS_PASSWORD="$redis_password" \
    USE_TLS="$use_tls" \
    bash -lc "$script"
}

if [ "$action" = "put" ]; then
  run_in_pod '
    set -euo pipefail

    redis_base=(redis-cli -c -h "$SEED_HOST" -p "$SEED_PORT" --raw)
    if [ "$USE_TLS" = "1" ]; then
      redis_base+=(--tls --cacert /etc/redis-tls/ca.crt)
    fi
    if [ -n "$REDIS_PASSWORD" ]; then
      export REDISCLI_AUTH="$REDIS_PASSWORD"
    else
      unset REDISCLI_AUTH || true
    fi

    is_retryable_output() {
      echo "$1" | grep -Eiq "MOVED|ASK|TRYAGAIN|CLUSTERDOWN|LOADING|MASTERDOWN|timeout|temporarily unavailable|connection reset|i/o timeout"
    }

    run_redis() {
      local start output rc
      start=$SECONDS
      while true; do
        set +e
        output="$("${redis_base[@]}" "$@" 2>&1)"
        rc=$?
        set -e
        if [ "$rc" -eq 0 ]; then
          printf "%s\n" "$output"
          return 0
        fi
        if is_retryable_output "$output" && [ $((SECONDS - start)) -lt 300 ]; then
          sleep 2
          continue
        fi
        echo "$output" >&2
        return 1
      done
    }

    for ((i=0; i<KEY_COUNT; i++)); do
      key="${KEY_PREFIX}:k:${i}"
      value="v-${i}"
      run_redis SET "$key" "$value" >/dev/null
    done

    run_redis SET "${KEY_PREFIX}:meta" "count=${KEY_COUNT};version=v1" >/dev/null
    echo "data assertions passed"
  '
  exit 0
fi

run_in_pod '
  set -euo pipefail

  redis_base=(redis-cli -c -h "$SEED_HOST" -p "$SEED_PORT" --raw)
  if [ "$USE_TLS" = "1" ]; then
    redis_base+=(--tls --cacert /etc/redis-tls/ca.crt)
  fi
  if [ -n "$REDIS_PASSWORD" ]; then
    export REDISCLI_AUTH="$REDIS_PASSWORD"
  else
    unset REDISCLI_AUTH || true
  fi

  is_retryable_output() {
    echo "$1" | grep -Eiq "MOVED|ASK|TRYAGAIN|CLUSTERDOWN|LOADING|MASTERDOWN|timeout|temporarily unavailable|connection reset|i/o timeout"
  }

  run_redis() {
    local start output rc
    start=$SECONDS
    while true; do
      set +e
      output="$("${redis_base[@]}" "$@" 2>&1)"
      rc=$?
      set -e
      if [ "$rc" -eq 0 ]; then
        printf "%s\n" "$output"
        return 0
      fi
      if is_retryable_output "$output" && [ $((SECONDS - start)) -lt 300 ]; then
        sleep 2
        continue
      fi
      echo "$output" >&2
      return 1
    done
  }

  meta_value="$(run_redis GET "${KEY_PREFIX}:meta" | tr -d "\r")"
  if [ -z "$meta_value" ]; then
    echo "missing metadata key: ${KEY_PREFIX}:meta"
    exit 1
  fi

  existing_count=0

  for ((i=0; i<KEY_COUNT; i++)); do
    key="${KEY_PREFIX}:k:${i}"
    exists="$(run_redis EXISTS "$key" | tr -d "\r\n[:space:]")"
    if [ "$exists" = "1" ]; then
      existing_count=$((existing_count + 1))
    fi
  done

  if [ "$ACTION" = "count" ]; then
    if [ "$existing_count" != "$KEY_COUNT" ]; then
      echo "data count check failed: expected=${KEY_COUNT} actual=${existing_count}"
      exit 1
    fi
    echo "data count assertions passed"
    exit 0
  fi

  missing_count=$((KEY_COUNT - existing_count))
  mismatch_count=0
  missing_samples=()
  mismatch_samples=()

  for ((i=0; i<KEY_COUNT; i++)); do
    key="${KEY_PREFIX}:k:${i}"
    exists="$(run_redis EXISTS "$key" | tr -d "\r\n[:space:]")"
    if [ "$exists" != "1" ]; then
      if [ "${#missing_samples[@]}" -lt 10 ]; then
        missing_samples+=("$key")
      fi
    fi
  done

  sample_indices=(0 1 2 10 100 500 999)
  for idx in "${sample_indices[@]}"; do
    if [ "$idx" -ge "$KEY_COUNT" ]; then
      continue
    fi
    key="${KEY_PREFIX}:k:${idx}"
    expected="v-${idx}"
    actual="$(run_redis GET "$key" | tr -d "\r")"
    if [ "$actual" != "$expected" ]; then
      mismatch_count=$((mismatch_count + 1))
      if [ "${#mismatch_samples[@]}" -lt 10 ]; then
        mismatch_samples+=("${key}: expected=${expected}, actual=${actual:-<empty>}")
      fi
    fi
  done

  if [ "$missing_count" -gt 0 ] || [ "$mismatch_count" -gt 0 ]; then
    echo "data check failed: missing=${missing_count} mismatch=${mismatch_count}"
    if [ "${#missing_samples[@]}" -gt 0 ]; then
      echo "missing samples:"
      printf "%s\n" "${missing_samples[@]}"
    fi
    if [ "${#mismatch_samples[@]}" -gt 0 ]; then
      echo "mismatch samples:"
      printf "%s\n" "${mismatch_samples[@]}"
    fi
    exit 1
  fi

  echo "data assertions passed"
'
