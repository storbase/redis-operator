#!/usr/bin/env bash
set -euo pipefail

action="${1:-}"
if [ -z "$action" ]; then
  echo "usage: $0 <up|down>" >&2
  exit 1
fi

cluster_name="${E2E_KIND_CLUSTER:-redis-operator-e2e}"
context="kind-${cluster_name}"
kubectl_bin="${KUBECTL_BIN:-kubectl}"
cluster_domain="${E2E_CLUSTER_DOMAIN:-cluster.local}"
resolver_dir="${E2E_RESOLVER_DIR:-/etc/resolver}"
resolver_file="${resolver_dir}/${cluster_domain}"
backup_file="${resolver_file}.redis-operator-e2e.bak"
marker="# Managed by redis-operator e2e local dns"
dns_source="${E2E_DNS_SOURCE:-kube-dns}"
ktctl_dns_port="${E2E_KTCTL_DNS_PORT:-10053}"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "local DNS helper currently supports macOS only; skip" >&2
  exit 0
fi

if ! command -v "$kubectl_bin" >/dev/null 2>&1; then
  echo "kubectl binary not found: $kubectl_bin" >&2
  exit 1
fi

if ! command -v sudo >/dev/null 2>&1; then
  echo "sudo is required to update ${resolver_file}" >&2
  exit 1
fi

if ! sudo -n true >/dev/null 2>&1; then
  echo "sudo credential is required. Run 'sudo -v' first, then retry." >&2
  exit 1
fi

dns_ip=""
if [ "$dns_source" = "kube-dns" ]; then
  dns_ip="$("$kubectl_bin" --context "$context" -n kube-system get svc kube-dns -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)"
  if [ -z "$dns_ip" ]; then
    dns_ip="$("$kubectl_bin" --context "$context" -n kube-system get svc coredns -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)"
  fi
elif [ "$dns_source" = "ktctl-local" ]; then
  dns_ip="127.0.0.1"
  if ! (lsof -nP -iTCP:"${ktctl_dns_port}" -sTCP:LISTEN >/dev/null 2>&1 || lsof -nP -iUDP:"${ktctl_dns_port}" >/dev/null 2>&1); then
    echo "ktctl local DNS port ${ktctl_dns_port} is not available on localhost." >&2
    echo "Start 'ktctl connect --dnsMode localDNS' first, then retry." >&2
    exit 1
  fi
else
  echo "unsupported E2E_DNS_SOURCE: ${dns_source}" >&2
  exit 1
fi

if [ "$action" = "up" ]; then
  if [ -z "$dns_ip" ]; then
    echo "failed to discover kube-dns/coredns service IP in context ${context}" >&2
    exit 1
  fi

  tmp_file="$(mktemp)"
  cat >"$tmp_file" <<EOF
${marker}
nameserver ${dns_ip}
EOF
  if [ "$dns_source" = "ktctl-local" ]; then
    cat >>"$tmp_file" <<EOF
port ${ktctl_dns_port}
EOF
  fi
  cat >>"$tmp_file" <<EOF
search_order 1
timeout 2
EOF

  sudo mkdir -p "$resolver_dir"
  if sudo test -f "$resolver_file"; then
    if ! sudo grep -qF "$marker" "$resolver_file" && ! sudo test -f "$backup_file"; then
      sudo cp "$resolver_file" "$backup_file"
    fi
  fi
  sudo cp "$tmp_file" "$resolver_file"
  sudo chmod 0644 "$resolver_file"
  rm -f "$tmp_file"
  if [ "$dns_source" = "ktctl-local" ]; then
    echo "configured resolver ${resolver_file} -> ${dns_ip}:${ktctl_dns_port} (ktctl local DNS)"
  else
    echo "configured resolver ${resolver_file} -> ${dns_ip} (kube-dns service)"
  fi
  exit 0
fi

if [ "$action" = "down" ]; then
  if sudo test -f "$backup_file"; then
    sudo mv "$backup_file" "$resolver_file"
    echo "restored resolver ${resolver_file} from backup"
    exit 0
  fi
  if sudo test -f "$resolver_file" && sudo grep -qF "$marker" "$resolver_file"; then
    sudo rm -f "$resolver_file"
    echo "removed managed resolver ${resolver_file}"
    exit 0
  fi
  echo "nothing to cleanup for resolver ${resolver_file}"
  exit 0
fi

echo "unsupported action: ${action}" >&2
exit 1
