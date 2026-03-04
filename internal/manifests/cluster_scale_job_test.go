package manifests

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestRenderClusterScaleJobCommandIncludesCoreActions(t *testing.T) {
	command := RenderClusterScaleJobCommand()
	required := []string{
		"run_cluster add-node",
		"run_cluster fix",
		"run_rebalance",
		"run_cluster reshard",
		"run_cluster del-node",
		"shard_master_id",
		"collect_shard_replica_ids",
		"CLUSTER MYID",
		"export REDISCLI_AUTH",
		"nodes don't agree about configuration",
		"clusterdown",
		"slots are open",
		"no such node id",
		"node_exists_in_cluster",
		"ensure_cluster_ready \"$TO_SHARDS\"",
		"cluster_state",
		"cluster_slots_assigned",
	}
	for _, item := range required {
		if !strings.Contains(command, item) {
			t.Fatalf("missing expected command fragment %q", item)
		}
	}
}

func TestNewClusterScaleJobIncludesTLSMountWhenEnabled(t *testing.T) {
	redis := &redisv1alpha1.Redis{
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			TLS:  &redisv1alpha1.TLSSpec{SecretName: "redis-tls"},
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           3,
				ReplicasPerShard: 1,
			},
		},
	}
	job := NewClusterScaleJob(redis, ClusterScaleJobOptions{
		Name:       "scale-job",
		Namespace:  "default",
		FromShards: 3,
		ToShards:   4,
	})
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected exactly one job container")
	}
	container := job.Spec.Template.Spec.Containers[0]
	foundMount := false
	for _, mount := range container.VolumeMounts {
		if mount.Name == "tls" && mount.MountPath == "/etc/redis-tls" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Fatalf("expected TLS volume mount")
	}
	foundVolume := false
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name == "tls" && volume.Secret != nil && volume.Secret.SecretName == "redis-tls" {
			foundVolume = true
		}
	}
	if !foundVolume {
		t.Fatalf("expected TLS secret volume")
	}
}

func TestNewClusterScaleJobUsesNeverRestart(t *testing.T) {
	redis := &redisv1alpha1.Redis{
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           3,
				ReplicasPerShard: 0,
			},
		},
	}
	job := NewClusterScaleJob(redis, ClusterScaleJobOptions{
		Name:       "scale-job",
		Namespace:  "default",
		FromShards: 3,
		ToShards:   2,
	})
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("unexpected restart policy: %s", job.Spec.Template.Spec.RestartPolicy)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Fatalf("unexpected backoff limit")
	}
	container := job.Spec.Template.Spec.Containers[0]
	if len(container.Command) != 2 || container.Command[0] != "/bin/bash" || container.Command[1] != "-c" {
		t.Fatalf("unexpected command: %v", container.Command)
	}
}
