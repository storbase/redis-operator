#!/usr/bin/env bash
set -euo pipefail

cluster_name="${E2E_KIND_CLUSTER:?E2E_KIND_CLUSTER is required}"
namespace="${E2E_NAMESPACE:?E2E_NAMESPACE is required}"
artifact_dir="${E2E_ARTIFACT_DIR_PR:?E2E_ARTIFACT_DIR_PR is required}"
chainsaw_dir="${E2E_CHAINSAW_DIR:?E2E_CHAINSAW_DIR is required}"
chainsaw_config="${E2E_CHAINSAW_CONFIG:?E2E_CHAINSAW_CONFIG is required}"
image="${E2E_IMG:?E2E_IMG is required}"
kubectl_bin="${KUBECTL_BIN:?KUBECTL_BIN is required}"
container_tool="${CONTAINER_TOOL_BIN:?CONTAINER_TOOL_BIN is required}"

mkdir -p "$artifact_dir"
operator_namespace="redis-operator-system"
manager_kustomization="config/manager/kustomization.yaml"
manager_kustomization_backup="$(mktemp)"
cp "$manager_kustomization" "$manager_kustomization_backup"

cleanup() {
  cp "$manager_kustomization_backup" "$manager_kustomization"
  rm -f "$manager_kustomization_backup"
}
trap cleanup EXIT

"$container_tool" build -t "$image" .
kind load docker-image "$image" --name "$cluster_name"

GOPROXY=https://goproxy.cn,direct make install
GOPROXY=https://goproxy.cn,direct make deploy IMG="$image"

"$kubectl_bin" -n "$operator_namespace" rollout status deployment/redis-operator-controller-manager --timeout=180s

"$kubectl_bin" delete namespace "$namespace" --ignore-not-found=true
"$kubectl_bin" create namespace "$namespace"

set +e
chainsaw test \
  --config "$chainsaw_config" \
  --test-file chainsaw-test.yaml \
  --test-dir "${chainsaw_dir}/cluster" \
  --test-dir "${chainsaw_dir}/failover" \
  --kube-context "kind-${cluster_name}" \
  --report-format JUNIT-TEST \
  --report-name chainsaw-pr \
  --report-path "$artifact_dir"
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  ./hack/e2e/dump.sh "$artifact_dir" "$namespace"
  kind export logs "${artifact_dir}/kind-logs" --name "$cluster_name" || true
fi

exit "$rc"
