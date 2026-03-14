package manifests

import (
	"fmt"
	"strings"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

// RenderClusterRedisCommand starts redis-server and configures cluster announce settings by ordinal.
func RenderClusterRedisCommand(externalEndpoints []redisv1alpha1.ClusterExternalNodeAddress) string {
	var builder strings.Builder
	builder.WriteString(`set -eu
cp /etc/redis-template/redis.conf /tmp/redis.conf
ordinal="${HOSTNAME##*-}"
self_host="${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc"
announce_host="${self_host}"
announce_ip=""
announce_port="6379"
announce_bus_port="16379"
`)
	if len(externalEndpoints) > 0 {
		builder.WriteString("case \"${ordinal}\" in\n")
		for _, endpoint := range externalEndpoints {
			fmt.Fprintf(&builder, "%d)\n", endpoint.Ordinal)
			fmt.Fprintf(&builder, "  announce_host=\"%s\"\n", endpoint.Host)
			fmt.Fprintf(&builder, "  announce_ip=\"%s\"\n", endpoint.Host)
			fmt.Fprintf(&builder, "  announce_port=\"%d\"\n", endpoint.Port)
			fmt.Fprintf(&builder, "  announce_bus_port=\"%d\"\n", endpoint.BusPort)
			builder.WriteString("  ;;\n")
		}
		builder.WriteString("esac\n")
	}
	builder.WriteString(`echo "cluster-announce-hostname ${announce_host}" >> /tmp/redis.conf
echo "cluster-announce-port ${announce_port}" >> /tmp/redis.conf
echo "cluster-announce-bus-port ${announce_bus_port}" >> /tmp/redis.conf
if [ -n "${announce_ip}" ]; then
  echo "cluster-announce-ip ${announce_ip}" >> /tmp/redis.conf
fi
if [ -n "${REDIS_PASSWORD:-}" ]; then
  echo "masterauth ${REDIS_PASSWORD}" >> /tmp/redis.conf
  echo "requirepass ${REDIS_PASSWORD}" >> /tmp/redis.conf
fi
exec redis-server /tmp/redis.conf`)
	return builder.String()
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
			fmt.Fprintf(&builder, "%d)\n", endpoint.Ordinal)
			fmt.Fprintf(&builder, "  announce_host=\"%s\"\n", endpoint.Host)
			fmt.Fprintf(&builder, "  announce_port=\"%d\"\n", endpoint.Port)
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
