# redis-operator

A Kubernetes operator that deploys Redis in one CRD with two modes:

- `Cluster`: shard-based Redis Cluster (`1 StatefulSet = 1 shard`)
- `Failover`: Redis + Sentinel (`2 StatefulSets`)

## Highlights

- Manage redis sentinel/cluster mode in one CRD.

## E2E local debug

Prerequisites:

- kind
- kubectl
- chainsaw
- ktctl

Run with manually managed ktctl:

1. `sudo ktctl connect --context kind-redis-operator-e2e --namespace default --dnsMode localDNS:cluster --dnsPort 10053`
2. `GOPROXY=https://goproxy.cn,direct make e2e-local`
3. Keep the `ktctl connect` terminal alive during the entire e2e run.

If you previously used `e2e-local-dns-up`, remove the stale resolver file before using this flow:

- `sudo rm -f /etc/resolver/cluster.local`


## How to contribute
