# Administration

Here is docs for admin user.

## Rolling Update

Rolling update is operator-driven. Users only need to update the `Redis` resource, for example:

- change `spec.image`
- change `spec.redisConfig`
- change `spec.sentinelConfig`
- change other Pod template fields that require restart

Do not delete Pods manually. The operator updates one Pod at a time.

### Failover Mode

Basic flow:

1. Update the `Redis` resource.
2. The operator updates Redis replica Pods first, one by one.
3. When only the current master Pod is left, the operator triggers `SENTINEL FAILOVER`.
4. After the old master is no longer master, the operator deletes that old master Pod.
5. After Redis Pods are updated, the operator updates Sentinel Pods one by one.

### Cluster Mode

Basic flow:

1. Update the `Redis` resource.
2. The operator updates shard StatefulSets one by one in shard order.
3. Inside one shard, replica Pods are updated first, one by one.
4. When only the current primary Pod is left, the operator requests `CLUSTER FAILOVER` on a healthy replica.
5. After the shard converges, the operator deletes the old primary Pod and continues with the next shard.
