#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-redis-e2e}"
name="${2:-redis-cluster}"
redis_password="${3:-}"

redis_cli_auth=()
if [ -n "$redis_password" ]; then
  redis_cli_auth=(-a "$redis_password")
fi

labels="app.kubernetes.io/instance=${name},app.kubernetes.io/component=redis"
shards="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.cluster.shards}')"
replicas_per_shard="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.spec.cluster.replicasPerShard}')"
expected_pods=$((shards * (replicas_per_shard + 1)))

deadline=$((SECONDS + 600))
while true; do
  current_pods="$(kubectl -n "$namespace" get pods -l "$labels" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$current_pods" -ge "$expected_pods" ]; then
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting for redis pods to be created: got $current_pods expected $expected_pods"
    exit 1
  fi
  sleep 2
done

kubectl -n "$namespace" wait --for=condition=Ready pod -l "$labels" --timeout=600s

cluster_info=""
deadline=$((SECONDS + 300))
while true; do
  if cluster_info="$(kubectl -n "$namespace" exec "${name}-shard-0-0" -- redis-cli "${redis_cli_auth[@]}" CLUSTER INFO 2>/dev/null)"; then
    cluster_state="$(printf '%s\n' "$cluster_info" | awk -F: '/^cluster_state:/{print $2}' | tr -d '\r')"
    cluster_slots_assigned="$(printf '%s\n' "$cluster_info" | awk -F: '/^cluster_slots_assigned:/{print $2}' | tr -d '\r')"
    if [ "$cluster_state" = "ok" ] && [ "$cluster_slots_assigned" = "16384" ]; then
      break
    fi
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timeout waiting cluster info healthy"
    printf '%s\n' "$cluster_info"
    exit 1
  fi
  sleep 2
done

known_nodes="$(printf '%s\n' "$cluster_info" | awk -F: '/^cluster_known_nodes:/{print $2}' | tr -d '\r')"
if [ "$known_nodes" -lt 6 ]; then
  echo "unexpected cluster_known_nodes: $known_nodes"
  exit 1
fi

cluster_size="$(printf '%s\n' "$cluster_info" | awk -F: '/^cluster_size:/{print $2}' | tr -d '\r')"
if [ "$cluster_size" -ne 3 ]; then
  echo "unexpected cluster_size: $cluster_size"
  exit 1
fi

nodes="$(kubectl -n "$namespace" exec "${name}-shard-0-0" -- redis-cli "${redis_cli_auth[@]}" CLUSTER NODES)"
master_count="$(printf '%s\n' "$nodes" | awk '$3 ~ /master/ {count++} END {print count+0}')"
replica_count="$(printf '%s\n' "$nodes" | awk '$3 ~ /slave|replica/ {count++} END {print count+0}')"
if [ "$master_count" -ne 3 ]; then
  echo "unexpected master count: $master_count"
  exit 1
fi
if [ "$replica_count" -ne 3 ]; then
  echo "unexpected replica count: $replica_count"
  exit 1
fi

endpoint="$(kubectl -n "$namespace" get redis "$name" -o jsonpath='{.status.endpoint}')"
if [ -z "$endpoint" ]; then
  echo "status.endpoint is empty"
  exit 1
fi

seed_count="$(printf '%s' "$endpoint" | awk -F, '{print NF}')"
if [ "$seed_count" -ne 3 ]; then
  echo "unexpected endpoint seed count: $seed_count endpoint=$endpoint"
  exit 1
fi

echo "cluster assertions passed"
