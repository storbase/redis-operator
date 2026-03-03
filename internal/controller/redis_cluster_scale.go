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

func (r *RedisReconciler) reconcileClusterScaling(
	ctx context.Context,
	redis *redisv1alpha1.Redis,
) (bool, ctrl.Result, error) {
	if redis.Spec.Mode != redisv1alpha1.RedisModeCluster || redis.Spec.Cluster == nil {
		return false, ctrl.Result{}, nil
	}

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
	scalingNeeded := clusterTopology.NeedsScaling(observedShards, targetShards)
	if !scalingNeeded && scale.Phase == redisv1alpha1.ClusterScalePhaseIdle {
		return false, ctrl.Result{}, nil
	}

	switch scale.Phase {
	case redisv1alpha1.ClusterScalePhaseIdle:
		initializeClusterScaleStatus(scale, observedShards, targetShards, redis.Generation, retryToken, now)
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scaling started: %d -> %d", observedShards, targetShards))
		return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil

	case redisv1alpha1.ClusterScalePhasePending:
		if !scalingNeeded {
			resetClusterScaleStatus(scale, now)
			return true, ctrl.Result{}, nil
		}
		scale.FromShards = observedShards
		scale.ToShards = targetShards
		scale.ObservedGeneration = redis.Generation
		scale.RetryToken = retryToken
		if err := clusterTopology.ValidateSingleStep(scale.FromShards, scale.ToShards); err != nil {
			r.markClusterScaleFailed(redis, err.Error(), now)
			return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
		}
		scale.Phase = redisv1alpha1.ClusterScalePhasePreparing
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scaling preparing: %d -> %d", scale.FromShards, scale.ToShards))
		return true, ctrl.Result{RequeueAfter: clusterScalePrepareRequeueAfter}, nil

	case redisv1alpha1.ClusterScalePhasePreparing:
		if !scalingNeeded {
			resetClusterScaleStatus(scale, now)
			return true, ctrl.Result{}, nil
		}
		ready, reason, err := r.clusterScalePrerequisitesReady(ctx, redis, scale)
		if err != nil {
			r.markClusterScaleFailed(redis, err.Error(), now)
			return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
		}
		if !ready {
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, reason)
			return true, ctrl.Result{RequeueAfter: clusterScalePrepareRequeueAfter}, nil
		}
		jobName, err := r.ensureClusterScaleJob(ctx, redis, scale)
		if err != nil {
			r.markClusterScaleFailed(redis, err.Error(), now)
			return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
		}
		scale.JobName = jobName
		scale.Phase = redisv1alpha1.ClusterScalePhaseRunning
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scale job %s is running", jobName))
		return true, ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}, nil

	case redisv1alpha1.ClusterScalePhaseRunning:
		done, err := r.observeClusterScaleJob(ctx, redis, scale)
		if err != nil {
			r.markClusterScaleFailed(redis, err.Error(), now)
			return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
		}
		if !done {
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("cluster scale job %s is running", scale.JobName))
			return true, ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}, nil
		}
		scale.Phase = redisv1alpha1.ClusterScalePhaseFinalizing
		return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil

	case redisv1alpha1.ClusterScalePhaseFinalizing:
		if observeErr != nil {
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("waiting cluster observation in finalizing: %v", observeErr))
			return true, ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}, nil
		}
		if observation.State != "ok" || observation.SlotsAssigned != 16384 || observation.ClusterSize != scale.ToShards {
			r.setHealthUnhealthy(
				redis,
				redisv1alpha1.ReasonScaling,
				fmt.Sprintf(
					"waiting cluster convergence: state=%s slots=%d size=%d target=%d",
					observation.State,
					observation.SlotsAssigned,
					observation.ClusterSize,
					scale.ToShards,
				),
			)
			return true, ctrl.Result{RequeueAfter: clusterScaleRunningRequeueAfter}, nil
		}
		if scale.ToShards < scale.FromShards {
			removedShard := scale.FromShards - 1
			if err := r.cleanupRemovedClusterShardResources(ctx, redis, removedShard); err != nil {
				r.markClusterScaleFailed(redis, err.Error(), now)
				return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
			}
		}
		redis.Status.Cluster.ObservedShards = scale.ToShards
		resetClusterScaleStatus(scale, now)
		r.setHealthHealthy(redis, fmt.Sprintf("Cluster scale completed: %d -> %d", scale.FromShards, scale.ToShards))
		return true, ctrl.Result{}, nil

	case redisv1alpha1.ClusterScalePhaseFailed:
		if scale.ObservedGeneration != redis.Generation || scale.ToShards != targetShards {
			scale.FromShards = observedShards
			scale.ToShards = targetShards
			scale.ObservedGeneration = redis.Generation
			scale.RetryToken = retryToken
			scale.JobName = ""
			scale.LastError = ""
			scale.StartedAt = &now
			scale.CompletedAt = nil
			scale.Phase = redisv1alpha1.ClusterScalePhasePending
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, "cluster scale target changed, restarting scale workflow")
			return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
		}
		if retryToken == scale.RetryToken {
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaleFailed, fmt.Sprintf("%s; retry by updating annotation %s", failMessageOrDefault(scale.LastError), clusterScaleRetryTokenAnnotation))
			return true, ctrl.Result{}, nil
		}
		scale.RetryToken = retryToken
		scale.Phase = redisv1alpha1.ClusterScalePhasePending
		scale.JobName = ""
		scale.LastError = ""
		scale.StartedAt = &now
		scale.CompletedAt = nil
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonScaling, fmt.Sprintf("manual retry accepted via annotation %s", clusterScaleRetryTokenAnnotation))
		return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
	}

	return true, ctrl.Result{RequeueAfter: clusterScaleTransitionRequeueAfter}, nil
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
