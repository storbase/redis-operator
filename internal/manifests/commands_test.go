package manifests

import (
	"strings"
	"testing"
)

func TestRenderFailoverRedisCommandIncludesReplicaAnnounce(t *testing.T) {
	masterHost := "redis-failover-redis-0.redis-failover-redis-headless.redis-e2e.svc.cluster.local"
	command := RenderFailoverRedisCommand(masterHost)

	required := []string{
		`self_host="${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc.cluster.local"`,
		`echo "replica-announce-ip ${self_host}" >> /tmp/redis.conf`,
		`echo "replica-announce-port 6379" >> /tmp/redis.conf`,
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
