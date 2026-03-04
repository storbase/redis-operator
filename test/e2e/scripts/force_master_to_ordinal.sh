#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-redis-e2e}"
name="${2:-redis-failover}"
master_name="${3:-mymaster}"
target_ordinal="${4:-3}"
redis_password="${5:-}"
sentinel_password="${6:-}"

if ! [[ "$target_ordinal" =~ ^[0-9]+$ ]]; then
  echo "target ordinal must be a non-negative integer: ${target_ordinal}"
  exit 1
fi

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
if [ -z "$quorum" ] || [ "$quorum" -lt 2 ]; then
  quorum=2
fi

if [ "$target_ordinal" -ge "$expected_redis" ]; then
  echo "target ordinal ${target_ordinal} must be less than redis replicas ${expected_redis}"
  exit 1
fi

target_pod="${name}-redis-${target_ordinal}"
priorities_applied=false

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

sentinel_pod_name() {
  local ordinal="$1"
  echo "${name}-sentinel-${ordinal}"
}

sentinel_pod_host() {
  local pod="$1"
  echo "${pod}.${name}-sentinel.${namespace}.svc.cluster.local"
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

flag_contains() {
  local flags="$1"
  local expected="$2"
  case ",${flags}," in
    *",${expected},"*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

query_replication_field() {
  local pod="$1"
  local field="$2"
  local host
  host="$(redis_pod_host "$pod")"
  kubectl -n "$namespace" exec "$pod" -- redis-cli "${redis_tls_flags[@]}" -h "$host" -p 6379 "${redis_cli_auth[@]}" --raw INFO replication 2>/dev/null \
    | sed -n "s/^${field}://p" | tr -d '\r' | head -n1 || true
}

sentinel_target_replica_flags() {
  local sentinel_pod="$1"
  local sentinel_host
  sentinel_host="$(sentinel_pod_host "$sentinel_pod")"
  local raw
  raw="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$sentinel_host" -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL replicas "$master_name" 2>/dev/null || true)"
  raw="$(trim_cr "$raw")"
  if [ -z "$raw" ]; then
    return 1
  fi

  local target_host
  target_host="$(redis_pod_host "$target_pod")"
  local target_name="${target_host}:6379"

  local key=""
  local current_name=""
  while IFS= read -r line; do
    case "$key" in
      name)
        current_name="$line"
        ;;
      flags)
        if [ "$current_name" = "$target_name" ] || [ "$current_name" = "$target_host" ]; then
          printf '%s' "$line"
          return 0
        fi
        ;;
    esac
    key="$line"
  done <<EOF
$raw
EOF
  return 1
}

wait_for_target_replica_ready_for_failover() {
  local timeout="$1"
  local start=$SECONDS
  local last_role=""
  local last_link=""
  local last_master=""
  local last_sentinel_diag=""

  while true; do
    last_role="$(query_replication_field "$target_pod" "role")"
    last_link="$(query_replication_field "$target_pod" "master_link_status")"

    local sentinels_ready=true
    local ordinal
    local diag_lines=()
    for ordinal in $(seq 0 $((expected_sentinel - 1))); do
      local pod
      pod="$(sentinel_pod_name "$ordinal")"
      local flags
      flags="$(sentinel_target_replica_flags "$pod" || true)"
      if [ -z "$flags" ]; then
        sentinels_ready=false
        diag_lines+=("${pod} => target replica not discovered")
        continue
      fi
      if ! flag_contains "$flags" "slave" && ! flag_contains "$flags" "replica"; then
        sentinels_ready=false
        diag_lines+=("${pod} => flags=${flags} (not replica)")
        continue
      fi
      if flag_contains "$flags" "s_down" || flag_contains "$flags" "o_down" || flag_contains "$flags" "disconnected"; then
        sentinels_ready=false
        diag_lines+=("${pod} => flags=${flags} (down/disconnected)")
        continue
      fi
      diag_lines+=("${pod} => flags=${flags}")
    done
    last_sentinel_diag="$(printf '%s\n' "${diag_lines[@]}")"

    last_master="$(detect_actual_master_once || true)"
    collect_sentinel_consensus
    local master_aligned=false
    if [ -n "$last_master" ] && [ "$last_master" = "$sentinel_consensus_pod" ] && [ "$sentinel_consensus_votes" -ge "$quorum" ]; then
      master_aligned=true
    fi

    if { [ "$last_role" = "slave" ] || [ "$last_role" = "replica" ]; } \
      && [ "$last_link" = "up" ] \
      && [ "$sentinels_ready" = "true" ] \
      && [ "$master_aligned" = "true" ]; then
      echo "target replica is ready for failover: ${target_pod} role=${last_role} link=${last_link} current_master=${last_master}"
      return 0
    fi

    if [ $((SECONDS - start)) -ge "$timeout" ]; then
      echo "timeout waiting target replica readiness before manual failover: ${target_pod}"
      echo "target replication status: role=${last_role:-<empty>} master_link_status=${last_link:-<empty>}"
      echo "actual master: ${last_master:-<none>}"
      echo "sentinel consensus: ${sentinel_consensus_pod:-<none>} votes=${sentinel_consensus_votes}/${expected_sentinel} quorum=${quorum}"
      echo "target replica visibility:"
      printf '%s\n' "$last_sentinel_diag"
      return 1
    fi
    sleep 2
  done
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
  vote_pods=()
  vote_counts=()
  sentinel_diag=""
  sentinel_consensus_pod=""
  sentinel_consensus_votes=0

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
    reported_host="$(trim_cr "$(printf '%s\n' "$endpoint" | sed -n '1p')")"
    local reported_port
    reported_port="$(trim_cr "$(printf '%s\n' "$endpoint" | sed -n '2p')")"
    if [ -z "$reported_host" ] || [ -z "$reported_port" ]; then
      lines+=("${pod} => unresolved")
      continue
    fi
    local mapped_pod
    mapped_pod="$(normalize_redis_host_to_pod "$reported_host" || true)"
    if [ -n "$mapped_pod" ]; then
      add_vote "$mapped_pod"
      lines+=("${pod} => ${reported_host}:${reported_port} -> ${mapped_pod}")
    else
      lines+=("${pod} => ${reported_host}:${reported_port} -> invalid-host")
    fi
  done

  sentinel_diag="$(printf '%s\n' "${lines[@]}")"
  local idx
  for idx in "${!vote_pods[@]}"; do
    if [ "${vote_counts[$idx]}" -gt "$sentinel_consensus_votes" ]; then
      sentinel_consensus_votes="${vote_counts[$idx]}"
      sentinel_consensus_pod="${vote_pods[$idx]}"
    fi
  done
}

set_replica_priority() {
  local pod="$1"
  local priority="$2"
  local host
  host="$(redis_pod_host "$pod")"
  local output
  output="$(kubectl -n "$namespace" exec "$pod" -- redis-cli "${redis_tls_flags[@]}" -h "$host" -p 6379 "${redis_cli_auth[@]}" --raw CONFIG SET replica-priority "$priority" 2>/dev/null || true)"
  output="$(trim_cr "$output")"
  if [ "$output" != "OK" ]; then
    echo "failed to set replica-priority=${priority} on ${pod}: ${output:-<empty>}"
    return 1
  fi
}

restore_priorities() {
  if [ "$priorities_applied" != "true" ]; then
    return
  fi
  local ordinal
  for ordinal in $(seq 0 $((expected_redis - 1))); do
    local pod
    pod="$(redis_pod_name "$ordinal")"
    set_replica_priority "$pod" 100 >/dev/null 2>&1 || true
  done
}

wait_for_failover_pods_ready() {
  local deadline=$((SECONDS + 600))
  while true; do
    local redis_count
    redis_count="$(kubectl -n "$namespace" get pods -l "$redis_labels" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
    local sentinel_count
    sentinel_count="$(kubectl -n "$namespace" get pods -l "$sentinel_labels" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
    if [ "$redis_count" -ge "$expected_redis" ] && [ "$sentinel_count" -ge "$expected_sentinel" ]; then
      break
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "timeout waiting failover pods to be created: redis=${redis_count}/${expected_redis} sentinel=${sentinel_count}/${expected_sentinel}"
      exit 1
    fi
    sleep 2
  done
  kubectl -n "$namespace" wait --for=condition=Ready pod -l "$redis_labels" --timeout=600s
  kubectl -n "$namespace" wait --for=condition=Ready pod -l "$sentinel_labels" --timeout=600s
}

trigger_failover() {
  local sentinel_pod
  sentinel_pod="$(sentinel_pod_name 0)"
  local sentinel_host
  sentinel_host="$(sentinel_pod_host "$sentinel_pod")"
  local output
  output="$(kubectl -n "$namespace" exec "$sentinel_pod" -- redis-cli "${sentinel_tls_flags[@]}" -h "$sentinel_host" -p 26379 "${sentinel_cli_auth[@]}" --raw SENTINEL FAILOVER "$master_name" 2>/dev/null || true)"
  output="$(trim_cr "$output")"
  if [ "$output" != "OK" ]; then
    echo "sentinel failover trigger returned: ${output:-<empty>}"
    return 1
  fi
  return 0
}

wait_for_target_master() {
  local timeout="$1"
  local start=$SECONDS
  while true; do
    local actual_master
    actual_master="$(detect_actual_master_once || true)"
    collect_sentinel_consensus
    if [ "$actual_master" = "$target_pod" ] && [ "$sentinel_consensus_pod" = "$target_pod" ] && [ "$sentinel_consensus_votes" -ge "$quorum" ]; then
      echo "target master reached: ${target_pod} votes=${sentinel_consensus_votes}/${expected_sentinel}"
      return 0
    fi
    if [ $((SECONDS - start)) -ge "$timeout" ]; then
      echo "timeout waiting target master ${target_pod}"
      echo "actual master: ${actual_master:-<none>}"
      echo "sentinel consensus: ${sentinel_consensus_pod:-<none>} votes=${sentinel_consensus_votes}/${expected_sentinel} quorum=${quorum}"
      echo "sentinel diagnostics:"
      printf '%s\n' "$sentinel_diag"
      return 1
    fi
    sleep 2
  done
}

trap restore_priorities EXIT

wait_for_failover_pods_ready

current_master="$(detect_actual_master_with_retry 180)"
if [ "$current_master" = "$target_pod" ]; then
  collect_sentinel_consensus
  if [ "$sentinel_consensus_pod" = "$target_pod" ] && [ "$sentinel_consensus_votes" -ge "$quorum" ]; then
    echo "target pod is already the master: ${target_pod}"
    exit 0
  fi
fi

wait_for_target_replica_ready_for_failover 240

for ordinal in $(seq 0 $((expected_redis - 1))); do
  pod="$(redis_pod_name "$ordinal")"
  if [ "$pod" = "$target_pod" ]; then
    set_replica_priority "$pod" 10
  else
    set_replica_priority "$pod" 200
  fi
done
priorities_applied=true

attempt=0
while true; do
  attempt=$((attempt + 1))
  if trigger_failover; then
    break
  fi
  if [ "$attempt" -ge 5 ]; then
    echo "failed to trigger sentinel failover after ${attempt} attempts"
    exit 1
  fi
  sleep 2
done

wait_for_target_master 240
echo "master moved to target ordinal ${target_ordinal}"
