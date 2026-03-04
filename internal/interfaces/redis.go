package interfaces

import "context"

// ClusterObservation reports current Redis Cluster topology metrics.
type ClusterObservation struct {
	State         string
	ClusterSize   int32
	KnownNodes    int32
	SlotsAssigned int32
}

// ClusterAdminClient defines runtime hooks for Cluster mode.
type ClusterAdminClient interface {
	HealCluster(ctx context.Context, namespace, name string) error
	ObserveCluster(ctx context.Context, namespace, name string) (ClusterObservation, error)
}

// FailoverAdminClient defines runtime hooks for Failover mode.
type FailoverAdminClient interface {
	HealFailover(ctx context.Context, namespace, name string) error
}

// RedisAdminClient defines redis/sentinel runtime repair hooks.
type RedisAdminClient interface {
	ClusterAdminClient
	FailoverAdminClient
}
