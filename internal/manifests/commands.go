package manifests

import (
	"fmt"
	"strings"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

// RenderClusterRedisCommand starts redis-server with cluster announce hostname.
func RenderClusterRedisCommand() string {
	return `set -eu
cp /etc/redis-template/redis.conf /tmp/redis.conf
if [ -n "${REDIS_PASSWORD:-}" ]; then
  echo "masterauth ${REDIS_PASSWORD}" >> /tmp/redis.conf
  echo "requirepass ${REDIS_PASSWORD}" >> /tmp/redis.conf
fi
exec redis-server /tmp/redis.conf --cluster-announce-hostname ${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc`
}

// RenderFailoverRedisCommand starts redis-server and configures replicas by ordinal.
func RenderFailoverRedisCommand(masterHost string, externalEndpoints []redisv1alpha1.ExternalNodeAddress) string {
	var builder strings.Builder
	builder.WriteString(`set -eu
cp /etc/redis-template/redis.conf /tmp/redis.conf
ordinal="${HOSTNAME##*-}"
self_host="${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
announce_host="${self_host}"
announce_port="6379"
`)
	if len(externalEndpoints) > 0 {
		builder.WriteString("case \"${ordinal}\" in\n")
		for _, endpoint := range externalEndpoints {
			builder.WriteString(fmt.Sprintf("%d)\n", endpoint.Ordinal))
			builder.WriteString(fmt.Sprintf("  announce_host=\"%s\"\n", endpoint.Host))
			builder.WriteString(fmt.Sprintf("  announce_port=\"%d\"\n", endpoint.Port))
			builder.WriteString("  ;;\n")
		}
		builder.WriteString("esac\n")
	}
	builder.WriteString(`echo "replica-announce-ip ${announce_host}" >> /tmp/redis.conf
echo "replica-announce-port ${announce_port}" >> /tmp/redis.conf
if [ "${ordinal}" != "0" ]; then
  echo "replicaof `)
	builder.WriteString(masterHost)
	builder.WriteString(` 6379" >> /tmp/redis.conf
fi
if [ -n "${REDIS_PASSWORD:-}" ]; then
  echo "masterauth ${REDIS_PASSWORD}" >> /tmp/redis.conf
  echo "requirepass ${REDIS_PASSWORD}" >> /tmp/redis.conf
fi
exec redis-server /tmp/redis.conf`)
	return builder.String()
}
