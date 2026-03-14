package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
	clusterTopology "github.com/storbase/redis-operator/internal/topology/cluster"
	failoverTopology "github.com/storbase/redis-operator/internal/topology/failover"
)

const (
	rollingUpdateRequeueAfter   = 3 * time.Second
	controllerRevisionHashLabel = "controller-revision-hash"
	unknownPodOrdinal           = int32(-1)
)

type rolloutComponent string

const (
	rolloutComponentRedis    rolloutComponent = "redis"
	rolloutComponentSentinel rolloutComponent = "sentinel"
)

type rolloutTarget struct {
	StatefulSet *appsv1.StatefulSet
	Pods        []corev1.Pod
	Component   rolloutComponent
	Shard       int32
}

type rolloutPod struct {
	Pod      corev1.Pod
	Ordinal  int32
	Revision string
}

func (r *RedisReconciler) reconcileRollingUpdate(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
) (bool, ctrl.Result, error) {
	targets, err := r.listRolloutTargets(ctx, redis)
	if err != nil {
		return false, ctrl.Result{}, err
	}

	for _, target := range targets {
		active, outdatedPods := analyzeRolloutState(target.StatefulSet, target.Pods)
		if !active {
			continue
		}
		if target.StatefulSet.Status.ObservedGeneration < target.StatefulSet.Generation {
			handled, result := r.waitRollingUpdate(
				redis,
				fmt.Sprintf("waiting statefulset %s to observe latest revision", target.StatefulSet.Name),
			)
			return handled, result, nil
		}
		if terminating := findTerminatingPod(target.Pods); terminating != nil {
			handled, result := r.waitRollingUpdate(
				redis,
				fmt.Sprintf("waiting pod %s to finish termination", terminating.Name),
			)
			return handled, result, nil
		}
		if !statefulSetReadyObject(target.StatefulSet) || !allPodsReady(target.Pods) {
			handled, result := r.waitRollingUpdate(
				redis,
				fmt.Sprintf("waiting statefulset %s replacement pod to become ready", target.StatefulSet.Name),
			)
			return handled, result, nil
		}
		if len(outdatedPods) == 0 {
			handled, result := r.waitRollingUpdate(
				redis,
				fmt.Sprintf("waiting statefulset %s current revision to converge", target.StatefulSet.Name),
			)
			return handled, result, nil
		}
		if err := r.ensureRollingUpdateRuntimeHealthy(ctx, redis); err != nil {
			r.emitWarning(redis, "RollingUpdateBlocked", err.Error())
			handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting runtime health before rolling update: %v", err))
			return handled, result, nil
		}

		switch redis.Spec.Mode {
		case redisv1alpha1.RedisModeFailover:
			return r.reconcileFailoverRolloutTarget(ctx, redis, target, outdatedPods)
		case redisv1alpha1.RedisModeCluster:
			return r.reconcileClusterRolloutTarget(ctx, redis, target, outdatedPods)
		}
	}

	return false, ctrl.Result{}, nil
}

func (r *RedisReconciler) listRolloutTargets(ctx context.Context, redis *redisv1alpha1.Redis) ([]rolloutTarget, error) {
	targets := make([]rolloutTarget, 0, 4)
	switch redis.Spec.Mode {
	case redisv1alpha1.RedisModeFailover:
		redisStatefulSet, err := r.getRolloutTarget(ctx, redis.Namespace, fmt.Sprintf("%s-redis", redis.Name), rolloutComponentRedis, 0)
		if err != nil {
			return nil, err
		}
		if redisStatefulSet != nil {
			targets = append(targets, *redisStatefulSet)
		}

		sentinelStatefulSet, err := r.getRolloutTarget(ctx, redis.Namespace, fmt.Sprintf("%s-sentinel", redis.Name), rolloutComponentSentinel, 0)
		if err != nil {
			return nil, err
		}
		if sentinelStatefulSet != nil {
			targets = append(targets, *sentinelStatefulSet)
		}
	case redisv1alpha1.RedisModeCluster:
		if redis.Spec.Cluster == nil {
			return targets, nil
		}
		for shard := int32(0); shard < redis.Spec.Cluster.Shards; shard++ {
			target, err := r.getRolloutTarget(
				ctx,
				redis.Namespace,
				fmt.Sprintf("%s-shard-%d", redis.Name, shard),
				rolloutComponentRedis,
				shard,
			)
			if err != nil {
				return nil, err
			}
			if target != nil {
				targets = append(targets, *target)
			}
		}
	}
	return targets, nil
}

func (r *RedisReconciler) getRolloutTarget(
	ctx context.Context,
	namespace,
	name string,
	component rolloutComponent,
	shard int32,
) (*rolloutTarget, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, sts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	pods, err := r.listStatefulSetPods(ctx, sts)
	if err != nil {
		return nil, err
	}
	return &rolloutTarget{
		StatefulSet: sts,
		Pods:        pods,
		Component:   component,
		Shard:       shard,
	}, nil
}

func (r *RedisReconciler) listStatefulSetPods(ctx context.Context, sts *appsv1.StatefulSet) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	listOptions := []client.ListOption{client.InNamespace(sts.Namespace)}
	if len(sts.Spec.Selector.MatchLabels) > 0 {
		listOptions = append(listOptions, client.MatchingLabels(sts.Spec.Selector.MatchLabels))
	}
	if err := r.Client.List(ctx, podList, listOptions...); err != nil {
		return nil, err
	}
	pods := append([]corev1.Pod(nil), podList.Items...)
	sort.Slice(pods, func(i, j int) bool {
		return podOrdinal(pods[i].Name) > podOrdinal(pods[j].Name)
	})
	return pods, nil
}

func (r *RedisReconciler) ensureRollingUpdateRuntimeHealthy(ctx context.Context, redis *redisv1alpha1.Redis) error {
	switch redis.Spec.Mode {
	case redisv1alpha1.RedisModeCluster:
		return clusterTopology.HealRuntime(ctx, r.RedisAdmin, redis.Namespace, redis.Name)
	case redisv1alpha1.RedisModeFailover:
		return failoverTopology.HealRuntime(ctx, r.RedisAdmin, redis.Namespace, redis.Name)
	default:
		return nil
	}
}

func (r *RedisReconciler) reconcileFailoverRolloutTarget(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	target rolloutTarget,
	outdatedPods []rolloutPod,
) (bool, ctrl.Result, error) {
	if target.Component == rolloutComponentSentinel {
		return r.deleteRolloutPod(ctx, redis, outdatedPods[0].Pod, fmt.Sprintf("restarting sentinel pod %s", outdatedPods[0].Pod.Name))
	}

	observation, err := r.RedisAdmin.ObserveFailover(ctx, redis.Namespace, redis.Name)
	if err != nil {
		r.emitWarning(redis, "RollingUpdateBlocked", err.Error())
		handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting failover role observation: %v", err))
		return handled, result, nil
	}

	replicaPods := make([]rolloutPod, 0, len(outdatedPods))
	var masterPod *rolloutPod
	for _, pod := range outdatedPods {
		if pod.Ordinal == observation.MasterOrdinal {
			current := pod
			masterPod = &current
			continue
		}
		replicaPods = append(replicaPods, pod)
	}
	sortRolloutPodsDescending(replicaPods)
	if len(replicaPods) > 0 {
		return r.deleteRolloutPod(ctx, redis, replicaPods[0].Pod, fmt.Sprintf("restarting failover replica pod %s", replicaPods[0].Pod.Name))
	}
	if masterPod == nil {
		return r.deleteRolloutPod(ctx, redis, outdatedPods[0].Pod, fmt.Sprintf("restarting failover pod %s", outdatedPods[0].Pod.Name))
	}
	if !observation.SentinelQuorumHealthy {
		handled, result := r.waitRollingUpdate(redis, "waiting healthy sentinel quorum before deleting current failover master")
		return handled, result, nil
	}
	if !hasHealthyFailoverReplica(observation, masterPod.Ordinal) {
		handled, result := r.waitRollingUpdate(redis, "waiting healthy failover replica before deleting current master")
		return handled, result, nil
	}
	if observation.MasterOrdinal != masterPod.Ordinal &&
		observation.ConsensusMasterOrdinal != unknownPodOrdinal &&
		observation.ConsensusMasterOrdinal != masterPod.Ordinal {
		return r.deleteRolloutPod(ctx, redis, masterPod.Pod, fmt.Sprintf("restarting former failover master pod %s", masterPod.Pod.Name))
	}
	if err := r.RedisAdmin.RequestFailover(ctx, redis.Namespace, redis.Name); err != nil {
		r.emitWarning(redis, "RollingUpdateBlocked", err.Error())
		handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting failover switchover before deleting %s: %v", masterPod.Pod.Name, err))
		return handled, result, nil
	}
	handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting failover switchover before deleting %s", masterPod.Pod.Name))
	return handled, result, nil
}

func (r *RedisReconciler) reconcileClusterRolloutTarget(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	target rolloutTarget,
	outdatedPods []rolloutPod,
) (bool, ctrl.Result, error) {
	shardObservations, err := r.RedisAdmin.ObserveClusterShards(ctx, redis.Namespace, redis.Name)
	if err != nil {
		r.emitWarning(redis, "RollingUpdateBlocked", err.Error())
		handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting cluster shard observation: %v", err))
		return handled, result, nil
	}
	shardObservation, ok := findClusterShardObservation(shardObservations, target.Shard)
	if !ok {
		handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting shard %d role observation", target.Shard))
		return handled, result, nil
	}

	replicaPods := make([]rolloutPod, 0, len(outdatedPods))
	var primaryPod *rolloutPod
	for _, pod := range outdatedPods {
		if pod.Ordinal == shardObservation.PrimaryOrdinal {
			current := pod
			primaryPod = &current
			continue
		}
		replicaPods = append(replicaPods, pod)
	}
	sortRolloutPodsDescending(replicaPods)
	if len(replicaPods) > 0 {
		return r.deleteRolloutPod(
			ctx,
			redis,
			replicaPods[0].Pod,
			fmt.Sprintf("restarting shard %d replica pod %s", target.Shard, replicaPods[0].Pod.Name),
		)
	}
	if primaryPod == nil {
		return r.deleteRolloutPod(
			ctx,
			redis,
			outdatedPods[0].Pod,
			fmt.Sprintf("restarting shard %d pod %s", target.Shard, outdatedPods[0].Pod.Name),
		)
	}
	if redis.Spec.Cluster != nil && redis.Spec.Cluster.ReplicasPerShard == 0 {
		return r.deleteRolloutPod(
			ctx,
			redis,
			primaryPod.Pod,
			fmt.Sprintf("restarting shard %d primary pod %s without switchover", target.Shard, primaryPod.Pod.Name),
		)
	}
	if shardObservation.PrimaryOrdinal != primaryPod.Ordinal {
		return r.deleteRolloutPod(
			ctx,
			redis,
			primaryPod.Pod,
			fmt.Sprintf("restarting former shard %d primary pod %s", target.Shard, primaryPod.Pod.Name),
		)
	}

	targetOrdinal, ok := pickClusterFailoverTarget(target.StatefulSet.Status.UpdateRevision, target.Pods, shardObservation)
	if !ok {
		handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting healthy shard %d replica before deleting current primary", target.Shard))
		return handled, result, nil
	}
	if err := r.RedisAdmin.RequestClusterFailover(ctx, redis.Namespace, redis.Name, target.Shard, targetOrdinal); err != nil {
		r.emitWarning(redis, "RollingUpdateBlocked", err.Error())
		handled, result := r.waitRollingUpdate(
			redis,
			fmt.Sprintf("waiting shard %d switchover before deleting %s: %v", target.Shard, primaryPod.Pod.Name, err),
		)
		return handled, result, nil
	}
	handled, result := r.waitRollingUpdate(
		redis,
		fmt.Sprintf("waiting shard %d switchover before deleting %s", target.Shard, primaryPod.Pod.Name),
	)
	return handled, result, nil
}

func (r *RedisReconciler) deleteRolloutPod(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	pod corev1.Pod,
	message string,
) (bool, ctrl.Result, error) {
	if err := r.Client.Delete(ctx, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			handled, result := r.waitRollingUpdate(redis, fmt.Sprintf("waiting pod %s recreation after deletion", pod.Name))
			return handled, result, nil
		}
		return false, ctrl.Result{}, err
	}
	r.setHealthUnhealthy(redis, redisv1alpha1.ReasonRollingUpdate, message)
	return true, ctrl.Result{RequeueAfter: rollingUpdateRequeueAfter}, nil
}

func (r *RedisReconciler) waitRollingUpdate(redis *redisv1alpha1.Redis, message string) (bool, ctrl.Result) {
	r.setHealthUnhealthy(redis, redisv1alpha1.ReasonRollingUpdate, message)
	return true, ctrl.Result{RequeueAfter: rollingUpdateRequeueAfter}
}

func analyzeRolloutState(sts *appsv1.StatefulSet, pods []corev1.Pod) (bool, []rolloutPod) {
	updateRevision := strings.TrimSpace(sts.Status.UpdateRevision)
	if updateRevision == "" {
		return false, nil
	}

	outdatedPods := make([]rolloutPod, 0, len(pods))
	for _, pod := range pods {
		revision := podRevision(pod)
		if revision == updateRevision {
			continue
		}
		outdatedPods = append(outdatedPods, rolloutPod{
			Pod:      pod,
			Ordinal:  podOrdinal(pod.Name),
			Revision: revision,
		})
	}

	active := len(outdatedPods) > 0
	if currentRevision := strings.TrimSpace(sts.Status.CurrentRevision); currentRevision != "" && currentRevision != updateRevision {
		active = true
	}
	sortRolloutPodsDescending(outdatedPods)
	return active, outdatedPods
}

func statefulSetReadyObject(sts *appsv1.StatefulSet) bool {
	replicas := int32(1)
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}
	return sts.Status.ObservedGeneration >= sts.Generation && sts.Status.ReadyReplicas >= replicas
}

func allPodsReady(pods []corev1.Pod) bool {
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || !podReady(pod) {
			return false
		}
	}
	return true
}

func podReady(pod corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func findTerminatingPod(pods []corev1.Pod) *corev1.Pod {
	for index := range pods {
		if pods[index].DeletionTimestamp != nil {
			return &pods[index]
		}
	}
	return nil
}

func podRevision(pod corev1.Pod) string {
	if pod.Labels == nil {
		return ""
	}
	return strings.TrimSpace(pod.Labels[controllerRevisionHashLabel])
}

func podOrdinal(name string) int32 {
	lastDash := strings.LastIndex(name, "-")
	if lastDash < 0 || lastDash == len(name)-1 {
		return unknownPodOrdinal
	}
	ordinal, err := strconv.ParseInt(name[lastDash+1:], 10, 32)
	if err != nil {
		return unknownPodOrdinal
	}
	return int32(ordinal)
}

func sortRolloutPodsDescending(pods []rolloutPod) {
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].Ordinal > pods[j].Ordinal
	})
}

func hasHealthyFailoverReplica(observation appinterfaces.FailoverObservation, masterOrdinal int32) bool {
	for _, node := range observation.Nodes {
		if node.Ordinal == masterOrdinal {
			continue
		}
		if node.Role == "replica" && node.MasterLinkStatus == "up" {
			return true
		}
	}
	return false
}

func findClusterShardObservation(
	observations []appinterfaces.ClusterShardObservation,
	shard int32,
) (appinterfaces.ClusterShardObservation, bool) {
	for _, observation := range observations {
		if observation.Shard == shard {
			return observation, true
		}
	}
	return appinterfaces.ClusterShardObservation{}, false
}

func pickClusterFailoverTarget(
	updateRevision string,
	pods []corev1.Pod,
	observation appinterfaces.ClusterShardObservation,
) (int32, bool) {
	updatedCandidates := make([]int32, 0, len(observation.Nodes))
	fallbackCandidates := make([]int32, 0, len(observation.Nodes))
	for _, node := range observation.Nodes {
		if node.Ordinal == observation.PrimaryOrdinal {
			continue
		}
		if node.Role != "replica" || node.MasterLinkStatus != "up" {
			continue
		}
		pod, ok := findPodByOrdinal(pods, node.Ordinal)
		if !ok || !podReady(pod) {
			continue
		}
		fallbackCandidates = append(fallbackCandidates, node.Ordinal)
		if podRevision(pod) == updateRevision {
			updatedCandidates = append(updatedCandidates, node.Ordinal)
		}
	}
	sort.Slice(updatedCandidates, func(i, j int) bool { return updatedCandidates[i] > updatedCandidates[j] })
	sort.Slice(fallbackCandidates, func(i, j int) bool { return fallbackCandidates[i] > fallbackCandidates[j] })
	if len(updatedCandidates) > 0 {
		return updatedCandidates[0], true
	}
	if len(fallbackCandidates) > 0 {
		return fallbackCandidates[0], true
	}
	return unknownPodOrdinal, false
}

func findPodByOrdinal(pods []corev1.Pod, ordinal int32) (corev1.Pod, bool) {
	for _, pod := range pods {
		if podOrdinal(pod.Name) == ordinal {
			return pod, true
		}
	}
	return corev1.Pod{}, false
}
