#!/usr/bin/env bash
set -euo pipefail

cluster_name="${E2E_KIND_CLUSTER:?E2E_KIND_CLUSTER is required}"
namespace="${E2E_NAMESPACE:?E2E_NAMESPACE is required}"
artifact_dir="${E2E_ARTIFACT_DIR_PR:?E2E_ARTIFACT_DIR_PR is required}"
chainsaw_dir="${E2E_CHAINSAW_DIR:?E2E_CHAINSAW_DIR is required}"
chainsaw_config="${E2E_CHAINSAW_CONFIG:?E2E_CHAINSAW_CONFIG is required}"
chainsaw_suites="${E2E_CHAINSAW_SUITES:-cluster,failover}"
chainsaw_skip_delete="${E2E_CHAINSAW_SKIP_DELETE:-true}"
chainsaw_report_name="${E2E_CHAINSAW_REPORT_NAME:-chainsaw-pr}"
image="${E2E_IMG:?E2E_IMG is required}"
kubectl_bin="${KUBECTL_BIN:?KUBECTL_BIN is required}"
container_tool="${CONTAINER_TOOL_BIN:?CONTAINER_TOOL_BIN is required}"
helm_bin="${HELM_BIN:-helm}"
helm_release="${E2E_HELM_RELEASE:-redis-operator}"
operator_namespace="${E2E_OPERATOR_NAMESPACE:-redis-operator-system}"
operator_deployment="${E2E_OPERATOR_DEPLOYMENT:-redis-operator-controller-manager}"

mkdir -p "$artifact_dir"

IFS=',' read -r -a suites <<<"$chainsaw_suites"
chainsaw_suite_dirs=()
for raw_suite in "${suites[@]}"; do
  suite="${raw_suite//[[:space:]]/}"
  if [ -z "$suite" ]; then
    continue
  fi
  case "$suite" in
    cluster|failover|failover-external)
      chainsaw_suite_dirs+=(--test-dir "${chainsaw_dir}/${suite}")
      ;;
    *)
      echo "unsupported E2E_CHAINSAW_SUITES entry: $suite" >&2
      exit 1
      ;;
  esac
done

if [ "${#chainsaw_suite_dirs[@]}" -eq 0 ]; then
  echo "E2E_CHAINSAW_SUITES must include at least one suite (cluster, failover, failover-external)" >&2
  exit 1
fi

if [[ "$image" == *:* ]]; then
  image_repository="${image%:*}"
  image_tag="${image##*:}"
else
  image_repository="$image"
  image_tag="latest"
fi

"$container_tool" build -t "$image" .
kind load docker-image "$image" --name "$cluster_name"

"$kubectl_bin" apply -f charts/redis-operator/crds

"$helm_bin" upgrade --install "$helm_release" charts/redis-operator \
  --namespace "$operator_namespace" \
  --create-namespace \
  --reset-values \
  --set-string image.repository="$image_repository" \
  --set-string image.tag="$image_tag" \
  --wait \
  --timeout=180s

"$kubectl_bin" -n "$operator_namespace" rollout status "deployment/${operator_deployment}" --timeout=180s

"$kubectl_bin" delete namespace "$namespace" --ignore-not-found=true
"$kubectl_bin" create namespace "$namespace"

set +e
chainsaw_cmd=(
  test
  --config "$chainsaw_config"
  --test-file chainsaw-test.yaml
  "${chainsaw_suite_dirs[@]}"
  --kube-context "kind-${cluster_name}"
  --report-format JUNIT-TEST
  --report-name "$chainsaw_report_name"
  --report-path "$artifact_dir"
)
if [ "$chainsaw_skip_delete" = "true" ]; then
  chainsaw_cmd+=(--skip-delete)
fi
chainsaw "${chainsaw_cmd[@]}"
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  E2E_OPERATOR_NAMESPACE="$operator_namespace" \
  E2E_OPERATOR_DEPLOYMENT="$operator_deployment" \
  ./hack/e2e/dump.sh "$artifact_dir" "$namespace"
  kind export logs "${artifact_dir}/kind-logs" --name "$cluster_name" || true
fi

exit "$rc"
