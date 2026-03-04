package cluster

import (
	"context"

	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

// HealRuntime executes cluster runtime heal checks.
func HealRuntime(ctx context.Context, admin appinterfaces.ClusterAdminClient, namespace, name string) error {
	return admin.HealCluster(ctx, namespace, name)
}

// ObserveRuntime returns current cluster topology observation.
func ObserveRuntime(ctx context.Context, admin appinterfaces.ClusterAdminClient, namespace, name string) (appinterfaces.ClusterObservation, error) {
	return admin.ObserveCluster(ctx, namespace, name)
}
