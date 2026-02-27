package manifests

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

// NewConfigMap creates a ConfigMap with provided labels and data.
func NewConfigMap(name, namespace string, labels map[string]string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Data: data,
	}
}

// RenderRedisConfig composes operator-managed and user-provided redis directives.
func RenderRedisConfig(mode redisv1alpha1.RedisMode, userLines []string) string {
	managed := []string{
		"bind 0.0.0.0 ::",
		"port 6379",
		"tcp-keepalive 300",
		"protected-mode no",
		"dir /data",
	}
	if mode == redisv1alpha1.RedisModeCluster {
		managed = append(managed,
			"cluster-enabled yes",
			"cluster-config-file /data/nodes.conf",
			"cluster-node-timeout 5000",
			"cluster-require-full-coverage no",
			"cluster-preferred-endpoint-type hostname",
		)
	}
	managed = append(managed, userLines...)
	return strings.Join(managed, "\n") + "\n"
}

// RenderSentinelConfig composes operator-managed and user-provided sentinel directives.
func RenderSentinelConfig(masterName, masterHost string, quorum int32, userLines []string) string {
	if quorum < 2 {
		quorum = 2
	}
	managed := []string{
		"port 26379",
		"dir /tmp",
		"sentinel resolve-hostnames yes",
		"sentinel announce-hostnames yes",
		fmt.Sprintf("sentinel monitor %s %s 6379 %d", masterName, masterHost, quorum),
		fmt.Sprintf("sentinel down-after-milliseconds %s 30000", masterName),
		fmt.Sprintf("sentinel parallel-syncs %s 1", masterName),
		fmt.Sprintf("sentinel failover-timeout %s 180000", masterName),
	}
	managed = append(managed, userLines...)
	return strings.Join(managed, "\n") + "\n"
}
