package failover

import (
	"context"

	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

// HealRuntime executes failover runtime heal checks.
func HealRuntime(ctx context.Context, admin appinterfaces.FailoverAdminClient, namespace, name string) error {
	return admin.HealFailover(ctx, namespace, name)
}
