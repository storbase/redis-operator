# redis-operator

Deploy and operate Redis on Kubernetes with one `Redis` CRD in two modes:

- `cluster`: shard-based Redis Cluster (`1 StatefulSet` per shard)
- `failover`: Redis + Sentinel (`2 StatefulSets`: redis and sentinel)

## Highlights

- [x] Based on the [official Redis Docker image](https://hub.docker.com/_/redis)
- [x] One CRD supports both Redis Cluster and Failover topologies
- [x] Uses stable Kubernetes DNS hostnames (StatefulSet/Service), not Pod IP addresses
- [x] Supports TLS with user-provided certificates
- [x] Supports cluster shard scale in/out (one shard per update)
- [x] Supports failover redis scale in/out
- [x] Supports failover external access with NodePort and Sentinel-native discovery
- [x] Supports cluster external access with NodePort and native announce settings

## Install with Helm 4

### Install from OCI chart (GHCR)

```bash
helm upgrade --install redis-operator oci://ghcr.io/storbase/charts/redis-operator \
  --version 0.1.0 \
  --namespace redis-operator-system \
  --create-namespace
```

### Install from local chart

```bash
helm upgrade --install redis-operator ./charts/redis-operator \
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
OPERATOR_VERSION=v0.1.0
kubectl apply -f https://raw.githubusercontent.com/storbase/redis-operator/${OPERATOR_VERSION}/config/crd/bases/redis.storbase.io_redis.yaml
helm upgrade redis-operator oci://ghcr.io/storbase/charts/redis-operator \
  --namespace redis-operator-system
```

## Redis examples

Apply from [examples](./examples) manifests.
