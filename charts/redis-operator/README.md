# redis-operator Helm Chart

This chart installs the Storbase Redis Operator.

## Prerequisites

- Kubernetes 1.29+
- Helm 4+

## Install from local chart

```bash
helm upgrade --install redis-operator ./charts/redis-operator \
  --namespace redis-operator-system \
  --create-namespace
```

## Install from OCI (GHCR)

```bash
helm upgrade --install redis-operator oci://ghcr.io/storbase/charts/redis-operator \
  --version 0.1.0 \
  --namespace redis-operator-system \
  --create-namespace
```

## Upgrade

```bash
helm upgrade redis-operator ./charts/redis-operator \
  --namespace redis-operator-system
```

## Uninstall

```bash
helm uninstall redis-operator --namespace redis-operator-system
```

CRDs in `crds/` are install-only in Helm. Uninstalling the chart does not delete CRDs.

## Values

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `image.repository` | string | `ghcr.io/storbase/redis-operator` | Operator image repository |
| `image.tag` | string | `v0.0.0` | Operator image tag |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `replicaCount` | int | `1` | Controller replicas |
| `serviceAccount.create` | bool | `true` | Create service account |
| `serviceAccount.name` | string | `""` | Existing service account name when `create=false` |
| `resources` | object | `{requests: {cpu: 10m, memory: 64Mi}, limits: {cpu: 500m, memory: 128Mi}}` | Container resources |
| `podAnnotations` | object | `{}` | Pod annotations |
| `nodeSelector` | object | `{}` | Pod node selector |
| `tolerations` | list | `[]` | Pod tolerations |
| `affinity` | object | `{}` | Pod affinity |
| `metrics.service.enabled` | bool | `true` | Create metrics service |
| `metrics.service.port` | int | `8443` | Metrics service port |
| `extraArgs` | list | `[]` | Extra manager arguments |

## Versioning contract

- Operator release tags use `vX.Y.Z`.
- Chart release tags use `chart-vA.B.C`.
- `Chart.yaml` `appVersion` points to the operator version.
