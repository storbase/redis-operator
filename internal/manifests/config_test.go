package manifests

import (
	"strings"
	"testing"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestRenderRedisConfigWithTLS(t *testing.T) {
	config := RenderRedisConfig(redisv1alpha1.RedisModeCluster, nil, true)
	required := []string{
		"port 0",
		"tls-port 6379",
		"tls-cert-file /etc/redis-tls/tls.crt",
		"tls-key-file /etc/redis-tls/tls.key",
		"tls-ca-cert-file /etc/redis-tls/ca.crt",
		"tls-replication yes",
		"tls-cluster yes",
		"tls-auth-clients no",
	}
	for _, item := range required {
		if !strings.Contains(config, item) {
			t.Fatalf("missing expected config line %q", item)
		}
	}
	if strings.Contains(config, "\nport 6379\n") {
		t.Fatalf("unexpected plaintext redis port in tls mode")
	}
}

func TestRenderSentinelConfigWithTLS(t *testing.T) {
	config := RenderSentinelConfig("mymaster", "redis-0.redis.default.svc.cluster.local", 6379, 2, nil, true)
	required := []string{
		"port 0",
		"tls-port 26379",
		"tls-cert-file /etc/redis-tls/tls.crt",
		"tls-key-file /etc/redis-tls/tls.key",
		"tls-ca-cert-file /etc/redis-tls/ca.crt",
		"tls-replication yes",
		"tls-auth-clients no",
	}
	for _, item := range required {
		if !strings.Contains(config, item) {
			t.Fatalf("missing expected config line %q", item)
		}
	}
	if strings.Contains(config, "\nport 26379\n") {
		t.Fatalf("unexpected plaintext sentinel port in tls mode")
	}
	if !strings.Contains(config, "sentinel monitor mymaster redis-0.redis.default.svc.cluster.local 6379 2") {
		t.Fatalf("expected sentinel monitor line with host and port")
	}
}
