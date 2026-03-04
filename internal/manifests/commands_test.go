package manifests

import (
	"strings"
	"testing"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestRenderFailoverRedisCommandIncludesReplicaAnnounce(t *testing.T) {
	masterHost := "redis-failover-redis-0.redis-failover-redis-headless.redis-e2e.svc.cluster.local"
	command := RenderFailoverRedisCommand(masterHost, []redisv1alpha1.ExternalNodeAddress{
		{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 31010}},
		{Ordinal: 1, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 31011}},
	})

	required := []string{
		`self_host="${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc.cluster.local"`,
		`announce_host="${self_host}"`,
		`announce_port="6379"`,
		`case "${ordinal}" in`,
		`0)`,
		`announce_host="10.0.0.10"`,
		`announce_port="31010"`,
		`echo "replica-announce-ip ${announce_host}" >> /tmp/redis.conf`,
		`echo "replica-announce-port ${announce_port}" >> /tmp/redis.conf`,
		`echo "replicaof redis-failover-redis-0.redis-failover-redis-headless.redis-e2e.svc.cluster.local 6379" >> /tmp/redis.conf`,
	}
	for _, item := range required {
		if !strings.Contains(command, item) {
			t.Fatalf("missing expected command fragment %q", item)
		}
	}
}

func TestRenderClusterRedisCommandUsesAnnounceDefaults(t *testing.T) {
	command := RenderClusterRedisCommand(nil)
	required := []string{
		`self_host="${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc"`,
		`announce_host="${self_host}"`,
		`announce_ip=""`,
		`announce_port="6379"`,
		`announce_bus_port="16379"`,
		`echo "cluster-announce-hostname ${announce_host}" >> /tmp/redis.conf`,
		`echo "cluster-announce-port ${announce_port}" >> /tmp/redis.conf`,
		`echo "cluster-announce-bus-port ${announce_bus_port}" >> /tmp/redis.conf`,
		`if [ -n "${announce_ip}" ]; then`,
		`echo "cluster-announce-ip ${announce_ip}" >> /tmp/redis.conf`,
	}
	for _, item := range required {
		if !strings.Contains(command, item) {
			t.Fatalf("missing expected command fragment %q", item)
		}
	}
}

func TestRenderClusterRedisCommandUsesExternalAnnounceByOrdinal(t *testing.T) {
	command := RenderClusterRedisCommand([]redisv1alpha1.ClusterExternalNodeAddress{
		{
			Shard:   0,
			Ordinal: 0,
			ExternalAddress: redisv1alpha1.ExternalAddress{
				Host: "node-1.example.com",
				Port: 32090,
			},
			BusPort: 32190,
		},
		{
			Shard:   0,
			Ordinal: 1,
			ExternalAddress: redisv1alpha1.ExternalAddress{
				Host: "node-2.example.com",
				Port: 32091,
			},
			BusPort: 32191,
		},
	})
	required := []string{
		`case "${ordinal}" in`,
		`0)`,
		`announce_host="node-1.example.com"`,
		`announce_ip="node-1.example.com"`,
		`announce_port="32090"`,
		`announce_bus_port="32190"`,
		`1)`,
		`announce_host="node-2.example.com"`,
		`announce_ip="node-2.example.com"`,
		`announce_port="32091"`,
		`announce_bus_port="32191"`,
	}
	for _, item := range required {
		if !strings.Contains(command, item) {
			t.Fatalf("missing expected command fragment %q", item)
		}
	}
}
