package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

const (
	failoverReplicaRepairAttempts = 6
	failoverReplicaRepairDelay    = 500 * time.Millisecond
)

func (c *AdminClient) healFailover(ctx context.Context, redisObj *redisv1alpha1.Redis, redisPassword, sentinelPassword string, tlsConfig *tls.Config) error {
	masterName := redisObj.Spec.Failover.MasterName
	if masterName == "" {
		masterName = defaultMasterName
	}

	redisAddrs := buildFailoverRedisAddresses(redisObj.Namespace, redisObj.Name, redisObj.Spec.Failover.RedisReplicas)
	if len(redisAddrs) == 0 {
		return fmt.Errorf("no redis addresses generated")
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
		masterAddr, err = normalizeFailoverMasterAddress(masterAddr, redisAddrs)
		if err != nil {
			lastErr = err
			continue
		}
		if err := c.checkMasterHealth(ctx, masterAddr, redisPassword, tlsConfig); err != nil {
			lastErr = err
			continue
		}
		if err := c.reconcileFailoverReplicas(ctx, redisAddrs, masterAddr, redisPassword, tlsConfig); err != nil {
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

func buildFailoverRedisAddresses(namespace, name string, replicas int32) []string {
	if replicas < 1 {
		return nil
	}

	statefulSetName := fmt.Sprintf("%s-redis", name)
	headlessService := fmt.Sprintf("%s-headless", statefulSetName)
	addrs := make([]string, 0, replicas)
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		addrs = append(
			addrs,
			fmt.Sprintf(
				"%s-%d.%s.%s.svc.%s:%d",
				statefulSetName,
				ordinal,
				headlessService,
				namespace,
				clusterDomain,
				clusterPort,
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

func (c *AdminClient) reconcileFailoverReplicas(
	ctx context.Context,
	redisAddrs []string,
	masterAddr,
	redisPassword string,
	tlsConfig *tls.Config,
) error {
	masterHost, masterPort, err := splitAddress(masterAddr)
	if err != nil {
		return err
	}
	masterPortInt, err := strconv.Atoi(masterPort)
	if err != nil {
		return fmt.Errorf("parse master port %q failed: %w", masterPort, err)
	}

	aliases := buildFailoverHostAliases(redisAddrs)
	normalizedMasterHost, ok := normalizeFailoverHost(masterHost, aliases)
	if !ok {
		return fmt.Errorf("master host %q does not match expected failover redis pods", masterHost)
	}

	for _, redisAddr := range redisAddrs {
		redisHost, _, err := splitAddress(redisAddr)
		if err != nil {
			return err
		}
		normalizedRedisHost, ok := normalizeFailoverHost(redisHost, aliases)
		if !ok {
			return fmt.Errorf("redis host %q does not match expected failover redis pods", redisHost)
		}

		fields, err := c.readReplicationInfo(ctx, redisAddr, redisPassword, tlsConfig)
		if err != nil {
			return err
		}

		if normalizedRedisHost == normalizedMasterHost {
			if !strings.EqualFold(fields["role"], "master") {
				return fmt.Errorf("expected master role on %s, got %q", redisAddr, fields["role"])
			}
			continue
		}

		if replicaTracksMaster(fields, aliases, normalizedMasterHost, masterPort) {
			continue
		}

		if err := c.setReplicaOf(ctx, redisAddr, redisPassword, tlsConfig, normalizedMasterHost, masterPortInt); err != nil {
			return err
		}
		if err := c.waitReplicaTrackingMaster(ctx, redisAddr, redisPassword, tlsConfig, aliases, normalizedMasterHost, masterPort); err != nil {
			return err
		}
	}

	return nil
}

func (c *AdminClient) readReplicationInfo(ctx context.Context, addr, password string, tlsConfig *tls.Config) (map[string]string, error) {
	cli := c.newRedisClient(addr, password, tlsConfig)
	defer func() {
		_ = cli.Close()
	}()

	commandCtx, cancel := c.commandContext(ctx)
	raw, err := cli.Info(commandCtx, "replication").Result()
	cancel()
	if err != nil {
		return nil, fmt.Errorf("fetch replication info from %s failed: %w", addr, err)
	}
	return parseInfoFields(raw), nil
}

func (c *AdminClient) setReplicaOf(
	ctx context.Context,
	replicaAddr,
	password string,
	tlsConfig *tls.Config,
	masterHost string,
	masterPort int,
) error {
	cli := c.newRedisClient(replicaAddr, password, tlsConfig)
	defer func() {
		_ = cli.Close()
	}()

	commandCtx, cancel := c.commandContext(ctx)
	err := cli.Do(commandCtx, "REPLICAOF", masterHost, masterPort).Err()
	cancel()
	if err != nil {
		return fmt.Errorf("set replicaof on %s to %s:%d failed: %w", replicaAddr, masterHost, masterPort, err)
	}
	return nil
}

func (c *AdminClient) waitReplicaTrackingMaster(
	ctx context.Context,
	replicaAddr,
	password string,
	tlsConfig *tls.Config,
	aliases map[string]string,
	masterHost,
	masterPort string,
) error {
	var lastFields map[string]string
	var lastErr error

	for attempt := 0; attempt < failoverReplicaRepairAttempts; attempt++ {
		fields, err := c.readReplicationInfo(ctx, replicaAddr, password, tlsConfig)
		if err == nil && replicaTracksMaster(fields, aliases, masterHost, masterPort) {
			return nil
		}
		lastFields = fields
		lastErr = err
		if attempt < failoverReplicaRepairAttempts-1 {
			time.Sleep(failoverReplicaRepairDelay)
		}
	}

	if lastErr != nil {
		return fmt.Errorf("verify replica tracking on %s failed: %w", replicaAddr, lastErr)
	}
	return fmt.Errorf(
		"replica %s is not tracking master %s:%s after repair (role=%q master_host=%q master_port=%q master_link_status=%q)",
		replicaAddr,
		masterHost,
		masterPort,
		lastFields["role"],
		lastFields["master_host"],
		lastFields["master_port"],
		lastFields["master_link_status"],
	)
}

func normalizeFailoverMasterAddress(masterAddr string, redisAddrs []string) (string, error) {
	host, port, err := splitAddress(masterAddr)
	if err != nil {
		return "", err
	}
	if port != strconv.Itoa(clusterPort) {
		return "", fmt.Errorf("sentinel returned unexpected master port %q", port)
	}

	aliases := buildFailoverHostAliases(redisAddrs)
	normalizedHost, ok := normalizeFailoverHost(host, aliases)
	if !ok {
		if isIPLikeHost(host) {
			return "", fmt.Errorf("sentinel returned ip-like master host %q", host)
		}
		return "", fmt.Errorf("sentinel returned unknown master host %q", host)
	}
	return net.JoinHostPort(normalizedHost, port), nil
}

func buildFailoverHostAliases(redisAddrs []string) map[string]string {
	aliases := make(map[string]string, len(redisAddrs)*5)
	for _, addr := range redisAddrs {
		host, _, err := splitAddress(addr)
		if err != nil {
			continue
		}
		canonical := strings.ToLower(strings.TrimSuffix(host, "."))
		if canonical == "" {
			continue
		}
		aliases[canonical] = canonical

		parts := strings.Split(canonical, ".")
		for i := 1; i <= 4 && i <= len(parts); i++ {
			alias := strings.Join(parts[:i], ".")
			aliases[alias] = canonical
		}
	}
	return aliases
}

func normalizeFailoverHost(rawHost string, aliases map[string]string) (string, bool) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rawHost), "."))
	if host == "" {
		return "", false
	}
	if isIPLikeHost(host) {
		return "", false
	}
	canonical, ok := aliases[host]
	return canonical, ok
}

func isIPLikeHost(host string) bool {
	host = strings.Trim(host, "[]")
	return net.ParseIP(host) != nil
}

func replicaTracksMaster(fields map[string]string, aliases map[string]string, masterHost, masterPort string) bool {
	if fields == nil {
		return false
	}
	role := strings.ToLower(strings.TrimSpace(fields["role"]))
	if role != "slave" && role != "replica" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(fields["master_link_status"]), "up") {
		return false
	}
	if strings.TrimSpace(fields["master_port"]) != masterPort {
		return false
	}
	replicaMasterHost, ok := normalizeFailoverHost(fields["master_host"], aliases)
	if !ok {
		return false
	}
	return replicaMasterHost == masterHost
}
