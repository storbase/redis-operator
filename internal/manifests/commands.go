package manifests

import "fmt"

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
func RenderFailoverRedisCommand(masterHost string) string {
	return fmt.Sprintf(`set -eu
cp /etc/redis-template/redis.conf /tmp/redis.conf
ordinal="${HOSTNAME##*-}"
self_host="${POD_NAME}.${HEADLESS_SERVICE}.${POD_NAMESPACE}.svc.cluster.local"
echo "replica-announce-ip ${self_host}" >> /tmp/redis.conf
echo "replica-announce-port 6379" >> /tmp/redis.conf
if [ "${ordinal}" != "0" ]; then
  echo "replicaof %s 6379" >> /tmp/redis.conf
fi
if [ -n "${REDIS_PASSWORD:-}" ]; then
  echo "masterauth ${REDIS_PASSWORD}" >> /tmp/redis.conf
  echo "requirepass ${REDIS_PASSWORD}" >> /tmp/redis.conf
fi
exec redis-server /tmp/redis.conf`, masterHost)
}
