package manifests

import (
	corev1 "k8s.io/api/core/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

const (
	DefaultRedisImage    = "docker.io/library/redis:8.6.1"
	DefaultExporterImage = "docker.io/oliver006/redis_exporter:v1.67.0"
	defaultStorageSize   = "10Gi"
)

// BaseLabels returns shared labels for all managed resources.
func BaseLabels(redis *redisv1alpha1.Redis) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "redis",
		"app.kubernetes.io/instance":   redis.Name,
		"app.kubernetes.io/managed-by": "redis-operator",
	}
}

// LabelsFor returns labels with component and optional shard markers.
func LabelsFor(redis *redisv1alpha1.Redis, component, shard string) map[string]string {
	labels := BaseLabels(redis)
	labels["app.kubernetes.io/component"] = component
	if shard != "" {
		labels["redis.storbase.io/shard"] = shard
	}
	return labels
}

func imageOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func pullPolicyOrDefault(policy corev1.PullPolicy) corev1.PullPolicy {
	if policy == "" {
		return corev1.PullIfNotPresent
	}
	return policy
}
