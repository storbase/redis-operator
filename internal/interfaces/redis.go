package interfaces

import "context"

// ClusterObservation reports current Redis Cluster topology metrics.
type ClusterObservation struct {
	State         string
	ClusterSize   int32
	KnownNodes    int32
	SlotsAssigned int32
}

// ReplicationNodeObservation reports one redis node replication role.
type ReplicationNodeObservation struct {
	Ordinal          int32
	Role             string
	MasterLinkStatus string
}

// FailoverObservation reports current failover topology roles.
type FailoverObservation struct {
	MasterOrdinal          int32
	ConsensusMasterOrdinal int32
	SentinelQuorumHealthy  bool
	Nodes                  []ReplicationNodeObservation
}

// ClusterShardObservation reports current roles inside one cluster shard.
type ClusterShardObservation struct {
	Shard          int32
	PrimaryOrdinal int32
	Nodes          []ReplicationNodeObservation
}

// ClusterAdminClient defines runtime hooks for Cluster mode.
type ClusterAdminClient interface {
	HealCluster(ctx context.Context, namespace, name string) error
	ObserveCluster(ctx context.Context, namespace, name string) (ClusterObservation, error)
	ObserveClusterShards(ctx context.Context, namespace, name string) ([]ClusterShardObservation, error)
	RequestClusterFailover(ctx context.Context, namespace, name string, shard, ordinal int32) error
}

// FailoverAdminClient defines runtime hooks for Failover mode.
type FailoverAdminClient interface {
	HealFailover(ctx context.Context, namespace, name string) error
	ObserveFailover(ctx context.Context, namespace, name string) (FailoverObservation, error)
	RequestFailover(ctx context.Context, namespace, name string) error
}

// RedisAdminClient defines redis/sentinel runtime repair hooks.
type RedisAdminClient interface {
	ClusterAdminClient
	FailoverAdminClient
}
