package interfaces

import "context"

// RedisAdminClient defines redis/sentinel runtime repair hooks.
type RedisAdminClient interface {
	HealCluster(ctx context.Context, namespace, name string) error
	HealFailover(ctx context.Context, namespace, name string) error
}
