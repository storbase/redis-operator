package failover

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	cfg "github.com/storbase/redis-operator/internal/config"
	"github.com/storbase/redis-operator/internal/manifests"
)

// BuildDesiredState renders Kubernetes objects for Failover mode.
func BuildDesiredState(redis *redisv1alpha1.Redis) ([]client.Object, redisv1alpha1.EndpointStatus, error) {
	if redis.Spec.Failover == nil {
		return nil, redisv1alpha1.EndpointStatus{}, fmt.Errorf("spec.failover must be set when mode is failover")
	}
	if redis.Spec.Cluster != nil {
		return nil, redisv1alpha1.EndpointStatus{}, fmt.Errorf("spec.cluster must not be set when mode is failover")
	}

	userRedisConfig := cfg.NormalizeUserLines(redis.Spec.RedisConfig)
	userSentinelConfig := cfg.NormalizeUserLines(redis.Spec.SentinelConfig)
	tlsEnabled := redis.Spec.TLS != nil
	tlsSecretName := ""
	if redis.Spec.TLS != nil {
		tlsSecretName = redis.Spec.TLS.SecretName
	}
	redisConfig := manifests.RenderRedisConfig(redisv1alpha1.RedisModeFailover, userRedisConfig, tlsEnabled)

	masterName := redis.Spec.Failover.MasterName
	if masterName == "" {
		masterName = "mymaster"
	}

	redisConfigName := fmt.Sprintf("%s-redis-config", redis.Name)
	sentinelConfigName := fmt.Sprintf("%s-sentinel-config", redis.Name)
	redisHeadlessName := fmt.Sprintf("%s-redis-headless", redis.Name)
	redisServiceName := fmt.Sprintf("%s-redis", redis.Name)
	sentinelServiceName := fmt.Sprintf("%s-sentinel", redis.Name)
	masterHost := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local", redisServiceName, redisHeadlessName, redis.Namespace)
	masterEndpoint := redisv1alpha1.ExternalAddress{
		Host: masterHost,
		Port: 6379,
	}

	var failoverExternal *redisv1alpha1.FailoverExternalAccessSpec
	if redis.Spec.ExternalAccess != nil {
		failoverExternal = redis.Spec.ExternalAccess.Failover
	}
	if failoverExternal != nil {
		for _, node := range failoverExternal.Redis.Nodes {
			if node.Ordinal == 0 {
				masterEndpoint = node.ExternalAddress
				break
			}
		}
	}

	redisCM := manifests.NewConfigMap(redisConfigName, redis.Namespace, manifests.BaseLabels(redis), map[string]string{
		"redis.conf": redisConfig,
	})
	sentinelConfig := manifests.RenderSentinelConfig(
		masterName,
		masterEndpoint.Host,
		masterEndpoint.Port,
		redis.Spec.Failover.Quorum,
		userSentinelConfig,
		tlsEnabled,
	)
	sentinelCM := manifests.NewConfigMap(sentinelConfigName, redis.Namespace, manifests.BaseLabels(redis), map[string]string{
		"sentinel.conf": sentinelConfig,
	})

	redisLabels := manifests.LabelsFor(redis, "redis", "")
	sentinelLabels := manifests.LabelsFor(redis, "sentinel", "")

	redisHeadless := manifests.NewHeadlessService(redisHeadlessName, redis.Namespace, redisLabels, "redis", 6379)
	redisService := manifests.NewService(redisServiceName, redis.Namespace, redisLabels, "redis", 6379)

	redisSTS := manifests.NewRedisStatefulSet(redis, manifests.RedisStatefulSetOptions{
		Name:          redisServiceName,
		Namespace:     redis.Namespace,
		ServiceName:   redisHeadlessName,
		Labels:        redisLabels,
		Replicas:      redis.Spec.Failover.RedisReplicas,
		Policy:        redis.Spec.Failover.RedisPod,
		Storage:       redis.Spec.Failover.Storage,
		Command:       manifests.RenderFailoverRedisCommand(masterHost, failoverExternalRedisNodes(failoverExternal)),
		ConfigMapName: redisConfigName,
		ConfigData:    redisConfig,
		TLSSecretName: tlsSecretName,
	})

	sentinelService := manifests.NewHeadlessService(sentinelServiceName, redis.Namespace, sentinelLabels, "sentinel", 26379)
	sentinelSTS := manifests.NewSentinelStatefulSet(manifests.SentinelStatefulSetOptions{
		Name:                sentinelServiceName,
		Namespace:           redis.Namespace,
		ServiceName:         sentinelServiceName,
		Labels:              sentinelLabels,
		Replicas:            redis.Spec.Failover.SentinelReplicas,
		Policy:              redis.Spec.Failover.SentinelPod,
		MasterName:          masterName,
		ConfigMapName:       sentinelConfigName,
		ConfigData:          sentinelConfig,
		RedisPasswordRef:    redis.Spec.Auth.RedisPasswordSecretRef,
		SentinelPasswordRef: redis.Spec.Auth.SentinelPasswordSecretRef,
		Image:               redis.Spec.Image,
		ImagePullPolicy:     redis.Spec.ImagePullPolicy,
		TLSSecretName:       tlsSecretName,
		ExternalEndpoints:   failoverExternalSentinelNodes(failoverExternal),
	})

	objects := []client.Object{redisCM, sentinelCM, redisHeadless, redisService, redisSTS, sentinelService, sentinelSTS}
	if failoverExternal != nil && failoverExternal.Type == redisv1alpha1.ExternalAccessTypeNodePort {
		redisExternalByOrdinal := indexExternalNodesByOrdinal(failoverExternal.Redis.Nodes)
		for ordinal := int32(0); ordinal < redis.Spec.Failover.RedisReplicas; ordinal++ {
			node, ok := redisExternalByOrdinal[ordinal]
			if !ok {
				continue
			}
			podName := fmt.Sprintf("%s-%d", redisServiceName, ordinal)
			objects = append(objects, manifests.NewNodePortService(
				fmt.Sprintf("%s-redis-external-%d", redis.Name, ordinal),
				redis.Namespace,
				redisLabels,
				podSelector(redisLabels, podName),
				"redis",
				6379,
				6379,
				node.Port,
			))
		}

		sentinelExternalByOrdinal := indexExternalNodesByOrdinal(failoverExternal.Sentinel.Nodes)
		for ordinal := int32(0); ordinal < redis.Spec.Failover.SentinelReplicas; ordinal++ {
			node, ok := sentinelExternalByOrdinal[ordinal]
			if !ok {
				continue
			}
			podName := fmt.Sprintf("%s-%d", sentinelServiceName, ordinal)
			objects = append(objects, manifests.NewNodePortService(
				fmt.Sprintf("%s-sentinel-external-%d", redis.Name, ordinal),
				redis.Namespace,
				sentinelLabels,
				podSelector(sentinelLabels, podName),
				"sentinel",
				26379,
				26379,
				node.Port,
			))
		}
	}

	endpoints := redisv1alpha1.EndpointStatus{
		Internal: buildFailoverInternalSentinelEndpoints(redis.Namespace, sentinelServiceName, redis.Spec.Failover.SentinelReplicas),
	}
	if failoverExternal != nil {
		endpoints.External = orderedExternalAddresses(failoverExternal.Sentinel.Nodes, redis.Spec.Failover.SentinelReplicas)
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

func indexExternalNodesByOrdinal(nodes []redisv1alpha1.ExternalNodeAddress) map[int32]redisv1alpha1.ExternalAddress {
	result := make(map[int32]redisv1alpha1.ExternalAddress, len(nodes))
	for _, node := range nodes {
		result[node.Ordinal] = node.ExternalAddress
	}
	return result
}

func orderedExternalAddresses(nodes []redisv1alpha1.ExternalNodeAddress, replicas int32) []redisv1alpha1.ExternalAddress {
	if replicas < 1 {
		return nil
	}
	byOrdinal := indexExternalNodesByOrdinal(nodes)
	result := make([]redisv1alpha1.ExternalAddress, 0, replicas)
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		address, ok := byOrdinal[ordinal]
		if !ok {
			continue
		}
		result = append(result, address)
	}
	return result
}

func buildFailoverInternalSentinelEndpoints(namespace, serviceName string, replicas int32) []redisv1alpha1.ExternalAddress {
	if replicas < 1 {
		return nil
	}
	endpoints := make([]redisv1alpha1.ExternalAddress, 0, replicas)
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		endpoints = append(endpoints, redisv1alpha1.ExternalAddress{
			Host: fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local", serviceName, ordinal, serviceName, namespace),
			Port: 26379,
		})
	}
	return endpoints
}

func failoverExternalRedisNodes(spec *redisv1alpha1.FailoverExternalAccessSpec) []redisv1alpha1.ExternalNodeAddress {
	if spec == nil {
		return nil
	}
	return spec.Redis.Nodes
}

func failoverExternalSentinelNodes(spec *redisv1alpha1.FailoverExternalAccessSpec) []redisv1alpha1.ExternalNodeAddress {
	if spec == nil {
		return nil
	}
	return spec.Sentinel.Nodes
}
