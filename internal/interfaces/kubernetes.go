package interfaces

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesClient defines all Kubernetes API interactions used by the reconciler.
type KubernetesClient interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object) error
	Apply(ctx context.Context, obj client.Object) error
	Patch(ctx context.Context, obj client.Object, patch client.Patch) error
	PatchStatus(ctx context.Context, obj client.Object, patch client.Patch) error
}
