package failover

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	cfg "github.com/storbase/redis-operator/internal/config"
	"github.com/storbase/redis-operator/internal/manifests"
)

// BuildDesiredState renders Kubernetes objects for Failover mode.
func BuildDesiredState(redis *redisv1alpha1.Redis) ([]client.Object, string, error) {
	if redis.Spec.Failover == nil {
		return nil, "", fmt.Errorf("spec.failover must be set when mode is Failover")
	}
	if redis.Spec.Cluster != nil {
		return nil, "", fmt.Errorf("spec.cluster must not be set when mode is Failover")
	}

	userRedisConfig := cfg.NormalizeUserLines(redis.Spec.RedisConfig)
	userSentinelConfig := cfg.NormalizeUserLines(redis.Spec.SentinelConfig)
	tlsEnabled := redis.Spec.TLS != nil
	tlsSecretName := ""
	if redis.Spec.TLS != nil {
		tlsSecretName = redis.Spec.TLS.SecretName
	}

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
		"redis.conf": manifests.RenderRedisConfig(redisv1alpha1.RedisModeFailover, userRedisConfig, tlsEnabled),
	})
	sentinelCM := manifests.NewConfigMap(sentinelConfigName, redis.Namespace, manifests.BaseLabels(redis), map[string]string{
		"sentinel.conf": manifests.RenderSentinelConfig(masterName, masterHost, redis.Spec.Failover.Quorum, userSentinelConfig, tlsEnabled),
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
		RedisPasswordRef:    redis.Spec.Auth.RedisPasswordSecretRef,
		SentinelPasswordRef: redis.Spec.Auth.SentinelPasswordSecretRef,
		Image:               redis.Spec.Image,
		ImagePullPolicy:     redis.Spec.ImagePullPolicy,
		TLSSecretName:       tlsSecretName,
	})

	objects := []client.Object{redisCM, sentinelCM, redisHeadless, redisService, redisSTS, sentinelService, sentinelSTS}
	endpoint := fmt.Sprintf("%s.%s.svc:26379", sentinelServiceName, redis.Namespace)
	return objects, endpoint, nil
}
