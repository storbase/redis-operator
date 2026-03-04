package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
	"github.com/storbase/redis-operator/internal/kubeutil"
	"github.com/storbase/redis-operator/internal/manifests"
	clusterTopology "github.com/storbase/redis-operator/internal/topology/cluster"
)

const (
	clusterScalePrepareRequeueAfter    = 3 * time.Second
	clusterScaleRunningRequeueAfter    = 5 * time.Second
	clusterScaleTransitionRequeueAfter = 1 * time.Second
	clusterScaleRetryTokenAnnotation   = "redis.storbase.io/cluster-scale-retry-token"
)

type clusterScaleReconcileContext struct {
	now            metav1.Time
	scale          *redisv1alpha1.ClusterScaleStatus
	retryToken     string
	observation    appinterfaces.ClusterObservation
	observeErr     error
	targetShards   int32
	observedShards int32
	scalingNeeded  bool
}

func (r *RedisReconciler) reconcileClusterScaling(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
) (bool, ctrl.Result) {
	if redis.Spec.Mode != redisv1alpha1.RedisModeCluster || redis.Spec.Cluster == nil {
		return false, ctrl.Result{}
	}

	state := r.newClusterScaleReconcileContext(ctx, redis)
	if !state.scalingNeeded && state.scale.Phase == redisv1alpha1.ClusterScalePhaseIdle {
		return false, ctrl.Result{}
	}

	switch state.scale.Phase {
	case redisv1alpha1.ClusterScalePhaseIdle:
		return true, r.reconcileClusterScaleIdle(redis, state)

	case redisv1alpha1.ClusterScalePhasePending:
		return true, r.reconcileClusterScalePending(redis, state)

	case redisv1alpha1.ClusterScalePhasePreparing:
		return true, r.reconcileClusterScalePreparing(ctx, redis, state)

	case redisv1alpha1.ClusterScalePhaseRunning:
		return true, r.reconcileClusterScaleRunning(ctx, redis, state)

	case redisv1alpha1.ClusterScalePhaseFinalizing:
		return true, r.reconcileClusterScaleFinalizing(ctx, redis, state)

	case redisv1alpha1.ClusterScalePhaseFailed:
		return true, r.reconcileClusterScaleFailed(redis, state)
	}

	return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
}

func (r *RedisReconciler) newClusterScaleReconcileContext(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
) *clusterScaleReconcileContext {
	now := metav1.NewTime(time.Now())
	scale := &redis.Status.ClusterScale
	retryToken := clusterScaleRetryToken(redis)
	if scale.Phase == "" {
		scale.Phase = redisv1alpha1.ClusterScalePhaseIdle
	}

	observation, observeErr := clusterTopology.ObserveRuntime(ctx, r.RedisAdmin, redis.Namespace, redis.Name)
	if observeErr == nil && observation.ClusterSize > 0 {
		redis.Status.Cluster.ObservedShards = observation.ClusterSize
	}
	if redis.Status.Cluster.ObservedShards <= 0 {
		// Bootstrap compatibility: use desired shards before the first successful observation.
		redis.Status.Cluster.ObservedShards = redis.Spec.Cluster.Shards
	}

	targetShards := redis.Spec.Cluster.Shards
	observedShards := redis.Status.Cluster.ObservedShards
	if isInitialClusterBootstrap(redis, scale) {
		observedShards = targetShards
		redis.Status.Cluster.ObservedShards = targetShards
	}

	return &clusterScaleReconcileContext{
		now:            now,
		scale:          scale,
		retryToken:     retryToken,
		observation:    observation,
		observeErr:     observeErr,
		targetShards:   targetShards,
		observedShards: observedShards,
		scalingNeeded:  clusterTopology.NeedsScaling(observedShards, targetShards),
	}
}

func (r *RedisReconciler) reconcileClusterScaleIdle(
	redis *redisv1alpha1.Redis,
	state *clusterScaleReconcileContext,
) ctrl.Result {
	initializeClusterScaleStatus(state.scale, state.observedShards, state.targetShards, redis.Generation, state.retryToken, state.now)
	r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scaling started: %d -> %d", state.observedShards, state.targetShards))
	return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
}

func (r *RedisReconciler) reconcileClusterScalePending(
	redis *redisv1alpha1.Redis,
	state *clusterScaleReconcileContext,
) ctrl.Result {
	if !state.scalingNeeded {
		resetClusterScaleStatus(state.scale, state.now)
		return ctrl.Result{}
	}

	scale := state.scale
	scale.FromShards = state.observedShards
	scale.ToShards = state.targetShards
	scale.ObservedGeneration = redis.Generation
	scale.RetryToken = state.retryToken
	if err := clusterTopology.ValidateSingleStep(scale.FromShards, scale.ToShards); err != nil {
		r.markClusterScaleFailed(redis, err.Error(), state.now)
		return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
	}
	scale.Phase = redisv1alpha1.ClusterScalePhasePreparing
	r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scaling preparing: %d -> %d", scale.FromShards, scale.ToShards))
	return ctrl.Result{RequeueAfter: clusterScalePrepareRequeueAfter}
}

func (r *RedisReconciler) reconcileClusterScalePreparing(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	state *clusterScaleReconcileContext,
) ctrl.Result {
	if !state.scalingNeeded {
		resetClusterScaleStatus(state.scale, state.now)
		return ctrl.Result{}
	}

	ready, reason, err := r.clusterScalePrerequisitesReady(ctx, redis, state.scale)
	if err != nil {
		r.markClusterScaleFailed(redis, err.Error(), state.now)
		return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
	}
	if !ready {
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, reason)
		return ctrl.Result{RequeueAfter: clusterScalePrepareRequeueAfter}
	}

	jobName, err := r.ensureClusterScaleJob(ctx, redis, state.scale)
	if err != nil {
		r.markClusterScaleFailed(redis, err.Error(), state.now)
		return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
	}
	state.scale.JobName = jobName
	state.scale.Phase = redisv1alpha1.ClusterScalePhaseRunning
	r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scale job %s is running", jobName))
	return ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}
}

func (r *RedisReconciler) reconcileClusterScaleRunning(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	state *clusterScaleReconcileContext,
) ctrl.Result {
	done, err := r.observeClusterScaleJob(ctx, redis, state.scale)
	if err != nil {
		r.markClusterScaleFailed(redis, err.Error(), state.now)
		return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
	}
	if !done {
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scale job %s is running", state.scale.JobName))
		return ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}
	}

	state.scale.Phase = redisv1alpha1.ClusterScalePhaseFinalizing
	return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
}

func (r *RedisReconciler) reconcileClusterScaleFinalizing(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	state *clusterScaleReconcileContext,
) ctrl.Result {
	if state.observeErr != nil {
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("waiting cluster observation in finalizing: %v", state.observeErr))
		return ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}
	}
	if state.observation.State != "ok" ||
		state.observation.SlotsAssigned != 16384 ||
		state.observation.ClusterSize != state.scale.ToShards {
		r.setHealthUnhealthy(
			redis,
			redisv1alpha1.ReasonScaling,
			fmt.Sprintf(
				"waiting cluster convergence: state=%s slots=%d size=%d target=%d",
				state.observation.State,
				state.observation.SlotsAssigned,
				state.observation.ClusterSize,
				state.scale.ToShards,
			),
		)
		return ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}
	}

	if state.scale.ToShards < state.scale.FromShards {
		removedShard := state.scale.FromShards - 1
		if err := r.cleanupRemovedClusterShardResources(ctx, redis, removedShard); err != nil {
			r.markClusterScaleFailed(redis, err.Error(), state.now)
			return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
		}
	}

	redis.Status.Cluster.ObservedShards = state.scale.ToShards
	resetClusterScaleStatus(state.scale, state.now)
	r.setHealthHealthy(redis, fmt.Sprintf("Cluster scale completed: %d -> %d", state.scale.FromShards, state.scale.ToShards))
	return ctrl.Result{}
}

func (r *RedisReconciler) reconcileClusterScaleFailed(
	redis *redisv1alpha1.Redis,
	state *clusterScaleReconcileContext,
) ctrl.Result {
	scale := state.scale
	if scale.ObservedGeneration != redis.Generation || scale.ToShards != state.targetShards {
		scale.FromShards = state.observedShards
		scale.ToShards = state.targetShards
		scale.ObservedGeneration = redis.Generation
		scale.RetryToken = state.retryToken
		scale.JobName = ""
		scale.LastError = ""
		scale.StartedAt = &state.now
		scale.CompletedAt = nil
		scale.Phase = redisv1alpha1.ClusterScalePhasePending
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, "cluster scale target changed, restarting scale workflow")
		return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
	}

	if state.retryToken == scale.RetryToken {
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaleFailed, fmt.Sprintf("%s; retry by updating annotation %s", failMessageOrDefault(scale.LastError), clusterScaleRetryTokenAnnotation))
		return ctrl.Result{}
	}

	scale.RetryToken = state.retryToken
	scale.Phase = redisv1alpha1.ClusterScalePhasePending
	scale.JobName = ""
	scale.LastError = ""
	scale.StartedAt = &state.now
	scale.CompletedAt = nil
	r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("manual retry accepted via annotation %s", clusterScaleRetryTokenAnnotation))
	return ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}
}

func initializeClusterScaleStatus(
	scale *redisv1alpha1.ClusterScaleStatus,
	fromShards,
	toShards int32,
	generation int64,
	retryToken string,
	now metav1.Time,
) {
	scale.Phase = redisv1alpha1.ClusterScalePhasePending
	scale.FromShards = fromShards
	scale.ToShards = toShards
	scale.ObservedGeneration = generation
	scale.RetryToken = retryToken
	scale.JobName = ""
	scale.LastError = ""
	scale.StartedAt = &now
	scale.CompletedAt = nil
}

func resetClusterScaleStatus(scale *redisv1alpha1.ClusterScaleStatus, now metav1.Time) {
	from := scale.FromShards
	to := scale.ToShards
	scale.Phase = redisv1alpha1.ClusterScalePhaseIdle
	scale.RetryToken = ""
	scale.JobName = ""
	scale.LastError = ""
	scale.FromShards = from
	scale.ToShards = to
	scale.CompletedAt = &now
}

func (r *RedisReconciler) markClusterScaleFailed(redis *redisv1alpha1.Redis, message string, now metav1.Time) {
	scale := &redis.Status.ClusterScale
	scale.Phase = redisv1alpha1.ClusterScalePhaseFailed
	scale.LastError = message
	scale.CompletedAt = &now
	r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaleFailed, fmt.Sprintf("%s; retry by updating annotation %s", failMessageOrDefault(message), clusterScaleRetryTokenAnnotation))
}

func (r *RedisReconciler) clusterScalePrerequisitesReady(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	scale *redisv1alpha1.ClusterScaleStatus,
) (bool, string, error) {
	if scale.ToShards > scale.FromShards {
		stsName := fmt.Sprintf("%s-shard-%d", redis.Name, scale.ToShards-1)
		ready, err := r.statefulSetReady(ctx, redis.Namespace, stsName)
		if err != nil {
			return false, "", err
		}
		if !ready {
			return false, fmt.Sprintf("statefulset %s is not ready", stsName), nil
		}
		return true, "", nil
	}

	sourceShard := scale.FromShards - 1
	stsName := fmt.Sprintf("%s-shard-%d", redis.Name, sourceShard)
	ready, err := r.statefulSetReady(ctx, redis.Namespace, stsName)
	if err != nil {
		return false, "", err
	}
	if !ready {
		return false, fmt.Sprintf("statefulset %s is not ready", stsName), nil
	}
	return true, "", nil
}

func (r *RedisReconciler) ensureClusterScaleJob(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	scale *redisv1alpha1.ClusterScaleStatus,
) (string, error) {
	if redis.Spec.Cluster == nil {
		return "", fmt.Errorf("spec.cluster is nil")
	}

	jobName := clusterTopology.BuildScaleJobName(redis.Name, scale.ObservedGeneration, scale.FromShards, scale.ToShards, scale.RetryToken)
	job := manifests.NewClusterScaleJob(redis, manifests.ClusterScaleJobOptions{
		Name:       jobName,
		Namespace:  redis.Namespace,
		Labels:     manifests.BaseLabels(redis),
		FromShards: scale.FromShards,
		ToShards:   scale.ToShards,
	})
	if err := kubeutil.SetControllerOwner(redis, r.Scheme, job); err != nil {
		return "", err
	}
	if err := r.Client.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create scale job %s failed: %w", jobName, err)
	}
	return jobName, nil
}

func (r *RedisReconciler) observeClusterScaleJob(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	scale *redisv1alpha1.ClusterScaleStatus,
) (bool, error) {
	if scale.JobName == "" {
		return false, fmt.Errorf("cluster scale job name is empty")
	}
	job := &batchv1.Job{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: redis.Namespace, Name: scale.JobName}, job); err != nil {
		return false, fmt.Errorf("read scale job %s failed: %w", scale.JobName, err)
	}
	if job.Status.Failed > 0 {
		return false, fmt.Errorf("scale job %s failed", scale.JobName)
	}
	if job.Status.Succeeded > 0 {
		return true, nil
	}
	return false, nil
}

func (r *RedisReconciler) cleanupRemovedClusterShardResources(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
	shard int32,
) error {
	stsName := fmt.Sprintf("%s-shard-%d", redis.Name, shard)
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName, Namespace: redis.Namespace}}
	if err := r.Client.Delete(ctx, sts); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete statefulset %s failed: %w", stsName, err)
	}

	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: stsName, Namespace: redis.Namespace}}
	if err := r.Client.Delete(ctx, service); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete service %s failed: %w", stsName, err)
	}

	for ordinal := int32(0); ordinal <= redis.Spec.Cluster.ReplicasPerShard; ordinal++ {
		externalServiceName := fmt.Sprintf("%s-shard-%d-external-%d", redis.Name, shard, ordinal)
		externalService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: externalServiceName, Namespace: redis.Namespace}}
		if err := r.Client.Delete(ctx, externalService); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete external service %s failed: %w", externalServiceName, err)
		}

		pvcName := fmt.Sprintf("data-%s-%d", stsName, ordinal)
		pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: redis.Namespace}}
		if err := r.Client.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pvc %s failed: %w", pvcName, err)
		}
	}

	return nil
}

func clusterScaleRetryToken(redis *redisv1alpha1.Redis) string {
	if redis.Annotations == nil {
		return ""
	}
	return redis.Annotations[clusterScaleRetryTokenAnnotation]
}

func failMessageOrDefault(message string) string {
	if message == "" {
		return "cluster scale job failed"
	}
	return message
}

func isInitialClusterBootstrap(redis *redisv1alpha1.Redis, scale *redisv1alpha1.ClusterScaleStatus) bool {
	if redis == nil || redis.Spec.Cluster == nil || scale == nil {
		return false
	}
	if redis.Generation > 1 {
		return false
	}
	return scale.ObservedGeneration == 0 && scale.Phase == redisv1alpha1.ClusterScalePhaseIdle
}
