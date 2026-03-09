package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

const unknownOrdinal int32 = -1

func (c *AdminClient) ObserveFailover(ctx context.Context, namespace, name string) (appinterfaces.FailoverObservation, error) {
	obj, err := c.loadRedis(ctx, namespace, name)
	if err != nil {
		return appinterfaces.FailoverObservation{}, err
	}
	if (obj.Spec.Mode != "" && obj.Spec.Mode != redisv1alpha1.RedisModeFailover) || obj.Spec.Failover == nil {
		return appinterfaces.FailoverObservation{}, nil
	}

	redisPassword, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.RedisPasswordSecretRef)
	if err != nil {
		return appinterfaces.FailoverObservation{}, err
	}
	sentinelPassword, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.SentinelPasswordSecretRef)
	if err != nil {
		return appinterfaces.FailoverObservation{}, err
	}
	tlsConfig, err := c.readTLSConfig(ctx, namespace, obj.Spec.TLS)
	if err != nil {
		return appinterfaces.FailoverObservation{}, err
	}

	redisAddrs := buildFailoverRedisAddresses(obj.Namespace, obj.Name, obj.Spec.Failover.RedisReplicas)
	if len(redisAddrs) == 0 {
		return appinterfaces.FailoverObservation{}, fmt.Errorf("no failover redis addresses generated")
	}
	observation, err := c.observeFailoverRedisNodes(ctx, redisAddrs, redisPassword, tlsConfig)
	if err != nil {
		return appinterfaces.FailoverObservation{}, err
	}

	sentinelAddrs := buildSentinelAddresses(obj.Namespace, obj.Name, obj.Spec.Failover.SentinelReplicas)
	if len(sentinelAddrs) == 0 {
		return appinterfaces.FailoverObservation{}, fmt.Errorf("no sentinel addresses generated")
	}
	masterName := failoverMasterName(obj)
	required := len(sentinelAddrs)/2 + 1
	reportCounts := map[int32]int{}
	healthySentinels := 0
	for _, addr := range sentinelAddrs {
		ordinal, err := c.observeSentinelMasterOrdinal(
			ctx,
			addr,
			masterName,
			sentinelPassword,
			tlsConfig,
			redisAddrs,
			failoverExternalRedisNodes(obj),
		)
		if err != nil {
			continue
		}
		healthySentinels++
		reportCounts[ordinal]++
	}

	consensusOrdinal := unknownOrdinal
	for ordinal, count := range reportCounts {
		if count >= required {
			consensusOrdinal = ordinal
			break
		}
	}
	observation.SentinelQuorumHealthy = healthySentinels >= required
	observation.ConsensusMasterOrdinal = consensusOrdinal
	return observation, nil
}

func (c *AdminClient) RequestFailover(ctx context.Context, namespace, name string) error {
	obj, err := c.loadRedis(ctx, namespace, name)
	if err != nil {
		return err
	}
	if (obj.Spec.Mode != "" && obj.Spec.Mode != redisv1alpha1.RedisModeFailover) || obj.Spec.Failover == nil {
		return nil
	}

	sentinelPassword, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.SentinelPasswordSecretRef)
	if err != nil {
		return err
	}
	tlsConfig, err := c.readTLSConfig(ctx, namespace, obj.Spec.TLS)
	if err != nil {
		return err
	}

	masterName := failoverMasterName(obj)
	var lastErr error
	for _, sentinelAddr := range buildSentinelAddresses(obj.Namespace, obj.Name, obj.Spec.Failover.SentinelReplicas) {
		if err := c.requestSentinelFailover(ctx, sentinelAddr, masterName, sentinelPassword, tlsConfig); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("request sentinel failover failed: %w", lastErr)
	}
	return fmt.Errorf("request sentinel failover failed: no reachable sentinel")
}

func (c *AdminClient) ObserveClusterShards(ctx context.Context, namespace, name string) ([]appinterfaces.ClusterShardObservation, error) {
	obj, err := c.loadRedis(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if (obj.Spec.Mode != "" && obj.Spec.Mode != redisv1alpha1.RedisModeCluster) || obj.Spec.Cluster == nil {
		return nil, nil
	}

	password, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.RedisPasswordSecretRef)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := c.readTLSConfig(ctx, namespace, obj.Spec.TLS)
	if err != nil {
		return nil, err
	}

	observations := make([]appinterfaces.ClusterShardObservation, 0, obj.Spec.Cluster.Shards)
	for shard := int32(0); shard < obj.Spec.Cluster.Shards; shard++ {
		nodes := make([]appinterfaces.ReplicationNodeObservation, 0, obj.Spec.Cluster.ReplicasPerShard+1)
		primaryOrdinal := unknownOrdinal
		for ordinal := int32(0); ordinal <= obj.Spec.Cluster.ReplicasPerShard; ordinal++ {
			addr := buildClusterNodeAddress(obj.Namespace, obj.Name, shard, ordinal)
			fields, err := c.readReplicationInfo(ctx, addr, password, tlsConfig)
			if err != nil {
				return nil, err
			}
			role := normalizeRedisRole(fields["role"])
			if role == "master" {
				if primaryOrdinal != unknownOrdinal {
					return nil, fmt.Errorf("multiple primaries detected in shard %d", shard)
				}
				primaryOrdinal = ordinal
			}
			nodes = append(nodes, appinterfaces.ReplicationNodeObservation{
				Ordinal:          ordinal,
				Role:             role,
				MasterLinkStatus: normalizeMasterLinkStatus(fields["master_link_status"]),
			})
		}
		if primaryOrdinal == unknownOrdinal {
			return nil, fmt.Errorf("no primary detected in shard %d", shard)
		}
		observations = append(observations, appinterfaces.ClusterShardObservation{
			Shard:          shard,
			PrimaryOrdinal: primaryOrdinal,
			Nodes:          nodes,
		})
	}
	return observations, nil
}

func (c *AdminClient) RequestClusterFailover(ctx context.Context, namespace, name string, shard, ordinal int32) error {
	obj, err := c.loadRedis(ctx, namespace, name)
	if err != nil {
		return err
	}
	if (obj.Spec.Mode != "" && obj.Spec.Mode != redisv1alpha1.RedisModeCluster) || obj.Spec.Cluster == nil {
		return nil
	}
	if shard < 0 || shard >= obj.Spec.Cluster.Shards {
		return fmt.Errorf("invalid shard ordinal %d", shard)
	}
	if ordinal < 0 || ordinal > obj.Spec.Cluster.ReplicasPerShard {
		return fmt.Errorf("invalid replica ordinal %d", ordinal)
	}

	password, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.RedisPasswordSecretRef)
	if err != nil {
		return err
	}
	tlsConfig, err := c.readTLSConfig(ctx, namespace, obj.Spec.TLS)
	if err != nil {
		return err
	}

	targetAddr := buildClusterNodeAddress(obj.Namespace, obj.Name, shard, ordinal)
	cli := c.newRedisClient(targetAddr, password, tlsConfig)
	defer func() {
		_ = cli.Close()
	}()

	commandCtx, cancel := c.commandContext(ctx)
	err = cli.Do(commandCtx, "CLUSTER", "FAILOVER").Err()
	cancel()
	if err != nil && !isIgnorableClusterFailoverError(err) {
		return fmt.Errorf("cluster failover on %s failed: %w", targetAddr, err)
	}
	return nil
}

func (c *AdminClient) observeFailoverRedisNodes(
	ctx context.Context,
	redisAddrs []string,
	password string,
	tlsConfig *tls.Config,
) (appinterfaces.FailoverObservation, error) {
	observation := appinterfaces.FailoverObservation{
		MasterOrdinal:          unknownOrdinal,
		ConsensusMasterOrdinal: unknownOrdinal,
		Nodes:                  make([]appinterfaces.ReplicationNodeObservation, 0, len(redisAddrs)),
	}
	for ordinal, addr := range redisAddrs {
		fields, err := c.readReplicationInfo(ctx, addr, password, tlsConfig)
		if err != nil {
			return appinterfaces.FailoverObservation{}, err
		}
		role := normalizeRedisRole(fields["role"])
		if role == "master" {
			if observation.MasterOrdinal != unknownOrdinal {
				return appinterfaces.FailoverObservation{}, fmt.Errorf("multiple masters detected in failover redis set")
			}
			observation.MasterOrdinal = int32(ordinal)
		}
		observation.Nodes = append(observation.Nodes, appinterfaces.ReplicationNodeObservation{
			Ordinal:          int32(ordinal),
			Role:             role,
			MasterLinkStatus: normalizeMasterLinkStatus(fields["master_link_status"]),
		})
	}
	if observation.MasterOrdinal == unknownOrdinal {
		return appinterfaces.FailoverObservation{}, fmt.Errorf("no master detected in failover redis set")
	}
	return observation, nil
}

func (c *AdminClient) observeSentinelMasterOrdinal(
	ctx context.Context,
	sentinelAddr,
	masterName,
	sentinelPassword string,
	tlsConfig *tls.Config,
	redisAddrs []string,
	externalRedisNodes []redisv1alpha1.ExternalNodeAddress,
) (int32, error) {
	masterAddr, err := c.checkSentinelHealth(ctx, sentinelAddr, masterName, sentinelPassword, tlsConfig)
	if err != nil {
		return unknownOrdinal, err
	}
	normalizedMasterAddr, err := normalizeFailoverMasterAddress(masterAddr, redisAddrs, externalRedisNodes)
	if err != nil {
		return unknownOrdinal, err
	}
	for ordinal, redisAddr := range redisAddrs {
		if canonicalEndpointKeyFromAddr(redisAddr) == canonicalEndpointKeyFromAddr(normalizedMasterAddr) {
			return int32(ordinal), nil
		}
	}
	return unknownOrdinal, fmt.Errorf("sentinel %s reported unknown master address %s", sentinelAddr, normalizedMasterAddr)
}

func (c *AdminClient) requestSentinelFailover(
	ctx context.Context,
	sentinelAddr,
	masterName,
	sentinelPassword string,
	tlsConfig *tls.Config,
) error {
	sentinel := c.newSentinelClient(sentinelAddr, sentinelPassword, tlsConfig)
	defer func() {
		_ = sentinel.Close()
	}()

	commandCtx, cancel := c.commandContext(ctx)
	err := sentinel.CkQuorum(commandCtx, masterName).Err()
	cancel()
	if err != nil {
		return fmt.Errorf("sentinel ckquorum via %s failed: %w", sentinelAddr, err)
	}

	commandCtx, cancel = c.commandContext(ctx)
	err = sentinel.Failover(commandCtx, masterName).Err()
	cancel()
	if err != nil && !isIgnorableSentinelFailoverError(err) {
		return fmt.Errorf("sentinel failover via %s failed: %w", sentinelAddr, err)
	}
	return nil
}

func failoverMasterName(obj *redisv1alpha1.Redis) string {
	if obj.Spec.Failover == nil || strings.TrimSpace(obj.Spec.Failover.MasterName) == "" {
		return defaultMasterName
	}
	return strings.TrimSpace(obj.Spec.Failover.MasterName)
}

func buildClusterNodeAddress(namespace, name string, shard, ordinal int32) string {
	shardService := fmt.Sprintf("%s-shard-%d", name, shard)
	return fmt.Sprintf(
		"%s-%d.%s.%s.svc.%s:%d",
		shardService,
		ordinal,
		shardService,
		namespace,
		clusterDomain,
		clusterPort,
	)
}

func normalizeRedisRole(raw string) string {
	role := strings.ToLower(strings.TrimSpace(raw))
	switch role {
	case "slave":
		return "replica"
	default:
		return role
	}
}

func normalizeMasterLinkStatus(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isIgnorableSentinelFailoverError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already") || strings.Contains(message, "in progress") || strings.Contains(message, "inprog")
}

func isIgnorableClusterFailoverError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "manual failover already") || strings.Contains(message, "failover already") || strings.Contains(message, "in progress")
}

func canonicalEndpointKeyFromAddr(addr string) string {
	host, port, err := splitAddress(addr)
	if err != nil {
		return ""
	}
	return canonicalEndpointKey(host, port)
}
