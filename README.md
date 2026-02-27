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

Optional split DNS setup for `*.cluster.local` on macOS:

1. `sudo -v`
2. `make e2e-local-dns-up E2E_DNS_SOURCE=ktctl-local`
3. `dig kubernetes.default.svc.cluster.local +short`

Run with script-managed ktctl:

- `GOPROXY=https://goproxy.cn,direct E2E_KTCTL_USE_SUDO=true make e2e-local-run`

Run with externally managed ktctl (recommended on macOS when sudo is interactive):

1. `sudo ktctl connect --context kind-redis-operator-e2e --namespace redis-e2e --dnsMode localDNS`
2. `make e2e-local-dns-up E2E_DNS_SOURCE=ktctl-local`
3. `GOPROXY=https://goproxy.cn,direct E2E_KTCTL_EXTERNAL_CONNECT=true make e2e-local-run`
4. Keep the `ktctl connect` terminal alive during the entire e2e run.

Cleanup split DNS (if configured):

- `make e2e-local-dns-down`


## How to contribute
