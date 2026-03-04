#!/usr/bin/env bash
set -euo pipefail

namespace="${1:?namespace is required}"
secret_name="${2:?secret name is required}"
common_name="${3:?common name is required}"
shift 3

if [ "$#" -lt 1 ]; then
  echo "at least one SAN DNS entry is required" >&2
  exit 1
fi

workdir="$(mktemp -d)"
cleanup() {
  rm -rf "${workdir}"
}
trap cleanup EXIT

config_file="${workdir}/openssl.cnf"
{
  cat <<EOF
[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no
[req_distinguished_name]
CN = ${common_name}
[v3_req]
subjectAltName = @alt_names
[alt_names]
EOF
  index=1
  for san in "$@"; do
    echo "DNS.${index} = ${san}"
    index=$((index + 1))
  done
} >"${config_file}"

openssl genrsa -out "${workdir}/ca.key" 2048
openssl req -x509 -new -nodes -key "${workdir}/ca.key" -sha256 -days 3650 \
  -subj "/CN=redis-e2e-ca" -out "${workdir}/ca.crt"

openssl genrsa -out "${workdir}/tls.key" 2048
openssl req -new -key "${workdir}/tls.key" -out "${workdir}/tls.csr" -config "${config_file}"
openssl x509 -req -in "${workdir}/tls.csr" -CA "${workdir}/ca.crt" -CAkey "${workdir}/ca.key" \
  -CAcreateserial -out "${workdir}/tls.crt" -days 3650 -sha256 \
  -extensions v3_req -extfile "${config_file}"

kubectl -n "${namespace}" create secret generic "${secret_name}" \
  --from-file=tls.crt="${workdir}/tls.crt" \
  --from-file=tls.key="${workdir}/tls.key" \
  --from-file=ca.crt="${workdir}/ca.crt" \
  --dry-run=client -o yaml | kubectl apply -f -

