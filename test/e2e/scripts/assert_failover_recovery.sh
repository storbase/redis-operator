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
quorum="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.failover.quorum}')"

if [ -z "$expected_redis" ] || [ -z "$expected_sentinel" ]; then
  echo "failed to load failover replica settings from Redis CR"
  exit 1
fi
if [ -z "$quorum" ] || [ "$quorum" -lt 2 ]; then
  quorum=2
fi

sentinel_diag=""
sentinel_consensus_pod=""
sentinel_consensus_votes=0

redis_pod_name() {
  local ordinal="$1"
  echo "${name}-redis-${ordinal}"
}

redis_pod_host() {
  local pod="$1"
  echo "${pod}.${name}-redis-headless.${namespace}.svc.cluster.local"
}

sentinel_pod_name() {
  local ordinal="$1"
  echo "${name}-sentinel-${ordinal}"
}

sentinel_pod_host() {
  local pod="$1"
  echo "${pod}.${name}-sentinel.${namespace}.svc.cluster.local"
}

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

normalize_sentinel_host_to_pod() {
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

detect_actual_master_once() {
  local masters=()
  local ordinal
  for ordinal in $(seq 0 $((expected_redis - 1))); do
    local pod
    pod="$(redis_pod_name "$ordinal")"
    local role
    role="$(query_replication_field "$pod" "role")"
    if [ "$role" = "master" ]; then
      masters+=("$pod")
    fi
  done
  if [ "${#masters[@]}" -eq 1 ]; then
    echo "${masters[0]}"
    return 0
  fi
  return 1
}

detect_actual_master_with_retry() {
  local timeout="$1"
  local start=$SECONDS
  while true; do
    local pod
    pod="$(detect_actual_master_once || true)"
    if [ -n "$pod" ]; then
      echo "$pod"
      return 0
    fi
    if [ $((SECONDS - start)) -ge "$timeout" ]; then
      echo "timeout detecting a single actual master pod"
      return 1
    fi
    sleep 2
  done
}

add_vote() {
  local pod="$1"
  local idx
  for idx in "${!vote_pods[@]}"; do
    if [ "${vote_pods[$idx]}" = "$pod" ]; then
      vote_counts[$idx]=$((vote_counts[$idx] + 1))
      return
    fi
  done
  vote_pods+=("$pod")
  vote_counts+=(1)
}

collect_sentinel_consensus() {
  local vote_pods=()
  local vote_counts=()
  local lines=()
  local ordinal

  for ordinal in $(seq 0 $((expected_sentinel - 1))); do
    local pod
    pod="$(sentinel_pod_name "$ordinal")"
    local host
    host="$(sentinel_pod_host "$pod")"
    local endpoint
    endpoint="$(kubectl -n "$namespace" exec "$pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$host" -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL get-master-addr-by-name "$master_name" 2>/dev/null || true)"
    local reported_host
    local reported_port
    reported_host="$(trim_cr "$(printf '%s\n' "$endpoint" | sed -n '1p')")"
    reported_port="$(trim_cr "$(printf '%s\n' "$endpoint" | sed -n '2p')")"

    if [ -z "$reported_host" ] || [ -z "$reported_port" ]; then
      lines+=("${pod} => unresolved")
      continue
    fi

    local mapped_pod
    mapped_pod="$(normalize_sentinel_host_to_pod "$reported_host" || true)"
    if [ -n "$mapped_pod" ]; then
      add_vote "$mapped_pod"
      lines+=("${pod} => ${reported_host}:${reported_port} -> ${mapped_pod}")
    else
      lines+=("${pod} => ${reported_host}:${reported_port} -> invalid-host")
    fi
  done

  sentinel_consensus_pod=""
  sentinel_consensus_votes=0
  sentinel_diag="$(printf '%s\n' "${lines[@]}")"

  local idx
  for idx in "${!vote_pods[@]}"; do
    if [ "${vote_counts[$idx]}" -gt "$sentinel_consensus_votes" ]; then
      sentinel_consensus_votes="${vote_counts[$idx]}"
      sentinel_consensus_pod="${vote_pods[$idx]}"
    fi
  done
}

assert_sentinel_master_matches_actual() {
  local stage="$1"
  local timeout="$2"
  local start=$SECONDS
  local last_actual=""
  local last_consensus=""
  local last_votes=0
  local last_diag=""

  while true; do
    local actual
    actual="$(detect_actual_master_once || true)"
    collect_sentinel_consensus

    last_actual="$actual"
    last_consensus="$sentinel_consensus_pod"
    last_votes="$sentinel_consensus_votes"
    last_diag="$sentinel_diag"

    if [ -n "$actual" ] && [ -n "$sentinel_consensus_pod" ] && [ "$sentinel_consensus_votes" -ge "$quorum" ] && [ "$sentinel_consensus_pod" = "$actual" ]; then
      echo "sentinel and actual master match at ${stage}: pod=${actual} votes=${sentinel_consensus_votes}/${expected_sentinel}"
      return 0
    fi

    if [ $((SECONDS - start)) -ge "$timeout" ]; then
      echo "timeout waiting sentinel/actual master alignment at ${stage}"
      echo "actual master: ${last_actual:-<none>}"
      echo "sentinel consensus: ${last_consensus:-<none>} votes=${last_votes}/${expected_sentinel} quorum=${quorum}"
      echo "sentinel diagnostics:"
      printf '%s\n' "$last_diag"
      return 1
    fi
    sleep 2
  done
}

wait_for_failover_pods_ready() {
  local deadline=$((SECONDS + 600))
  while true; do
    local redis_count
    local sentinel_count
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
}

assert_old_master_rejoined_as_replica() {
  local old_master_pod="$1"
  local deadline=$((SECONDS + 300))
  while true; do
    local role
    local link_status
    role="$(query_replication_field "$old_master_pod" "role")"
    link_status="$(query_replication_field "$old_master_pod" "master_link_status")"
    if { [ "$role" = "slave" ] || [ "$role" = "replica" ]; } && [ "$link_status" = "up" ]; then
      echo "old master pod ${old_master_pod} rejoined as replica with link status up"
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "timeout waiting old master pod to rejoin as replica: pod=${old_master_pod} role=${role:-<empty>} master_link_status=${link_status:-<empty>}"
      return 1
    fi
    sleep 2
  done
}

assert_final_topology_roles() {
  local deadline=$((SECONDS + 180))
  while true; do
    local master_count=0
    local replica_count=0
    local unknown_count=0
    local ordinal
    for ordinal in $(seq 0 $((expected_redis - 1))); do
      local pod
      pod="$(redis_pod_name "$ordinal")"
      local role
      role="$(query_replication_field "$pod" "role")"
      case "$role" in
        master)
          master_count=$((master_count + 1))
          ;;
        slave|replica)
          replica_count=$((replica_count + 1))
          ;;
        *)
          unknown_count=$((unknown_count + 1))
          ;;
      esac
    done
    if [ "$master_count" -eq 1 ] && [ "$replica_count" -eq $((expected_redis - 1)) ] && [ "$unknown_count" -eq 0 ]; then
      echo "final topology is healthy: master=1 replica=$replica_count"
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "unexpected final topology roles: master=$master_count replica=$replica_count unknown=$unknown_count expected_replica=$((expected_redis - 1))"
      return 1
    fi
    sleep 2
  done
}

wait_for_failover_pods_ready
assert_sentinel_master_matches_actual "baseline" 180

old_master_pod="$(detect_actual_master_with_retry 180)"
old_master_uid="$(kubectl -n "$namespace" get pod "$old_master_pod" -o jsonpath='{.metadata.uid}')"
echo "deleting current master pod: ${old_master_pod} uid=${old_master_uid}"
kubectl -n "$namespace" delete pod "$old_master_pod" --wait=false >/dev/null

deadline=$((SECONDS + 300))
while true; do
  new_uid="$(kubectl -n "$namespace" get pod "$old_master_pod" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
  if [ -n "$new_uid" ] && [ "$new_uid" != "$old_master_uid" ]; then
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting old master pod to be recreated: pod=${old_master_pod}"
    exit 1
  fi
  sleep 2
done

kubectl -n "$namespace" wait --for=condition=Ready "pod/${old_master_pod}" --timeout=300s

new_master_pod=""
deadline=$((SECONDS + 240))
while true; do
  candidate_master="$(detect_actual_master_once || true)"
  if [ -n "$candidate_master" ] && [ "$candidate_master" != "$old_master_pod" ]; then
    new_master_pod="$candidate_master"
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting new master election after deleting ${old_master_pod}"
    exit 1
  fi
  sleep 2
done
echo "new master elected: ${new_master_pod}"

assert_sentinel_master_matches_actual "post-failover" 180
assert_old_master_rejoined_as_replica "$old_master_pod"
assert_final_topology_roles
assert_sentinel_master_matches_actual "final" 180

echo "failover recovery assertions passed"
