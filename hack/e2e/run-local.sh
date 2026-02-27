#!/usr/bin/env bash
set -euo pipefail

cluster_name="${E2E_KIND_CLUSTER:?E2E_KIND_CLUSTER is required}"
namespace="${E2E_NAMESPACE:?E2E_NAMESPACE is required}"
artifact_dir="${E2E_ARTIFACT_DIR_LOCAL:?E2E_ARTIFACT_DIR_LOCAL is required}"
chainsaw_dir="${E2E_CHAINSAW_DIR:?E2E_CHAINSAW_DIR is required}"
chainsaw_config="${E2E_CHAINSAW_CONFIG:?E2E_CHAINSAW_CONFIG is required}"
ktctl_use_sudo="${E2E_KTCTL_USE_SUDO:-false}"
ktctl_external_connect="${E2E_KTCTL_EXTERNAL_CONNECT:-false}"
cluster_domain="${E2E_CLUSTER_DOMAIN:-cluster.local}"
dns_preflight="${E2E_DNS_PREFLIGHT:-true}"
ktctl_dns_port="${E2E_KTCTL_DNS_PORT:-10053}"
operator_dns_server="${E2E_OPERATOR_DNS_SERVER:-}"

mkdir -p "$artifact_dir"

ktctl_pid=""
controller_pid=""
controller_runtime_pid=""
gomodcache="${GOMODCACHE:-$(go env GOMODCACHE)}"
ktctl_cmd=(ktctl)

if [ "$ktctl_use_sudo" = "true" ] && [ "$ktctl_external_connect" != "true" ]; then
  if ! sudo -n true >/dev/null 2>&1; then
    echo "sudo credential is required for ktctl. Run 'sudo -v' first, then retry with E2E_KTCTL_USE_SUDO=true." >&2
    exit 1
  fi
  ktctl_cmd=(sudo -n ktctl)
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
  if [ -n "$ktctl_pid" ] && kill -0 "$ktctl_pid" >/dev/null 2>&1; then
    kill "$ktctl_pid" >/dev/null 2>&1 || true
    wait "$ktctl_pid" >/dev/null 2>&1 || true
  fi
  if [ "$ktctl_external_connect" != "true" ]; then
    "${ktctl_cmd[@]}" clean --context "kind-${cluster_name}" --namespace "$namespace" --localOnly >/dev/null 2>&1 || true
  fi
}

trap cleanup EXIT

if [ "$ktctl_external_connect" != "true" ]; then
  "${ktctl_cmd[@]}" connect \
    --context "kind-${cluster_name}" \
    --namespace "$namespace" \
    --dnsMode "hosts:${namespace}" \
    >"${artifact_dir}/ktctl.log" 2>&1 &
  ktctl_pid="$!"
  echo "$ktctl_pid" >"${artifact_dir}/ktctl.pid"
  sleep 5
  if ! kill -0 "$ktctl_pid" >/dev/null 2>&1; then
    echo "ktctl connect exited unexpectedly"
    tail -n 100 "${artifact_dir}/ktctl.log" || true
    exit 1
  fi
else
  if ! pgrep -f "ktctl.*connect" >/dev/null 2>&1; then
    echo "E2E_KTCTL_EXTERNAL_CONNECT=true but no running 'ktctl connect' process was found." >&2
    echo "Start ktctl in another terminal first, then rerun this command." >&2
    exit 1
  fi
  if [ "$dns_preflight" = "true" ] && command -v dig >/dev/null 2>&1; then
    dns_answer="$(dig +short +time=2 +tries=1 @127.0.0.1 -p "$ktctl_dns_port" "kubernetes.default.svc.${cluster_domain}" 2>/dev/null | tr -d '\r' || true)"
    if [ -z "$dns_answer" ]; then
      echo "DNS preflight failed for kubernetes.default.svc.${cluster_domain}." >&2
      echo "Ensure ktctl is connected with '--dnsMode localDNS' and local DNS port ${ktctl_dns_port} is active." >&2
      exit 1
    fi
  fi
  if [ -z "$operator_dns_server" ]; then
    operator_dns_server="127.0.0.1:${ktctl_dns_port}"
  fi
  echo "ktctl connect is externally managed; skip starting and cleaning ktctl process." >"${artifact_dir}/ktctl.log"
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
REDIS_OPERATOR_DNS_SERVER="$operator_dns_server" \
go run ./cmd/main.go >"${artifact_dir}/local-controller.log" 2>&1 &
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
chainsaw test \
  --config "$chainsaw_config" \
  --test-file chainsaw-test.yaml \
  --test-dir "${chainsaw_dir}/cluster" \
  --test-dir "${chainsaw_dir}/failover" \
  --kube-context "kind-${cluster_name}" \
  --report-format JUNIT-TEST \
  --report-name chainsaw-local \
  --report-path "$artifact_dir"
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  ./hack/e2e/dump.sh "$artifact_dir" "$namespace"
fi

exit "$rc"
