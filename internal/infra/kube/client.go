package kube

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

// Client wraps controller-runtime client behind an interface.
type Client struct {
	client client.Client
}

// NewClient creates a new KubernetesClient implementation.
func NewClient(c client.Client) appinterfaces.KubernetesClient {
	return &Client{client: c}
}

func (c *Client) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	return c.client.Get(ctx, key, obj)
}

func (c *Client) Apply(ctx context.Context, obj client.Object) error {
	current := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)

	err := c.client.Get(ctx, key, current)
	if apierrors.IsNotFound(err) {
		return c.client.Create(ctx, obj)
	}
	if err != nil {
		return err
	}

	obj.SetResourceVersion(current.GetResourceVersion())
	return c.client.Update(ctx, obj)
}

func (c *Client) Patch(ctx context.Context, obj client.Object, patch client.Patch) error {
	return c.client.Patch(ctx, obj, patch)
}

func (c *Client) PatchStatus(ctx context.Context, obj client.Object, patch client.Patch) error {
	return c.client.Status().Patch(ctx, obj, patch)
}
