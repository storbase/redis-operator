package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

type slotRange struct {
	Start int
	End   int
}

const (
	clusterReplicateRetryTimeout  = 45 * time.Second
	clusterReplicateRetryInterval = 500 * time.Millisecond
)

type clusterTopology struct {
	Masters         []string
	ReplicaToMaster map[string]string
	All             []string
	ExpectedNodes   int
	ExpectedShards  int
}

func (c *AdminClient) healCluster(ctx context.Context, redisObj *redisv1alpha1.Redis, password string, tlsConfig *tls.Config) error {
	topology, err := buildClusterTopology(
		redisObj.Namespace,
		redisObj.Name,
		redisObj.Spec.Cluster.Shards,
		redisObj.Spec.Cluster.ReplicasPerShard,
	)
	if err != nil {
		return err
	}

	ready, err := c.isClusterHealthy(ctx, topology.Masters, password, tlsConfig, topology.ExpectedNodes, topology.ExpectedShards)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}

	if err := c.ensureClusterNodesReachable(ctx, topology.All, password, tlsConfig); err != nil {
		return err
	}
	if err := c.bootstrapMeet(ctx, topology.Masters[0], topology.All[1:], password, tlsConfig); err != nil {
		return err
	}
	if err := c.bootstrapSlots(ctx, topology.Masters, password, tlsConfig); err != nil {
		return err
	}
	if err := c.bootstrapReplicas(ctx, topology.Masters, topology.ReplicaToMaster, password, tlsConfig); err != nil {
		return err
	}

	ready, err = c.isClusterHealthy(ctx, topology.Masters, password, tlsConfig, topology.ExpectedNodes, topology.ExpectedShards)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("cluster bootstrap not completed yet")
	}
	return nil
}

func buildClusterTopology(namespace, name string, shards, replicasPerShard int32) (clusterTopology, error) {
	if shards < 1 {
		return clusterTopology{}, fmt.Errorf("invalid shards: %d", shards)
	}
	if replicasPerShard < 0 {
		return clusterTopology{}, fmt.Errorf("invalid replicasPerShard: %d", replicasPerShard)
	}

	masters := make([]string, 0, shards)
	all := make([]string, 0, shards*(replicasPerShard+1))
	replicaToMaster := make(map[string]string)

	for shard := int32(0); shard < shards; shard++ {
		shardService := fmt.Sprintf("%s-shard-%d", name, shard)
		masterAddr := fmt.Sprintf(
			"%s-0.%s.%s.svc.%s:%d",
			shardService,
			shardService,
			namespace,
			clusterDomain,
			clusterPort,
		)
		masters = append(masters, masterAddr)
		all = append(all, masterAddr)
		for ordinal := int32(1); ordinal <= replicasPerShard; ordinal++ {
			replicaAddr := fmt.Sprintf(
				"%s-%d.%s.%s.svc.%s:%d",
				shardService,
				ordinal,
				shardService,
				namespace,
				clusterDomain,
				clusterPort,
			)
			all = append(all, replicaAddr)
			replicaToMaster[replicaAddr] = masterAddr
		}
	}

	return clusterTopology{
		Masters:         masters,
		ReplicaToMaster: replicaToMaster,
		All:             all,
		ExpectedNodes:   int(shards * (replicasPerShard + 1)),
		ExpectedShards:  int(shards),
	}, nil
}

func buildSlotRanges(shards int) []slotRange {
	if shards < 1 {
		return nil
	}

	ranges := make([]slotRange, 0, shards)
	base := totalClusterSlots / shards
	extra := totalClusterSlots % shards
	start := 0

	for i := 0; i < shards; i++ {
		width := base
		if i < extra {
			width++
		}
		end := start + width - 1
		ranges = append(ranges, slotRange{Start: start, End: end})
		start = end + 1
	}
	return ranges
}

func (c *AdminClient) ensureClusterNodesReachable(ctx context.Context, addrs []string, password string, tlsConfig *tls.Config) error {
	for _, addr := range addrs {
		cli := c.newRedisClient(addr, password, tlsConfig)
		commandCtx, cancel := c.commandContext(ctx)
		err := cli.Ping(commandCtx).Err()
		cancel()
		_ = cli.Close()
		if err != nil {
			return fmt.Errorf("redis node %s is not ready: %w", addr, err)
		}
	}
	return nil
}

func (c *AdminClient) isClusterHealthy(ctx context.Context, probes []string, password string, tlsConfig *tls.Config, expectedNodes, expectedShards int) (bool, error) {
	var lastErr error
	for _, addr := range probes {
		cli := c.newRedisClient(addr, password, tlsConfig)
		commandCtx, cancel := c.commandContext(ctx)
		raw, err := cli.ClusterInfo(commandCtx).Result()
		cancel()
		_ = cli.Close()
		if err != nil {
			lastErr = err
			continue
		}
		fields := parseInfoFields(raw)

		state := fields["cluster_state"]
		if state != "ok" {
			return false, nil
		}
		assigned, err := parseIntField(fields, "cluster_slots_assigned")
		if err != nil {
			return false, err
		}
		knownNodes, err := parseIntField(fields, "cluster_known_nodes")
		if err != nil {
			return false, err
		}
		clusterSize, err := parseIntField(fields, "cluster_size")
		if err != nil {
			return false, err
		}
		if assigned == totalClusterSlots && knownNodes >= expectedNodes && clusterSize == expectedShards {
			return true, nil
		}
		return false, nil
	}

	if lastErr != nil {
		return false, fmt.Errorf("cluster probe failed: %w", lastErr)
	}
	return false, nil
}

func (c *AdminClient) observeClusterInfo(
	ctx context.Context,
	probes []string,
	password string,
	tlsConfig *tls.Config,
) (appinterfaces.ClusterObservation, error) {
	var lastErr error
	for _, addr := range probes {
		cli := c.newRedisClient(addr, password, tlsConfig)
		commandCtx, cancel := c.commandContext(ctx)
		raw, err := cli.ClusterInfo(commandCtx).Result()
		cancel()
		_ = cli.Close()
		if err != nil {
			lastErr = err
			continue
		}

		fields := parseInfoFields(raw)
		clusterSize, err := parseIntField(fields, "cluster_size")
		if err != nil {
			return appinterfaces.ClusterObservation{}, err
		}
		knownNodes, err := parseIntField(fields, "cluster_known_nodes")
		if err != nil {
			return appinterfaces.ClusterObservation{}, err
		}
		slotsAssigned, err := parseIntField(fields, "cluster_slots_assigned")
		if err != nil {
			return appinterfaces.ClusterObservation{}, err
		}
		return appinterfaces.ClusterObservation{
			State:         fields["cluster_state"],
			ClusterSize:   clusterSize,
			KnownNodes:    knownNodes,
			SlotsAssigned: slotsAssigned,
		}, nil
	}
	if lastErr != nil {
		return appinterfaces.ClusterObservation{}, fmt.Errorf("cluster observation failed: %w", lastErr)
	}
	return appinterfaces.ClusterObservation{}, fmt.Errorf("cluster observation failed: no probe address available")
}

func (c *AdminClient) bootstrapMeet(ctx context.Context, coordinator string, peers []string, password string, tlsConfig *tls.Config) error {
	cli := c.newRedisClient(coordinator, password, tlsConfig)
	defer func() {
		_ = cli.Close()
	}()

	for _, peer := range peers {
		host, port, err := splitAddress(peer)
		if err != nil {
			return err
		}
		meetHost := c.resolveMeetHost(ctx, host)
		commandCtx, cancel := c.commandContext(ctx)
		err = cli.Do(commandCtx, "CLUSTER", "MEET", meetHost, port).Err()
		cancel()
		if err != nil && !isIgnorableMeetError(err) {
			return fmt.Errorf("cluster meet %s via %s failed: %w", peer, coordinator, err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	return nil
}

func (c *AdminClient) resolveMeetHost(ctx context.Context, host string) string {
	resolveCtx, cancel := c.commandContext(ctx)
	defer cancel()

	ips, err := c.lookupHost(resolveCtx, host)
	if err != nil {
		return host
	}
	return pickMeetHost(host, ips)
}

func pickMeetHost(host string, ips []string) string {
	return pickPreferredHost(host, ips)
}

func (c *AdminClient) bootstrapSlots(ctx context.Context, masters []string, password string, tlsConfig *tls.Config) error {
	slotRanges := buildSlotRanges(len(masters))
	for index, masterAddr := range masters {
		cli := c.newRedisClient(masterAddr, password, tlsConfig)
		commandCtx, cancel := c.commandContext(ctx)
		err := cli.Do(commandCtx, "CLUSTER", "ADDSLOTSRANGE", slotRanges[index].Start, slotRanges[index].End).Err()
		cancel()
		_ = cli.Close()
		if err != nil && !isIgnorableAddSlotsError(err) {
			return fmt.Errorf(
				"cluster addslotsrange %d-%d on %s failed: %w",
				slotRanges[index].Start,
				slotRanges[index].End,
				masterAddr,
				err,
			)
		}
	}
	return nil
}

func (c *AdminClient) bootstrapReplicas(ctx context.Context, masters []string, replicaToMaster map[string]string, password string, tlsConfig *tls.Config) error {
	masterIDs := make(map[string]string, len(masters))
	for _, masterAddr := range masters {
		cli := c.newRedisClient(masterAddr, password, tlsConfig)
		commandCtx, cancel := c.commandContext(ctx)
		id, err := cli.Do(commandCtx, "CLUSTER", "MYID").Text()
		cancel()
		_ = cli.Close()
		if err != nil {
			return fmt.Errorf("cluster myid on %s failed: %w", masterAddr, err)
		}
		masterIDs[masterAddr] = id
	}

	for replicaAddr, masterAddr := range replicaToMaster {
		masterID, ok := masterIDs[masterAddr]
		if !ok {
			return fmt.Errorf("master id missing for %s", masterAddr)
		}
		cli := c.newRedisClient(replicaAddr, password, tlsConfig)
		err := c.replicateWithRetry(ctx, cli, masterID)
		_ = cli.Close()
		if err != nil && !isIgnorableReplicateError(err) {
			return fmt.Errorf("cluster replicate %s -> %s failed: %w", replicaAddr, masterAddr, err)
		}
	}
	return nil
}

func (c *AdminClient) replicateWithRetry(ctx context.Context, cli *redis.Client, masterID string) error {
	deadline := time.Now().Add(clusterReplicateRetryTimeout)
	for {
		commandCtx, cancel := c.commandContext(ctx)
		err := cli.Do(commandCtx, "CLUSTER", "REPLICATE", masterID).Err()
		cancel()
		if err == nil || isIgnorableReplicateError(err) {
			return nil
		}
		if !isRetryableReplicateError(err) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(clusterReplicateRetryInterval)
	}
}

func isIgnorableMeetError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already known") || strings.Contains(message, "duplicate")
}

func isIgnorableAddSlotsError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already busy") || strings.Contains(message, "busy")
}

func isIgnorableReplicateError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already") || strings.Contains(message, "replicate a master")
}

func isRetryableReplicateError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown node") ||
		strings.Contains(message, "no such master with that id") ||
		strings.Contains(message, "try again")
}
