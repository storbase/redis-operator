package controller

import (
	"testing"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestIsInitialClusterBootstrap(t *testing.T) {
	redis := &redisv1alpha1.Redis{
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           3,
				ReplicasPerShard: 1,
			},
		},
	}
	scale := &redisv1alpha1.ClusterScaleStatus{
		Phase:              redisv1alpha1.ClusterScalePhaseIdle,
		ObservedGeneration: 0,
	}

	redis.Generation = 1
	if !isInitialClusterBootstrap(redis, scale) {
		t.Fatalf("expected initial bootstrap for generation=1 idle status")
	}

	redis.Generation = 2
	if isInitialClusterBootstrap(redis, scale) {
		t.Fatalf("did not expect initial bootstrap for generation>1")
	}

	redis.Generation = 1
	scale.ObservedGeneration = 1
	if isInitialClusterBootstrap(redis, scale) {
		t.Fatalf("did not expect initial bootstrap when observed generation is set")
	}

	scale.ObservedGeneration = 0
	scale.Phase = redisv1alpha1.ClusterScalePhaseFailed
	if isInitialClusterBootstrap(redis, scale) {
		t.Fatalf("did not expect initial bootstrap when phase is not idle")
	}
}
