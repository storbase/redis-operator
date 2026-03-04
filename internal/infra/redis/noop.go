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

func (n *NoopAdminClient) HealFailover(_ context.Context, _, _ string) error {
	return nil
}
