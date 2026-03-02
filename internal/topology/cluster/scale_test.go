package cluster

import (
	"testing"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestValidateSingleStep(t *testing.T) {
	if err := ValidateSingleStep(3, 4); err != nil {
		t.Fatalf("single step scale-out should be valid: %v", err)
	}
	if err := ValidateSingleStep(4, 3); err != nil {
		t.Fatalf("single step scale-in should be valid: %v", err)
	}
	if err := ValidateSingleStep(3, 3); err == nil {
		t.Fatalf("same shard count should be rejected")
	}
	if err := ValidateSingleStep(3, 5); err == nil {
		t.Fatalf("more than one shard step should be rejected")
	}
}

func TestScaleDirection(t *testing.T) {
	if got := ScaleDirection(3, 4); got != "ScaleOut" {
		t.Fatalf("unexpected direction: %s", got)
	}
	if got := ScaleDirection(4, 3); got != "ScaleIn" {
		t.Fatalf("unexpected direction: %s", got)
	}
}

func TestBuildScaleJobNameStable(t *testing.T) {
	a := BuildScaleJobName("redis-cluster", 10, 3, 4, "token-a")
	b := BuildScaleJobName("redis-cluster", 10, 3, 4, "token-a")
	if a != b {
		t.Fatalf("job name should be stable: %q != %q", a, b)
	}
	if len(a) > 63 {
		t.Fatalf("job name exceeds max length: %d", len(a))
	}
}

func TestBuildScaleJobNameChangesWithRetryToken(t *testing.T) {
	a := BuildScaleJobName("redis-cluster", 10, 3, 4, "token-a")
	b := BuildScaleJobName("redis-cluster", 10, 3, 4, "token-b")
	if a == b {
		t.Fatalf("job name should change when retry token changes")
	}
}

func TestScalePhaseActive(t *testing.T) {
	active := []redisv1alpha1.ClusterScalePhase{
		redisv1alpha1.ClusterScalePhasePending,
		redisv1alpha1.ClusterScalePhasePreparing,
		redisv1alpha1.ClusterScalePhaseRunning,
		redisv1alpha1.ClusterScalePhaseFinalizing,
	}
	for _, phase := range active {
		if !ScalePhaseActive(phase) {
			t.Fatalf("phase should be active: %s", phase)
		}
	}
	if ScalePhaseActive(redisv1alpha1.ClusterScalePhaseIdle) {
		t.Fatalf("idle phase should be inactive")
	}
	if ScalePhaseActive(redisv1alpha1.ClusterScalePhaseFailed) {
		t.Fatalf("failed phase should be inactive")
	}
}
