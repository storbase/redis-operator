package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func (c *AdminClient) healFailover(ctx context.Context, redisObj *redisv1alpha1.Redis, redisPassword, sentinelPassword string, tlsConfig *tls.Config) error {
	masterName := redisObj.Spec.Failover.MasterName
	if masterName == "" {
		masterName = defaultMasterName
	}

	sentinelAddrs := buildSentinelAddresses(redisObj.Namespace, redisObj.Name, redisObj.Spec.Failover.SentinelReplicas)
	if len(sentinelAddrs) == 0 {
		return fmt.Errorf("no sentinel addresses generated")
	}

	var lastErr error
	for _, addr := range sentinelAddrs {
		masterAddr, err := c.checkSentinelHealth(ctx, addr, masterName, sentinelPassword, tlsConfig)
		if err != nil {
			lastErr = err
			continue
		}
		if err := c.checkMasterHealth(ctx, masterAddr, redisPassword, tlsConfig); err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("all sentinel checks failed: %w", lastErr)
	}
	return fmt.Errorf("all sentinel checks failed")
}

func buildSentinelAddresses(namespace, name string, replicas int32) []string {
	if replicas < 1 {
		return nil
	}

	serviceName := fmt.Sprintf("%s-sentinel", name)
	addrs := make([]string, 0, replicas)
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		addrs = append(
			addrs,
			fmt.Sprintf(
				"%s-%d.%s.%s.svc.%s:%d",
				serviceName,
				ordinal,
				serviceName,
				namespace,
				clusterDomain,
				sentinelPort,
			),
		)
	}
	return addrs
}

func (c *AdminClient) checkSentinelHealth(ctx context.Context, sentinelAddr, masterName, sentinelPassword string, tlsConfig *tls.Config) (string, error) {
	sentinel := c.newSentinelClient(sentinelAddr, sentinelPassword, tlsConfig)
	defer func() {
		_ = sentinel.Close()
	}()

	commandCtx, cancel := c.commandContext(ctx)
	err := sentinel.CkQuorum(commandCtx, masterName).Err()
	cancel()
	if err != nil {
		return "", fmt.Errorf("sentinel ckquorum via %s failed: %w", sentinelAddr, err)
	}

	commandCtx, cancel = c.commandContext(ctx)
	masterEndpoint, err := sentinel.GetMasterAddrByName(commandCtx, masterName).Result()
	cancel()
	if err != nil {
		return "", fmt.Errorf("sentinel get-master-addr-by-name via %s failed: %w", sentinelAddr, err)
	}
	if len(masterEndpoint) != 2 {
		return "", fmt.Errorf("sentinel returned invalid master endpoint via %s: %v", sentinelAddr, masterEndpoint)
	}
	return net.JoinHostPort(masterEndpoint[0], masterEndpoint[1]), nil
}

func (c *AdminClient) checkMasterHealth(ctx context.Context, masterAddr, redisPassword string, tlsConfig *tls.Config) error {
	cli := c.newRedisClient(masterAddr, redisPassword, tlsConfig)
	defer func() {
		_ = cli.Close()
	}()

	commandCtx, cancel := c.commandContext(ctx)
	if err := cli.Ping(commandCtx).Err(); err != nil {
		cancel()
		return fmt.Errorf("ping master %s failed: %w", masterAddr, err)
	}
	cancel()

	commandCtx, cancel = c.commandContext(ctx)
	raw, err := cli.Info(commandCtx, "replication").Result()
	cancel()
	if err != nil {
		return fmt.Errorf("fetch replication info from %s failed: %w", masterAddr, err)
	}
	fields := parseInfoFields(raw)
	if !strings.EqualFold(fields["role"], "master") {
		return fmt.Errorf("master %s has unexpected role %q", masterAddr, fields["role"])
	}
	return nil
}
