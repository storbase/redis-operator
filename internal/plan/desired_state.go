package plan

import (
	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	clusterTopology "github.com/storbase/redis-operator/internal/topology/cluster"
	failoverTopology "github.com/storbase/redis-operator/internal/topology/failover"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DesiredState contains all objects that should exist for a Redis resource.
type DesiredState struct {
	Objects  []client.Object
	Endpoint string
}

// BuildDesiredState renders mode-specific desired Kubernetes objects.
func BuildDesiredState(redis *redisv1alpha1.Redis) (DesiredState, error) {
	if redis.Spec.Mode == redisv1alpha1.RedisModeCluster {
		objects, endpoint, err := clusterTopology.BuildDesiredState(redis)
		if err != nil {
			return DesiredState{}, err
		}
		return DesiredState{Objects: objects, Endpoint: endpoint}, nil
	}
	objects, endpoint, err := failoverTopology.BuildDesiredState(redis)
	if err != nil {
		return DesiredState{}, err
	}
	return DesiredState{Objects: objects, Endpoint: endpoint}, nil
}
