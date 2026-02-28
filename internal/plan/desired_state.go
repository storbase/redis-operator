package plan

import (
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	cfg "github.com/storbase/redis-operator/internal/config"
	"github.com/storbase/redis-operator/internal/manifests"
)

// DesiredState contains all objects that should exist for a Redis resource.
type DesiredState struct {
	Objects  []client.Object
	Endpoint string
}

// BuildDesiredState renders mode-specific desired Kubernetes objects.
func BuildDesiredState(redis *redisv1alpha1.Redis) (DesiredState, error) {
	if redis.Spec.Mode == redisv1alpha1.RedisModeCluster {
		return buildClusterDesiredState(redis)
	}
	return buildFailoverDesiredState(redis)
}

func buildClusterDesiredState(redis *redisv1alpha1.Redis) (DesiredState, error) {
	if redis.Spec.Cluster == nil {
		return DesiredState{}, fmt.Errorf("spec.cluster must be set when mode is Cluster")
	}
	if redis.Spec.Failover != nil {
		return DesiredState{}, fmt.Errorf("spec.failover must not be set when mode is Cluster")
	}
	if len(cfg.NormalizeUserLines(redis.Spec.SentinelConfig)) > 0 {
		return DesiredState{}, fmt.Errorf("spec.sentinelConfig is only valid in Failover mode")
	}

	userRedisConfig := cfg.NormalizeUserLines(redis.Spec.RedisConfig)

	configName := fmt.Sprintf("%s-redis-config", redis.Name)
	redisCM := manifests.NewConfigMap(configName, redis.Namespace, manifests.BaseLabels(redis), map[string]string{
		"redis.conf": manifests.RenderRedisConfig(redisv1alpha1.RedisModeCluster, userRedisConfig),
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
		})
		objects = append(objects, redisSTS)
		seeds = append(seeds, fmt.Sprintf("%s-0.%s.%s.svc:6379", shardName, shardName, redis.Namespace))
	}

	return DesiredState{Objects: objects, Endpoint: strings.Join(seeds, ",")}, nil
}

func buildFailoverDesiredState(redis *redisv1alpha1.Redis) (DesiredState, error) {
	if redis.Spec.Failover == nil {
		return DesiredState{}, fmt.Errorf("spec.failover must be set when mode is Failover")
	}
	if redis.Spec.Cluster != nil {
		return DesiredState{}, fmt.Errorf("spec.cluster must not be set when mode is Failover")
	}

	userRedisConfig := cfg.NormalizeUserLines(redis.Spec.RedisConfig)
	userSentinelConfig := cfg.NormalizeUserLines(redis.Spec.SentinelConfig)

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

	redisCM := manifests.NewConfigMap(redisConfigName, redis.Namespace, manifests.BaseLabels(redis), map[string]string{
		"redis.conf": manifests.RenderRedisConfig(redisv1alpha1.RedisModeFailover, userRedisConfig),
	})
	sentinelCM := manifests.NewConfigMap(sentinelConfigName, redis.Namespace, manifests.BaseLabels(redis), map[string]string{
		"sentinel.conf": manifests.RenderSentinelConfig(masterName, masterHost, redis.Spec.Failover.Quorum, userSentinelConfig),
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
		Command:       manifests.RenderFailoverRedisCommand(masterHost),
		ConfigMapName: redisConfigName,
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
		RedisPasswordRef:    redis.Spec.Auth.RedisPasswordSecretRef,
		SentinelPasswordRef: redis.Spec.Auth.SentinelPasswordSecretRef,
		Image:               redis.Spec.Image,
		ImagePullPolicy:     redis.Spec.ImagePullPolicy,
	})

	objects := []client.Object{redisCM, sentinelCM, redisHeadless, redisService, redisSTS, sentinelService, sentinelSTS}
	endpoint := fmt.Sprintf("%s.%s.svc:26379", sentinelServiceName, redis.Namespace)
	return DesiredState{Objects: objects, Endpoint: endpoint}, nil
}
