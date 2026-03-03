package plan

import (
	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	clusterTopology "github.com/storbase/redis-operator/internal/topology/cluster"
	failoverTopology "github.com/storbase/redis-operator/internal/topology/failover"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DesiredState contains all objects that should exist for a Redis resource.
type DesiredState struct {
	Objects   []client.Object
	Endpoints redisv1alpha1.EndpointStatus
}

// BuildDesiredState renders mode-specific desired Kubernetes objects.
func BuildDesiredState(redis *redisv1alpha1.Redis) (DesiredState, error) {
	if redis.Spec.Mode == redisv1alpha1.RedisModeCluster {
		objects, endpoints, err := clusterTopology.BuildDesiredState(redis)
		if err != nil {
			return DesiredState{}, err
		}
		return DesiredState{Objects: objects, Endpoints: endpoints}, nil
	}
	objects, endpoints, err := failoverTopology.BuildDesiredState(redis)
	if err != nil {
		return DesiredState{}, err
	}
	return DesiredState{Objects: objects, Endpoints: endpoints}, nil
}
