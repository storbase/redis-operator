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

func TestRenderClusterRedisCommandUsesAnnounceHostname(t *testing.T) {
	command := RenderClusterRedisCommand()
	if !strings.Contains(command, "--cluster-announce-hostname ${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc") {
		t.Fatalf("cluster command should include cluster announce hostname")
	}
}
