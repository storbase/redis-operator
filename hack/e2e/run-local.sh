#!/usr/bin/env bash
set -euo pipefail

cluster_name="${E2E_KIND_CLUSTER:?E2E_KIND_CLUSTER is required}"
namespace="${E2E_NAMESPACE:?E2E_NAMESPACE is required}"
artifact_dir="${E2E_ARTIFACT_DIR_LOCAL:?E2E_ARTIFACT_DIR_LOCAL is required}"
chainsaw_dir="${E2E_CHAINSAW_DIR:?E2E_CHAINSAW_DIR is required}"
chainsaw_config="${E2E_CHAINSAW_CONFIG:?E2E_CHAINSAW_CONFIG is required}"
chainsaw_suites="${E2E_CHAINSAW_SUITES:-cluster,failover}"
chainsaw_skip_delete="${E2E_CHAINSAW_SKIP_DELETE:-true}"
chainsaw_report_name="${E2E_CHAINSAW_REPORT_NAME:-chainsaw-local}"
cluster_domain="${E2E_CLUSTER_DOMAIN:-cluster.local}"
dns_preflight="${E2E_DNS_PREFLIGHT:-true}"
ktctl_dns_host="${E2E_KTCTL_DNS_HOST:-127.0.0.1}"
ktctl_dns_port="${E2E_KTCTL_DNS_PORT:-10053}"
kubectl_bin="${KUBECTL_BIN:-kubectl}"
helm_bin="${HELM_BIN:-helm}"
chainsaw_bin="${CHAINSAW_BIN:-chainsaw}"
kind_bin="${KIND_BIN:-kind}"
helm_release="${E2E_HELM_RELEASE:-redis-operator}"
operator_namespace="${E2E_OPERATOR_NAMESPACE:-redis-operator-system}"
operator_deployment="${E2E_OPERATOR_DEPLOYMENT:-redis-operator-controller-manager}"

mkdir -p "$artifact_dir"

controller_pid=""
controller_runtime_pid=""
gomodcache="${GOMODCACHE:-$(go env GOMODCACHE)}"

IFS=',' read -r -a suites <<<"$chainsaw_suites"
chainsaw_suite_dirs=()
for raw_suite in "${suites[@]}"; do
  suite="${raw_suite//[[:space:]]/}"
  if [ -z "$suite" ]; then
    continue
  fi
  case "$suite" in
    cluster|cluster-external|failover|failover-external)
      chainsaw_suite_dirs+=(--test-dir "${chainsaw_dir}/${suite}")
      ;;
    *)
      echo "unsupported E2E_CHAINSAW_SUITES entry: $suite" >&2
      exit 1
      ;;
  esac
done

if [ "${#chainsaw_suite_dirs[@]}" -eq 0 ]; then
  echo "E2E_CHAINSAW_SUITES must include at least one suite (cluster, cluster-external, failover, failover-external)" >&2
  exit 1
fi

cleanup() {
  if [ -n "$controller_runtime_pid" ] && kill -0 "$controller_runtime_pid" >/dev/null 2>&1; then
    kill "$controller_runtime_pid" >/dev/null 2>&1 || true
    wait "$controller_runtime_pid" >/dev/null 2>&1 || true
  fi
  if [ -n "$controller_pid" ] && kill -0 "$controller_pid" >/dev/null 2>&1; then
    kill "$controller_pid" >/dev/null 2>&1 || true
    wait "$controller_pid" >/dev/null 2>&1 || true
  fi
  rm -f "${artifact_dir}/controller.pid" "${artifact_dir}/controller-runtime.pid"
}

trap cleanup EXIT

if ! pgrep -f "ktctl" >/dev/null 2>&1; then
  echo "No running 'ktctl connect' process was found." >&2
  echo "Run this command in another terminal first:" >&2
  echo "  sudo ktctl connect --context kind-${cluster_name} --namespace default --dnsMode localDNS:cluster --dnsPort 10053" >&2
  exit 1
fi
if [ "$dns_preflight" = "true" ] && command -v dig >/dev/null 2>&1; then
  dns_answer="$(
    dig +short +time=2 +tries=1 @"${ktctl_dns_host}" -p "${ktctl_dns_port}" \
      "kubernetes.default.svc.${cluster_domain}" 2>/dev/null | tr -d '\r' || true
  )"
  if [ -z "$dns_answer" ]; then
    echo "DNS preflight failed for kubernetes.default.svc.${cluster_domain}." >&2
    echo "Ensure ktctl is connected with '--dnsMode localDNS' and DNS ${ktctl_dns_host}:${ktctl_dns_port} is active." >&2
    exit 1
  fi
fi
echo "Using externally managed ktctl connect; keep it running during the e2e run." >"${artifact_dir}/ktctl.log"

"$kubectl_bin" apply -f charts/redis-operator/crds

"$helm_bin" upgrade --install "$helm_release" charts/redis-operator \
  --namespace "$operator_namespace" \
  --create-namespace \
  --reset-values \
  --set replicaCount=0 \
  --wait \
  --timeout=180s

"$kubectl_bin" -n "$operator_namespace" get "deployment/${operator_deployment}" >/dev/null
desired_replicas="$("$kubectl_bin" -n "$operator_namespace" get "deployment/${operator_deployment}" -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
if [ "$desired_replicas" != "0" ]; then
  echo "expected deployment/${operator_deployment} replicas=0, got '${desired_replicas:-unknown}'" >&2
  exit 1
fi

"$kubectl_bin" delete namespace "$namespace" --ignore-not-found=true
"$kubectl_bin" create namespace "$namespace"

if [ -f "${artifact_dir}/controller-runtime.pid" ]; then
  old_runtime_pid="$(cat "${artifact_dir}/controller-runtime.pid" 2>/dev/null || true)"
  if [ -n "$old_runtime_pid" ] && kill -0 "$old_runtime_pid" >/dev/null 2>&1; then
    kill "$old_runtime_pid" >/dev/null 2>&1 || true
    wait "$old_runtime_pid" >/dev/null 2>&1 || true
  fi
  rm -f "${artifact_dir}/controller-runtime.pid"
fi

if [ -f "${artifact_dir}/controller.pid" ]; then
  old_controller_pid="$(cat "${artifact_dir}/controller.pid" 2>/dev/null || true)"
  if [ -n "$old_controller_pid" ] && kill -0 "$old_controller_pid" >/dev/null 2>&1; then
    kill "$old_controller_pid" >/dev/null 2>&1 || true
    wait "$old_controller_pid" >/dev/null 2>&1 || true
  fi
  rm -f "${artifact_dir}/controller.pid"
fi

existing_8081_pid="$(lsof -tiTCP:8081 -sTCP:LISTEN 2>/dev/null | head -n1 || true)"
if [ -n "$existing_8081_pid" ]; then
  existing_cmd="$(ps -p "$existing_8081_pid" -o command= 2>/dev/null || true)"
  if printf '%s' "$existing_cmd" | grep -Eq '(redis-operator|/go-build/.*/main)'; then
    kill "$existing_8081_pid" >/dev/null 2>&1 || true
    sleep 1
  else
    echo "port 8081 is already in use by PID $existing_8081_pid ($existing_cmd)" >&2
    exit 1
  fi
fi

GOCACHE="$(pwd)/.cache/go-build" \
GOMODCACHE="$gomodcache" \
GODEBUG="${GODEBUG:-netdns=cgo}" \
go run ./cmd/main.go \
  --health-probe-bind-address=:8081 \
  --metrics-bind-address=:8443 >"${artifact_dir}/local-controller.log" 2>&1 &
controller_pid="$!"
echo "$controller_pid" >"${artifact_dir}/controller.pid"
sleep 5
if ! kill -0 "$controller_pid" >/dev/null 2>&1; then
  echo "local controller exited unexpectedly"
  tail -n 200 "${artifact_dir}/local-controller.log" || true
  exit 1
fi
controller_runtime_pid="$(lsof -tiTCP:8081 -sTCP:LISTEN 2>/dev/null | head -n1 || true)"
if [ -n "$controller_runtime_pid" ]; then
  echo "$controller_runtime_pid" >"${artifact_dir}/controller-runtime.pid"
fi

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
"$chainsaw_bin" "${chainsaw_cmd[@]}"
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  E2E_OPERATOR_NAMESPACE="$operator_namespace" \
  E2E_OPERATOR_DEPLOYMENT="$operator_deployment" \
  ./hack/e2e/dump.sh "$artifact_dir" "$namespace"
  "$kind_bin" export logs "${artifact_dir}/kind-logs" --name "$cluster_name" || true
fi

exit "$rc"
