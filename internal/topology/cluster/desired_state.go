package cluster

import (
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	cfg "github.com/storbase/redis-operator/internal/config"
	"github.com/storbase/redis-operator/internal/manifests"
)

// BuildDesiredState renders Kubernetes objects for Cluster mode.
func BuildDesiredState(redis *redisv1alpha1.Redis) ([]client.Object, string, error) {
	if redis.Spec.Cluster == nil {
		return nil, "", fmt.Errorf("spec.cluster must be set when mode is Cluster")
	}
	if redis.Spec.Failover != nil {
		return nil, "", fmt.Errorf("spec.failover must not be set when mode is Cluster")
	}
	if len(cfg.NormalizeUserLines(redis.Spec.SentinelConfig)) > 0 {
		return nil, "", fmt.Errorf("spec.sentinelConfig is only valid in Failover mode")
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
	seeds := make([]string, 0, redis.Spec.Cluster.Shards)

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
			Command:       manifests.RenderClusterRedisCommand(),
			ConfigMapName: configName,
			TLSSecretName: tlsSecretName,
		})
		objects = append(objects, redisSTS)
		seeds = append(seeds, fmt.Sprintf("%s-0.%s.%s.svc:6379", shardName, shardName, redis.Namespace))
	}

	return objects, strings.Join(seeds, ","), nil
}
