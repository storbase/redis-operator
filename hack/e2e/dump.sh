#!/usr/bin/env bash
set -euo pipefail

artifact_dir="${1:-test/e2e/artifacts}"
namespace="${2:-redis-e2e}"
operator_namespace="${E2E_OPERATOR_NAMESPACE:-redis-operator-system}"
operator_deployment="${E2E_OPERATOR_DEPLOYMENT:-redis-operator-controller-manager}"

mkdir -p "$artifact_dir"

kubectl get nodes -o wide >"${artifact_dir}/nodes.txt" 2>&1 || true
kubectl get ns >"${artifact_dir}/namespaces.txt" 2>&1 || true
kubectl -n "$namespace" get all -o wide >"${artifact_dir}/workload-all.txt" 2>&1 || true
kubectl -n "$namespace" get redis -o yaml >"${artifact_dir}/redis-crs.yaml" 2>&1 || true
kubectl -n "$namespace" get events --sort-by=.lastTimestamp >"${artifact_dir}/events.txt" 2>&1 || true
kubectl -n "$operator_namespace" get all -o wide >"${artifact_dir}/operator-all.txt" 2>&1 || true
kubectl -n "$operator_namespace" logs "deployment/${operator_deployment}" -c manager --tail=-1 >"${artifact_dir}/operator-manager.log" 2>&1 || true

while IFS= read -r pod; do
  safe_name="${pod//\//_}"
  kubectl -n "$namespace" describe "$pod" >"${artifact_dir}/${safe_name}.describe.txt" 2>&1 || true
  kubectl -n "$namespace" logs "$pod" --all-containers=true --tail=-1 >"${artifact_dir}/${safe_name}.logs.txt" 2>&1 || true
done < <(kubectl -n "$namespace" get pods -o name 2>/dev/null || true)
