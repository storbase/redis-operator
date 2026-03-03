package manifests

import (
	"fmt"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

const (
	clusterScaleJobNameMaxLength = 63
)

// ClusterScaleJobOptions defines Job creation parameters for cluster shard scaling.
type ClusterScaleJobOptions struct {
	Name       string
	Namespace  string
	Labels     map[string]string
	FromShards int32
	ToShards   int32
}

// NewClusterScaleJob builds a Job that runs redis-cli cluster manager operations.
func NewClusterScaleJob(redis *redisv1alpha1.Redis, opts ClusterScaleJobOptions) *batchv1.Job {
	labels := opts.Labels
	if labels == nil {
		labels = BaseLabels(redis)
	}
	labels = cloneStringMap(labels)
	labels["app.kubernetes.io/component"] = "cluster-scale"

	activeDeadlineSeconds := int64(1800)
	backoffLimit := int32(0)
	ttlSecondsAfterFinished := int32(3600)
	command := RenderClusterScaleJobCommand()
	env := []corev1.EnvVar{
		{Name: "REDIS_NAME", Value: redis.Name},
		{Name: "REDIS_NAMESPACE", Value: opts.Namespace},
		{Name: "FROM_SHARDS", Value: strconv.Itoa(int(opts.FromShards))},
		{Name: "TO_SHARDS", Value: strconv.Itoa(int(opts.ToShards))},
		{Name: "REPLICAS_PER_SHARD", Value: strconv.Itoa(int(redis.Spec.Cluster.ReplicasPerShard))},
		{Name: "CLUSTER_DOMAIN", Value: "cluster.local"},
	}
	if redis.Spec.Auth.RedisPasswordSecretRef != nil {
		env = append(env, corev1.EnvVar{
			Name:      "REDIS_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: redis.Spec.Auth.RedisPasswordSecretRef},
		})
	}

	container := corev1.Container{
		Name:            "cluster-scale",
		Image:           imageOrDefault(redis.Spec.Image, DefaultRedisImage),
		ImagePullPolicy: pullPolicyOrDefault(redis.Spec.ImagePullPolicy),
		Command:         []string{"/bin/bash", "-c"},
		Args:            []string{command},
		Env:             env,
	}

	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers:    []corev1.Container{container},
	}
	if redis.Spec.TLS != nil && redis.Spec.TLS.SecretName != "" {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: redis.Spec.TLS.SecretName,
			}},
		})
		podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "tls",
			MountPath: "/etc/redis-tls",
			ReadOnly:  true,
		})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadlineSeconds,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// RenderClusterScaleJobCommand renders the shell script used by cluster scale Job.
func RenderClusterScaleJobCommand() string {
	return `set -euo pipefail

coordinator_host="${REDIS_NAME}-shard-0-0.${REDIS_NAME}-shard-0.${REDIS_NAMESPACE}.svc.${CLUSTER_DOMAIN}"
coordinator_addr="${coordinator_host}:6379"
tls_args=()
if [ -f /etc/redis-tls/ca.crt ]; then
  tls_args=(--tls --cacert /etc/redis-tls/ca.crt)
fi
if [ -n "${REDIS_PASSWORD:-}" ]; then
  export REDISCLI_AUTH="${REDIS_PASSWORD}"
fi

run_cluster() {
  local output
  local rc
  local attempt=1
  local max_attempts=30
  while true; do
    set +e
    output="$(redis-cli "${tls_args[@]}" --cluster "$@" 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -eq 0 ]; then
      if echo "$output" | grep -Eiq "please fix your cluster problems before rebalancing|slots are open|migrating state|importing state"; then
        if [ "$attempt" -lt "$max_attempts" ]; then
          attempt=$((attempt + 1))
          sleep 2
          continue
        fi
        echo "$output" >&2
        return 1
      fi
      echo "$output"
      return 0
    fi
    if echo "$output" | grep -Eiq 'already|exists|not found|unknown node|no such node id|duplicate'; then
      echo "$output"
      return 0
    fi
    if echo "$output" | grep -Eiq "nodes don't agree about configuration|cluster state is not ok|clusterdown|the cluster is down|clustermanagermoveslot|please fix your cluster problems before rebalancing|slots are open|migrating state|importing state"; then
      if [ "$attempt" -lt "$max_attempts" ]; then
        attempt=$((attempt + 1))
        sleep 2
        continue
      fi
    fi
    echo "$output" >&2
    return 1
  done
}

run_rebalance() {
  local output
  local rc
  local attempt=1
  local max_attempts=30
  while true; do
    set +e
    output="$(redis-cli "${tls_args[@]}" --cluster rebalance "$@" 2>&1)"
    rc=$?
    set -e

    if [ "$rc" -eq 0 ] && ! echo "$output" | grep -Eiq "please fix your cluster problems before rebalancing|slots are open|migrating state|importing state|nodes don't agree about configuration|cluster state is not ok|clusterdown|the cluster is down"; then
      echo "$output"
      return 0
    fi

    if echo "$output" | grep -Eiq "please fix your cluster problems before rebalancing|slots are open|migrating state|importing state|nodes don't agree about configuration|cluster state is not ok|clusterdown|the cluster is down"; then
      if [ "$attempt" -lt "$max_attempts" ]; then
        run_cluster fix "$1" --cluster-yes || true
        attempt=$((attempt + 1))
        sleep 3
        continue
      fi
    fi

    echo "$output" >&2
    return 1
  done
}

run_cmd() {
  local host="$1"
  shift
  redis-cli "${tls_args[@]}" -h "$host" -p 6379 "$@"
}

cluster_nodes() {
  run_cmd "$coordinator_host" CLUSTER NODES
}

node_exists_in_cluster() {
  local node_id="$1"
  cluster_nodes | awk -v id="$node_id" '$1 == id {found = 1} END {exit found ? 0 : 1}'
}

shard_host() {
  local shard_idx="$1"
  local ordinal="$2"
  echo "${REDIS_NAME}-shard-${shard_idx}-${ordinal}.${REDIS_NAME}-shard-${shard_idx}.${REDIS_NAMESPACE}.svc.${CLUSTER_DOMAIN}"
}

node_id_by_host() {
  local host="$1"
  run_cmd "$host" CLUSTER MYID | awk 'NR == 1 {gsub(/\r/, "", $1); print $1; exit}'
}

node_flags_by_host() {
  local host="$1"
  run_cmd "$host" CLUSTER NODES | awk '$3 ~ /myself/ {gsub(/\r/, "", $3); print $3; exit}'
}

shard_master_id() {
  local shard_idx="$1"
  local ordinal host flags node_id
  for ordinal in $(seq 0 "$REPLICAS_PER_SHARD"); do
    host="$(shard_host "$shard_idx" "$ordinal")"
    flags="$(node_flags_by_host "$host" || true)"
    if [ -z "$flags" ]; then
      continue
    fi
    if echo "$flags" | grep -Eq 'master' && ! echo "$flags" | grep -Eq 'fail'; then
      node_id="$(node_id_by_host "$host" || true)"
      if [ -n "$node_id" ]; then
        echo "$node_id"
        return 0
      fi
    fi
  done
}

collect_shard_replica_ids() {
  local shard_idx="$1"
  local master_id="$2"
  local ordinal host node_id
  for ordinal in $(seq 0 "$REPLICAS_PER_SHARD"); do
    host="$(shard_host "$shard_idx" "$ordinal")"
    node_id="$(node_id_by_host "$host" || true)"
    if [ -n "$node_id" ] && [ "$node_id" != "$master_id" ]; then
      echo "$node_id"
    fi
  done
}

slot_count_by_node_id() {
  local node_id="$1"
  cluster_nodes | awk -v id="$node_id" '
    $1 == id {
      c = 0
      for (i = 9; i <= NF; i++) {
        if ($i ~ /^[0-9]+-[0-9]+$/) {
          split($i, a, "-")
          c += a[2] - a[1] + 1
        } else if ($i ~ /^[0-9]+$/) {
          c += 1
        }
      }
      print c + 0
      exit
    }
  '
}

collect_master_ids() {
  cluster_nodes | awk '$3 ~ /master/ && $3 !~ /fail/ {print $1}'
}

cluster_info_field() {
  local key="$1"
  run_cmd "$coordinator_host" CLUSTER INFO | awk -F: -v key="$key" '$1 == key {gsub(/\r/, "", $2); print $2}'
}

ensure_node_visible() {
  local node_id="$1"
  local deadline=$((SECONDS + 180))
  while true; do
    if cluster_nodes | awk -v id="$node_id" '$1 == id {found = 1} END {exit found ? 0 : 1}'; then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "node ${node_id} not visible from coordinator within timeout" >&2
      exit 1
    fi
    sleep 2
  done
}

ensure_cluster_ready() {
  local expected_size="$1"
  local deadline=$((SECONDS + 600))
  while true; do
    local state assigned size
    state="$(cluster_info_field cluster_state || true)"
    assigned="$(cluster_info_field cluster_slots_assigned || true)"
    size="$(cluster_info_field cluster_size || true)"
    if [ "$state" = "ok" ] && [ "$assigned" = "16384" ] && [ "$size" = "$expected_size" ]; then
      break
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "cluster check failed state=${state} assigned=${assigned} size=${size} expected_size=${expected_size}" >&2
      exit 1
    fi
    sleep 5
  done
}

if [ "$TO_SHARDS" -gt "$FROM_SHARDS" ]; then
  new_idx=$((TO_SHARDS - 1))
  new_master_host="${REDIS_NAME}-shard-${new_idx}-0.${REDIS_NAME}-shard-${new_idx}.${REDIS_NAMESPACE}.svc.${CLUSTER_DOMAIN}"
  new_master_addr="${new_master_host}:6379"
  run_cluster add-node "$new_master_addr" "$coordinator_addr" --cluster-yes

  new_master_id=""
  for _ in $(seq 1 30); do
    new_master_id="$(node_id_by_host "$new_master_host" || true)"
    if [ -n "$new_master_id" ]; then
      break
    fi
    sleep 2
  done
  if [ -z "$new_master_id" ]; then
    echo "failed to resolve new master node id for ${new_master_host}" >&2
    exit 1
  fi

  ensure_node_visible "$new_master_id"

  if [ "$REPLICAS_PER_SHARD" -gt 0 ]; then
    for ordinal in $(seq 1 "$REPLICAS_PER_SHARD"); do
      replica_host="${REDIS_NAME}-shard-${new_idx}-${ordinal}.${REDIS_NAME}-shard-${new_idx}.${REDIS_NAMESPACE}.svc.${CLUSTER_DOMAIN}"
      replica_addr="${replica_host}:6379"
      run_cluster add-node "$replica_addr" "$coordinator_addr" --cluster-slave --cluster-master-id "$new_master_id" --cluster-yes
    done
  fi

  run_cluster fix "$coordinator_addr" --cluster-yes
  run_rebalance "$coordinator_addr" --cluster-use-empty-masters --cluster-yes
fi

if [ "$TO_SHARDS" -lt "$FROM_SHARDS" ]; then
  remove_idx=$((FROM_SHARDS - 1))
  source_master_id="$(shard_master_id "$remove_idx" || true)"
  if [ -z "$source_master_id" ]; then
    echo "source shard master already removed: shard=${remove_idx}"
  else
    while true; do
      source_master_id="$(shard_master_id "$remove_idx" || true)"
      if [ -z "$source_master_id" ]; then
        break
      fi
      source_slots="$(slot_count_by_node_id "$source_master_id" || echo 0)"
      if [ "${source_slots:-0}" -eq 0 ]; then
        break
      fi

      mapfile -t targets < <(collect_master_ids | grep -v "^${source_master_id}$" || true)
      if [ "${#targets[@]}" -eq 0 ]; then
        echo "no target master available for reshard" >&2
        exit 1
      fi
      for target_id in "${targets[@]}"; do
        source_slots="$(slot_count_by_node_id "$source_master_id" || echo 0)"
        if [ "${source_slots:-0}" -eq 0 ]; then
          break
        fi
        move_slots=128
        if [ "$source_slots" -lt "$move_slots" ]; then
          move_slots="$source_slots"
        fi
        run_cluster reshard "$coordinator_addr" --cluster-from "$source_master_id" --cluster-to "$target_id" --cluster-slots "$move_slots" --cluster-yes
      done
    done

    mapfile -t replica_ids < <(collect_shard_replica_ids "$remove_idx" "$source_master_id" || true)
    for replica_id in "${replica_ids[@]}"; do
      if [ -n "$replica_id" ] && node_exists_in_cluster "$replica_id"; then
        run_cluster del-node "$coordinator_addr" "$replica_id" --cluster-yes
      fi
    done

    source_master_id="$(shard_master_id "$remove_idx" || true)"
    if [ -n "$source_master_id" ] && node_exists_in_cluster "$source_master_id"; then
      run_cluster del-node "$coordinator_addr" "$source_master_id" --cluster-yes
    fi
  fi

  run_rebalance "$coordinator_addr" --cluster-yes
fi

ensure_cluster_ready "$TO_SHARDS"
`
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// NormalizeClusterScaleJobName guarantees Kubernetes job name length limits.
func NormalizeClusterScaleJobName(name string) string {
	if len(name) <= clusterScaleJobNameMaxLength {
		return name
	}
	return fmt.Sprintf("%s-%s", name[:40], name[len(name)-22:])
}
