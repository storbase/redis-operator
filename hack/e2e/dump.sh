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
kubectl -n "$namespace" get pods -o wide >"${artifact_dir}/pods.txt" 2>&1 || true
kubectl -n "$namespace" get pods -o yaml >"${artifact_dir}/pods.yaml" 2>&1 || true
kubectl -n "$namespace" get jobs -o wide >"${artifact_dir}/jobs.txt" 2>&1 || true
kubectl -n "$namespace" get jobs -o yaml >"${artifact_dir}/jobs.yaml" 2>&1 || true
kubectl -n "$namespace" get statefulsets -o wide >"${artifact_dir}/statefulsets.txt" 2>&1 || true
kubectl -n "$namespace" get statefulsets -o yaml >"${artifact_dir}/statefulsets.yaml" 2>&1 || true
kubectl -n "$namespace" get pvc -o wide >"${artifact_dir}/pvcs.txt" 2>&1 || true
kubectl -n "$namespace" get service -o wide >"${artifact_dir}/services.txt" 2>&1 || true
kubectl -n "$namespace" get redis -o yaml >"${artifact_dir}/redis-crs.yaml" 2>&1 || true
kubectl -n "$namespace" get redis -o jsonpath='{range .items[*]}{.metadata.name}{"\tphase="}{.status.clusterScale.phase}{"\tfrom="}{.status.clusterScale.fromShards}{"\tto="}{.status.clusterScale.toShards}{"\treason="}{.status.reason}{"\tlastError="}{.status.clusterScale.lastError}{"\n"}{end}' >"${artifact_dir}/redis-status-summary.txt" 2>&1 || true
kubectl -n "$namespace" get events --sort-by=.lastTimestamp >"${artifact_dir}/events.txt" 2>&1 || true
kubectl -n "$namespace" get jobs -l app.kubernetes.io/component=cluster-scale -o yaml >"${artifact_dir}/cluster-scale-jobs.yaml" 2>&1 || true
kubectl -n "$namespace" get pods -l app.kubernetes.io/component=cluster-scale -o yaml >"${artifact_dir}/cluster-scale-pods.yaml" 2>&1 || true
kubectl -n "$operator_namespace" get all -o wide >"${artifact_dir}/operator-all.txt" 2>&1 || true
kubectl -n "$operator_namespace" describe "deployment/${operator_deployment}" >"${artifact_dir}/operator-deployment.describe.txt" 2>&1 || true
kubectl -n "$operator_namespace" logs "deployment/${operator_deployment}" -c manager --tail=-1 >"${artifact_dir}/operator-manager.log" 2>&1 || true
kubectl -n "$operator_namespace" logs "deployment/${operator_deployment}" -c manager --tail=-1 --previous >"${artifact_dir}/operator-manager.previous.log" 2>&1 || true

while IFS= read -r pod; do
  safe_name="${pod//\//_}"
  kubectl -n "$namespace" describe "$pod" >"${artifact_dir}/${safe_name}.describe.txt" 2>&1 || true
  kubectl -n "$namespace" logs "$pod" --all-containers=true --tail=-1 >"${artifact_dir}/${safe_name}.logs.txt" 2>&1 || true
done < <(kubectl -n "$namespace" get pods -o name 2>/dev/null || true)

while IFS= read -r job; do
  safe_name="${job//\//_}"
  kubectl -n "$namespace" describe "$job" >"${artifact_dir}/${safe_name}.describe.txt" 2>&1 || true
  kubectl -n "$namespace" logs "$job" --tail=-1 >"${artifact_dir}/${safe_name}.logs.txt" 2>&1 || true

  job_name="${job#job/}"
  while IFS= read -r pod; do
    pod_safe_name="${pod//\//_}"
    kubectl -n "$namespace" describe "$pod" >"${artifact_dir}/${safe_name}.${pod_safe_name}.describe.txt" 2>&1 || true
    kubectl -n "$namespace" logs "$pod" --all-containers=true --tail=-1 >"${artifact_dir}/${safe_name}.${pod_safe_name}.logs.txt" 2>&1 || true
  done < <(kubectl -n "$namespace" get pods -l "job-name=${job_name}" -o name 2>/dev/null || true)
done < <(kubectl -n "$namespace" get jobs -o name 2>/dev/null || true)
