package redis

import (
	"context"

	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

// NoopAdminClient is a placeholder implementation for redis runtime operations.
type NoopAdminClient struct{}

// NewNoopAdminClient returns a redis client implementation that does nothing.
func NewNoopAdminClient() appinterfaces.RedisAdminClient {
	return &NoopAdminClient{}
}

func (n *NoopAdminClient) HealCluster(_ context.Context, _, _ string) error {
	return nil
}

func (n *NoopAdminClient) ObserveCluster(_ context.Context, _, _ string) (appinterfaces.ClusterObservation, error) {
	return appinterfaces.ClusterObservation{}, nil
}

func (n *NoopAdminClient) ObserveClusterShards(_ context.Context, _, _ string) ([]appinterfaces.ClusterShardObservation, error) {
	return nil, nil
}

func (n *NoopAdminClient) RequestClusterFailover(_ context.Context, _, _ string, _, _ int32) error {
	return nil
}

func (n *NoopAdminClient) HealFailover(_ context.Context, _, _ string) error {
	return nil
}

func (n *NoopAdminClient) ObserveFailover(_ context.Context, _, _ string) (appinterfaces.FailoverObservation, error) {
	return appinterfaces.FailoverObservation{}, nil
}

func (n *NoopAdminClient) RequestFailover(_ context.Context, _, _ string) error {
	return nil
}
