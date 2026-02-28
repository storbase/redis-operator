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

redis_labels="app.kubernetes.io/instance=${name},app.kubernetes.io/component=redis"
sentinel_labels="app.kubernetes.io/instance=${name},app.kubernetes.io/component=sentinel"
expected_redis="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.redisReplicas}')"
expected_sentinel="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.sentinelReplicas}')"

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
master_host=""
master_port=""
deadline=$((SECONDS + 300))
while true; do
  ckquorum_output="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli -p 26379 "${sentinel_cli_auth[@]}" SENTINEL CKQUORUM "$master_name" 2>/dev/null || true)"
  if printf '%s\n' "$ckquorum_output" | grep -Eiq '(^OK|usable Sentinels)'; then
    master_host="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL get-master-addr-by-name "$master_name" | sed -n '1p' | tr -d '\r')"
    master_port="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL get-master-addr-by-name "$master_name" | sed -n '2p' | tr -d '\r')"
    if [ -n "$master_host" ] && [ -n "$master_port" ]; then
      break
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
for ordinal in $(seq 0 $((expected_redis - 1))); do
  candidate="${name}-redis-${ordinal}"
  candidate_role="$(kubectl -n "$namespace" exec "$candidate" -- redis-cli "${redis_cli_auth[@]}" --raw INFO replication | sed -n 's/^role://p' | tr -d '\r' | head -n1 || true)"
  if [ "$candidate_role" = "master" ]; then
    master_pod="$candidate"
    break
  fi
done
if [ -z "$master_pod" ]; then
  echo "failed to determine master pod for failover assertions"
  exit 1
fi

role_info="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli -h "$master_host" -p "$master_port" "${redis_cli_auth[@]}" INFO replication)"

printf '%s\n' "$role_info" | grep -q "role:master"

key="${name}:steady:e2e"
kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli -h "$master_host" -p "$master_port" "${redis_cli_auth[@]}" SET "$key" "ok" >/dev/null
value="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli -h "$master_host" -p "$master_port" "${redis_cli_auth[@]}" GET "$key" | tr -d '\r')"
if [ "$value" != "ok" ]; then
  echo "unexpected value: $value"
  exit 1
fi

echo "failover steady-state assertions passed"
