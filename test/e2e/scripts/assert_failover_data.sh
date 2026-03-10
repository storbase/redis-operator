#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-redis-e2e}"
name="${2:-redis-failover}"
master_name="${3:-mymaster}"
action="${4:-check}"
key_prefix="${5:-${name}:e2e:failover:v1}"
key_count="${6:-200}"
redis_password="${7:-}"
sentinel_password="${8:-}"

if [ "$action" != "put" ] && [ "$action" != "check" ] && [ "$action" != "count" ]; then
  echo "unsupported action: $action (expected put|check|count)"
  exit 1
fi

if ! [[ "$key_count" =~ ^[0-9]+$ ]] || [ "$key_count" -le 0 ]; then
  echo "invalid key_count: $key_count"
  exit 1
fi

sentinel_pod="${name}-sentinel-0"
sentinel_host="${sentinel_pod}.${name}-sentinel.${namespace}.svc.cluster.local"

tls_secret="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.tls.secretName}' 2>/dev/null || true)"
use_tls="0"
if [ -n "$tls_secret" ]; then
  use_tls="1"
fi

kubectl -n "$namespace" wait --for=condition=Ready "pod/${sentinel_pod}" --timeout=300s >/dev/null

run_in_pod() {
  local script="$1"
  kubectl -n "$namespace" exec "$sentinel_pod" -- env \
    SENTINEL_HOST="$sentinel_host" \
    MASTER_NAME="$master_name" \
    KEY_PREFIX="$key_prefix" \
    KEY_COUNT="$key_count" \
    REDIS_PASSWORD="$redis_password" \
    SENTINEL_PASSWORD="$sentinel_password" \
    USE_TLS="$use_tls" \
    bash -lc "$script"
}

if [ "$action" = "put" ]; then
  run_in_pod '
    set -euo pipefail

    sentinel_base=(redis-cli -h "$SENTINEL_HOST" -p 26379 --raw)
    if [ "$USE_TLS" = "1" ]; then
      sentinel_base+=(--tls --cacert /etc/redis-tls/ca.crt)
    fi
    is_retryable_output() {
      echo "$1" | grep -Eiq "READONLY|LOADING|MASTERDOWN|NOREPLICAS|timeout|temporarily unavailable|connection reset|connection refused|i/o timeout|No such master"
    }

    run_sentinel() {
      if [ -n "$SENTINEL_PASSWORD" ]; then
        REDISCLI_AUTH="$SENTINEL_PASSWORD" "${sentinel_base[@]}" "$@"
        return
      fi
      "${sentinel_base[@]}" "$@"
    }

    resolve_master_endpoint() {
      local start output rc host port
      start=$SECONDS
      while true; do
        set +e
        output="$(run_sentinel SENTINEL get-master-addr-by-name "$MASTER_NAME" 2>&1)"
        rc=$?
        set -e
        if [ "$rc" -eq 0 ]; then
          host="$(printf "%s\n" "$output" | sed -n "1p" | tr -d "\r")"
          port="$(printf "%s\n" "$output" | sed -n "2p" | tr -d "\r")"
          if [ -n "$host" ] && [ -n "$port" ]; then
            printf "%s\n%s\n" "$host" "$port"
            return 0
          fi
        fi
        if is_retryable_output "$output" && [ $((SECONDS - start)) -lt 300 ]; then
          sleep 2
          continue
        fi
        echo "$output" >&2
        return 1
      done
    }

    run_redis() {
      local start endpoint master_host master_port output rc
      start=$SECONDS
      while true; do
        endpoint="$(resolve_master_endpoint)"
        master_host="$(printf "%s\n" "$endpoint" | sed -n "1p")"
        master_port="$(printf "%s\n" "$endpoint" | sed -n "2p")"
        redis_base=(redis-cli -h "$master_host" -p "$master_port" --raw)
        if [ "$USE_TLS" = "1" ]; then
          redis_base+=(--tls --cacert /etc/redis-tls/ca.crt)
        fi
        run_redis_cli() {
          if [ -n "$REDIS_PASSWORD" ]; then
            REDISCLI_AUTH="$REDIS_PASSWORD" "${redis_base[@]}" "$@"
            return
          fi
          "${redis_base[@]}" "$@"
        }

        set +e
        output="$(run_redis_cli "$@" 2>&1)"
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

  sentinel_base=(redis-cli -h "$SENTINEL_HOST" -p 26379 --raw)
  if [ "$USE_TLS" = "1" ]; then
    sentinel_base+=(--tls --cacert /etc/redis-tls/ca.crt)
  fi
  is_retryable_output() {
    echo "$1" | grep -Eiq "READONLY|LOADING|MASTERDOWN|NOREPLICAS|timeout|temporarily unavailable|connection reset|connection refused|i/o timeout|No such master"
  }

  run_sentinel() {
    if [ -n "$SENTINEL_PASSWORD" ]; then
      REDISCLI_AUTH="$SENTINEL_PASSWORD" "${sentinel_base[@]}" "$@"
      return
    fi
    "${sentinel_base[@]}" "$@"
  }

  resolve_master_endpoint() {
    local start output rc host port
    start=$SECONDS
    while true; do
      set +e
      output="$(run_sentinel SENTINEL get-master-addr-by-name "$MASTER_NAME" 2>&1)"
      rc=$?
      set -e
      if [ "$rc" -eq 0 ]; then
        host="$(printf "%s\n" "$output" | sed -n "1p" | tr -d "\r")"
        port="$(printf "%s\n" "$output" | sed -n "2p" | tr -d "\r")"
        if [ -n "$host" ] && [ -n "$port" ]; then
          printf "%s\n%s\n" "$host" "$port"
          return 0
        fi
      fi
      if is_retryable_output "$output" && [ $((SECONDS - start)) -lt 300 ]; then
        sleep 2
        continue
      fi
      echo "$output" >&2
      return 1
    done
  }

  run_redis() {
    local start endpoint master_host master_port output rc
    start=$SECONDS
    while true; do
      endpoint="$(resolve_master_endpoint)"
      master_host="$(printf "%s\n" "$endpoint" | sed -n "1p")"
      master_port="$(printf "%s\n" "$endpoint" | sed -n "2p")"
      redis_base=(redis-cli -h "$master_host" -p "$master_port" --raw)
      if [ "$USE_TLS" = "1" ]; then
        redis_base+=(--tls --cacert /etc/redis-tls/ca.crt)
      fi
      run_redis_cli() {
        if [ -n "$REDIS_PASSWORD" ]; then
          REDISCLI_AUTH="$REDIS_PASSWORD" "${redis_base[@]}" "$@"
          return
        fi
        "${redis_base[@]}" "$@"
      }

      set +e
      output="$(run_redis_cli "$@" 2>&1)"
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

  if [ "$action" = "count" ]; then
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

  sample_indices=(0 1 2 10 50 100 199)
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
