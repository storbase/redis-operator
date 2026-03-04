package cluster

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	"github.com/storbase/redis-operator/internal/manifests"
)

// NeedsScaling returns true when desired and observed shards are different.
func NeedsScaling(observed, desired int32) bool {
	return observed != desired
}

// ValidateSingleStep verifies shard change is exactly one step.
func ValidateSingleStep(fromShards, toShards int32) error {
	if int32(math.Abs(float64(toShards-fromShards))) > 1 {
		return fmt.Errorf("cluster shards can only change by one step: from=%d to=%d", fromShards, toShards)
	}
	if fromShards == toShards {
		return fmt.Errorf("scale step requires shard count change")
	}
	return nil
}

// ScaleDirection returns the textual scale direction.
func ScaleDirection(fromShards, toShards int32) string {
	if toShards > fromShards {
		return "ScaleOut"
	}
	return "ScaleIn"
}

// BuildScaleJobName returns a deterministic, DNS-safe job name.
func BuildScaleJobName(redisName string, generation int64, fromShards, toShards int32, retryToken string) string {
	raw := fmt.Sprintf("%s-%d-%d-%d-%s", redisName, generation, fromShards, toShards, retryToken)
	sum := sha1.Sum([]byte(raw))
	short := hex.EncodeToString(sum[:8])
	name := fmt.Sprintf("%s-cluster-scale-%s", redisName, short)
	return manifests.NormalizeClusterScaleJobName(name)
}

// ScalePhaseActive reports whether cluster scale is in progress.
func ScalePhaseActive(phase redisv1alpha1.ClusterScalePhase) bool {
	switch phase {
	case redisv1alpha1.ClusterScalePhasePending,
		redisv1alpha1.ClusterScalePhasePreparing,
		redisv1alpha1.ClusterScalePhaseRunning,
		redisv1alpha1.ClusterScalePhaseFinalizing:
		return true
	default:
		return false
	}
}
