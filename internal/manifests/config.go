package manifests

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

const (
	redisTLSCertFile = "/etc/redis-tls/tls.crt"
	redisTLSKeyFile  = "/etc/redis-tls/tls.key"
	redisTLSCAFile   = "/etc/redis-tls/ca.crt"
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
func RenderRedisConfig(mode redisv1alpha1.RedisMode, userLines []string, tlsEnabled bool) string {
	managed := []string{
		"bind 0.0.0.0 ::",
		"tcp-keepalive 300",
		"protected-mode no",
		"dir /data",
	}
	if tlsEnabled {
		managed = append(managed,
			"port 0",
			"tls-port 6379",
			fmt.Sprintf("tls-cert-file %s", redisTLSCertFile),
			fmt.Sprintf("tls-key-file %s", redisTLSKeyFile),
			fmt.Sprintf("tls-ca-cert-file %s", redisTLSCAFile),
			"tls-replication yes",
			"tls-auth-clients no",
		)
	} else {
		managed = append(managed, "port 6379")
	}
	if mode == redisv1alpha1.RedisModeCluster {
		managed = append(managed,
			"cluster-enabled yes",
			"cluster-config-file /data/nodes.conf",
			"cluster-node-timeout 5000",
			"cluster-require-full-coverage no",
			"cluster-preferred-endpoint-type hostname",
		)
		if tlsEnabled {
			managed = append(managed, "tls-cluster yes")
		}
	}
	managed = append(managed, userLines...)
	return strings.Join(managed, "\n") + "\n"
}

// RenderSentinelConfig composes operator-managed and user-provided sentinel directives.
func RenderSentinelConfig(
	masterName,
	masterHost string,
	masterPort int32,
	quorum int32,
	userLines []string,
	tlsEnabled bool,
) string {
	if quorum < 2 {
		quorum = 2
	}
	if masterPort < 1 {
		masterPort = 6379
	}
	managed := []string{"dir /tmp"}
	if tlsEnabled {
		managed = append(managed,
			"port 0",
			"tls-port 26379",
			fmt.Sprintf("tls-cert-file %s", redisTLSCertFile),
			fmt.Sprintf("tls-key-file %s", redisTLSKeyFile),
			fmt.Sprintf("tls-ca-cert-file %s", redisTLSCAFile),
			"tls-replication yes",
			"tls-auth-clients no",
		)
	} else {
		managed = append(managed, "port 26379")
	}
	managed = append(managed,
		"sentinel resolve-hostnames yes",
		"sentinel announce-hostnames yes",
		fmt.Sprintf("sentinel monitor %s %s %d %d", masterName, masterHost, masterPort, quorum),
		fmt.Sprintf("sentinel down-after-milliseconds %s 30000", masterName),
		fmt.Sprintf("sentinel parallel-syncs %s 1", masterName),
		fmt.Sprintf("sentinel failover-timeout %s 180000", masterName),
	)
	managed = append(managed, userLines...)
	return strings.Join(managed, "\n") + "\n"
}
