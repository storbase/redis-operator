# redis-operator

A Kubernetes operator that deploys Redis in one CRD with two modes:

- `cluster`: shard-based Redis Cluster (`1 StatefulSet = 1 shard`)
- `failover`: Redis + Sentinel (`2 StatefulSets`)

## Highlights

- [x] Based on the [official Redis Docker image](https://hub.docker.com/_/redis)
- [x] Supports both Redis Cluster and Failover modes in a single CRD
- [x] Uses hostnames instead of Pod IP addresses
  - Use stable Kubernetes DNS hostnames (StatefulSet/Service) for Redis node identity.
  - This avoids broken topology/replication links after Pod restarts or rescheduling when Pod IPs change.
- [x] Supports TLS with user-provided certificates
- [x] Supports cluster shard scale in/out
- [x] Supports failover redis scale in/out
- [x] Supports failover external access with NodePort and Sentinel native discovery
- [x] Supports cluster external access with NodePort and native announce settings

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
- helm
- go
- openssl
- ktctl (run in a separate terminal with local DNS mode)

Run local e2e with helm-installed operator:

1. `make e2e-local`
2. Keep `ktctl connect` running (for example: `sudo ktctl connect --context kind-redis-operator-e2e --namespace default --dnsMode localDNS`).
3. The workflow reapplies chart CRDs, installs the Helm chart with `replicaCount=0`, and runs the controller locally via `go run ./cmd/main.go`.
4. If an existing kind cluster is older than Kubernetes 1.29, it is recreated automatically before running tests.
5. Run one suite only when needed, for example:
   - `E2E_CHAINSAW_SUITES=failover make e2e-local`
   - `E2E_CHAINSAW_SUITES=cluster-external make e2e-local`
   - `E2E_CHAINSAW_SUITES=failover-external make e2e-local`

## Failover external access (NodePort)

Configure external per-pod endpoints with `spec.externalAccess.failover`.
The operator will:

- create per-pod NodePort Services for Redis and Sentinel;
- write Redis/Sentinel announce directives with those external endpoints;
- keep internal runtime reconciliation on stable in-cluster hostnames;
- publish user-facing internal and external endpoints in `.status.endpoints`.

## Cluster external access (NodePort)

Configure per-pod external endpoints with `spec.externalAccess.cluster`.
The operator will:

- create one per-pod NodePort Service with two ports (`redis` and `cluster-bus`);
- write `cluster-announce-hostname`, `cluster-announce-port`, and `cluster-announce-bus-port` by shard/ordinal;
- keep internal runtime reconciliation on stable in-cluster hostnames;
- publish internal seed endpoints in `.status.endpoints.internal` and external seed endpoints (ordinal `0` of each shard) in `.status.endpoints.external`.

Notes:

- only `NodePort` is supported in this release;
- cluster nodes must be configured explicitly for all shard/ordinal pairs;
- `port` and `busPort` must be unique NodePort values in `[30000,32767]`.

## How to contribute

Contributions are welcome.
