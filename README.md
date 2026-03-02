# redis-operator

A Kubernetes operator that deploys Redis in one CRD with two modes:

- `Cluster`: shard-based Redis Cluster (`1 StatefulSet = 1 shard`)
- `Failover`: Redis + Sentinel (`2 StatefulSets`)

## Highlights

- [x] Based on the [official Redis Docker image](https://hub.docker.com/_/redis)
- [x] Supports both Redis Cluster and Failover modes in a single CRD
- [x] Uses hostnames instead of Pod IP addresses
  - Use stable Kubernetes DNS hostnames (StatefulSet/Service) for Redis node identity.
  - This avoids broken topology/replication links after Pod restarts or rescheduling when Pod IPs change.
- [x] Supports TLS with user-provided certificates
- [x] Supports cluster shard scale in/out

## Install with Helm 4

### Install from OCI chart (GHCR)

```bash
helm upgrade --install redis-operator oci://ghcr.io/storbase/charts/redis-operator \
  --version 0.1.0 \
  --namespace redis-operator-system \
  --create-namespace
```

### Upgrade

```bash
helm upgrade redis-operator ./charts/redis-operator \
  --namespace redis-operator-system
```

### Uninstall

```bash
helm uninstall redis-operator --namespace redis-operator-system
```

## CRD lifecycle

CRDs are shipped from `charts/redis-operator/crds`, which Helm installs on first install.
Helm does not upgrade or delete CRDs from this directory.

To upgrade CRDs, apply them manually before chart upgrade:

```bash
OPERATOR_VERSION=v0.0.0
kubectl apply -f https://raw.githubusercontent.com/storbase/redis-operator/${OPERATOR_VERSION}/config/crd/bases/redis.storbase.io_redis.yaml
helm upgrade redis-operator oci://ghcr.io/storbase/charts/redis-operator \
  --namespace redis-operator-system
```

## Versioning contract

- Operator release tags: `vX.Y.Z`
- Chart release tags: `chart-vA.B.C`
- `charts/redis-operator/Chart.yaml` `appVersion` points to operator version.

## E2E local debug

Prerequisites:

- kind
- kubectl
- chainsaw
- ktctl
- openssl

Run with manually managed ktctl:

1. `sudo ktctl connect --context kind-redis-operator-e2e --namespace default --dnsMode localDNS:cluster --dnsPort 10053`
2. `make e2e-local`
3. Keep the `ktctl connect` terminal alive during the entire e2e run.

## How to contribute

Contributions are welcome.
