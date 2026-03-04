package cluster

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	cfg "github.com/storbase/redis-operator/internal/config"
	"github.com/storbase/redis-operator/internal/manifests"
)

// BuildDesiredState renders Kubernetes objects for Cluster mode.
func BuildDesiredState(redis *redisv1alpha1.Redis) ([]client.Object, redisv1alpha1.EndpointStatus, error) {
	if redis.Spec.Cluster == nil {
		return nil, redisv1alpha1.EndpointStatus{}, fmt.Errorf("spec.cluster must be set when mode is cluster")
	}
	if redis.Spec.Failover != nil {
		return nil, redisv1alpha1.EndpointStatus{}, fmt.Errorf("spec.failover must not be set when mode is cluster")
	}
	if len(cfg.NormalizeUserLines(redis.Spec.SentinelConfig)) > 0 {
		return nil, redisv1alpha1.EndpointStatus{}, fmt.Errorf("spec.sentinelConfig is only valid in failover mode")
	}

	userRedisConfig := cfg.NormalizeUserLines(redis.Spec.RedisConfig)
	tlsEnabled := redis.Spec.TLS != nil
	tlsSecretName := ""
	if redis.Spec.TLS != nil {
		tlsSecretName = redis.Spec.TLS.SecretName
	}

	configName := fmt.Sprintf("%s-redis-config", redis.Name)
	redisCM := manifests.NewConfigMap(configName, redis.Namespace, manifests.BaseLabels(redis), map[string]string{
		"redis.conf": manifests.RenderRedisConfig(redisv1alpha1.RedisModeCluster, userRedisConfig, tlsEnabled),
	})

	objects := []client.Object{redisCM}
	internalRedisEndpoints := make([]redisv1alpha1.ExternalAddress, 0, redis.Spec.Cluster.Shards)
	externalRedisEndpoints := make([]redisv1alpha1.ExternalAddress, 0, redis.Spec.Cluster.Shards)

	var clusterExternal *redisv1alpha1.ClusterExternalAccessSpec
	if redis.Spec.ExternalAccess != nil {
		clusterExternal = redis.Spec.ExternalAccess.Cluster
	}
	clusterExternalByShardOrdinal := indexClusterExternalNodesByShardAndOrdinal(clusterExternal)

	for i := int32(0); i < redis.Spec.Cluster.Shards; i++ {
		shardName := fmt.Sprintf("%s-shard-%d", redis.Name, i)
		shardLabels := manifests.LabelsFor(redis, "redis", fmt.Sprintf("%d", i))

		headless := manifests.NewHeadlessService(shardName, redis.Namespace, shardLabels, "redis", 6379)
		objects = append(objects, headless)

		replicas := redis.Spec.Cluster.ReplicasPerShard + 1
		if replicas < 1 {
			replicas = 1
		}

		redisSTS := manifests.NewRedisStatefulSet(redis, manifests.RedisStatefulSetOptions{
			Name:          shardName,
			Namespace:     redis.Namespace,
			ServiceName:   shardName,
			Labels:        shardLabels,
			Replicas:      replicas,
			Policy:        redis.Spec.Cluster.RedisPod,
			Storage:       redis.Spec.Cluster.Storage,
			Command:       manifests.RenderClusterRedisCommand(clusterExternalNodesForShard(clusterExternalByShardOrdinal, i, replicas)),
			ConfigMapName: configName,
			TLSSecretName: tlsSecretName,
		})
		objects = append(objects, redisSTS)
		internalRedisEndpoints = append(internalRedisEndpoints, redisv1alpha1.ExternalAddress{
			Host: fmt.Sprintf("%s-0.%s.%s.svc.cluster.local", shardName, shardName, redis.Namespace),
			Port: 6379,
		})

		if clusterExternal != nil && clusterExternal.Type == redisv1alpha1.ExternalAccessTypeNodePort {
			for ordinal := int32(0); ordinal < replicas; ordinal++ {
				endpoint, ok := clusterExternalNodeByShardOrdinal(clusterExternalByShardOrdinal, i, ordinal)
				if !ok {
					continue
				}
				podName := fmt.Sprintf("%s-%d", shardName, ordinal)
				objects = append(objects, manifests.NewNodePortServiceWithPorts(
					fmt.Sprintf("%s-shard-%d-external-%d", redis.Name, i, ordinal),
					redis.Namespace,
					shardLabels,
					podSelector(shardLabels, podName),
					[]manifests.NodePortServicePort{
						{
							Name:       "redis",
							Port:       6379,
							TargetPort: 6379,
							NodePort:   endpoint.Port,
						},
						{
							Name:       "cluster-bus",
							Port:       16379,
							TargetPort: 16379,
							NodePort:   endpoint.BusPort,
						},
					},
				))
				if ordinal == 0 {
					externalRedisEndpoints = append(externalRedisEndpoints, endpoint.ExternalAddress)
				}
			}
		}
	}

	endpoints := redisv1alpha1.EndpointStatus{
		Internal: internalRedisEndpoints,
	}
	if clusterExternal != nil {
		endpoints.External = externalRedisEndpoints
	}
	return objects, endpoints, nil
}

func podSelector(base map[string]string, podName string) map[string]string {
	selector := make(map[string]string, len(base)+1)
	for key, value := range base {
		selector[key] = value
	}
	selector["statefulset.kubernetes.io/pod-name"] = podName
	return selector
}

func indexClusterExternalNodesByShardAndOrdinal(
	spec *redisv1alpha1.ClusterExternalAccessSpec,
) map[int32]map[int32]redisv1alpha1.ClusterExternalNodeAddress {
	if spec == nil {
		return nil
	}
	result := make(map[int32]map[int32]redisv1alpha1.ClusterExternalNodeAddress)
	for _, node := range spec.Nodes {
		shardNodes, ok := result[node.Shard]
		if !ok {
			shardNodes = make(map[int32]redisv1alpha1.ClusterExternalNodeAddress)
			result[node.Shard] = shardNodes
		}
		shardNodes[node.Ordinal] = node
	}
	return result
}

func clusterExternalNodesForShard(
	byShardOrdinal map[int32]map[int32]redisv1alpha1.ClusterExternalNodeAddress,
	shard,
	replicas int32,
) []redisv1alpha1.ClusterExternalNodeAddress {
	if len(byShardOrdinal) == 0 {
		return nil
	}
	nodesByOrdinal, ok := byShardOrdinal[shard]
	if !ok {
		return nil
	}
	result := make([]redisv1alpha1.ClusterExternalNodeAddress, 0, replicas)
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		node, exists := nodesByOrdinal[ordinal]
		if !exists {
			continue
		}
		result = append(result, node)
	}
	return result
}

func clusterExternalNodeByShardOrdinal(
	byShardOrdinal map[int32]map[int32]redisv1alpha1.ClusterExternalNodeAddress,
	shard,
	ordinal int32,
) (redisv1alpha1.ClusterExternalNodeAddress, bool) {
	if len(byShardOrdinal) == 0 {
		return redisv1alpha1.ClusterExternalNodeAddress{}, false
	}
	nodesByOrdinal, ok := byShardOrdinal[shard]
	if !ok {
		return redisv1alpha1.ClusterExternalNodeAddress{}, false
	}
	node, exists := nodesByOrdinal[ordinal]
	if !exists {
		return redisv1alpha1.ClusterExternalNodeAddress{}, false
	}
	return node, true
}
