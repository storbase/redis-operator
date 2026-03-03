/*
Copyright 2026 storbase.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	kubeinfra "github.com/storbase/redis-operator/internal/infra/kube"
	redisinfra "github.com/storbase/redis-operator/internal/infra/redis"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
	"github.com/storbase/redis-operator/internal/kubeutil"
	"github.com/storbase/redis-operator/internal/plan"
	clusterTopology "github.com/storbase/redis-operator/internal/topology/cluster"
	failoverTopology "github.com/storbase/redis-operator/internal/topology/failover"
)

const runtimePrerequisitesRequeueAfter = 3 * time.Second

// RedisReconciler reconciles a Redis object.
type RedisReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme

	Kube       appinterfaces.KubernetesClient
	RedisAdmin appinterfaces.RedisAdminClient
	Recorder   events.EventRecorder
}

type statefulSetStatusSnapshot struct {
	Exists             bool
	Generation         int64
	ObservedGeneration int64
	DesiredReplicas    int32
	ReadyReplicas      int32
}

// +kubebuilder:rbac:groups=redis.storbase.io,resources=redis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redis.storbase.io,resources=redis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redis.storbase.io,resources=redis/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile moves observed cluster state toward desired Redis resource state.
func (r *RedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	redis := &redisv1alpha1.Redis{}
	if err := r.Kube.Get(ctx, req.NamespacedName, redis); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !redis.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	before := redis.DeepCopy()

	if err := plan.ValidateSemantic(redis); err != nil {
		r.emitWarning(redis, "InvalidSpec", err.Error())
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonInvalidSpec, err.Error())
		if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{}, err
	}

	desired, err := plan.BuildDesiredState(redis)
	if err != nil {
		r.emitWarning(redis, "BuildFailed", err.Error())
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonBuildFailed, err.Error())
		if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{}, err
	}

	for _, obj := range desired.Objects {
		if err := kubeutil.SetControllerOwner(redis, r.Scheme, obj); err != nil {
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonApplyFailed, err.Error())
			if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			return ctrl.Result{}, err
		}
		if err := r.Kube.Apply(ctx, obj); err != nil {
			applyErr := fmt.Errorf("apply %T/%s failed: %w", obj, client.ObjectKeyFromObject(obj), err)
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonApplyFailed, applyErr.Error())
			if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			return ctrl.Result{}, applyErr
		}
	}

	redis.Status.Endpoints = desired.Endpoints
	if err := r.setObservedReadyReplicaCounts(ctx, redis); err != nil {
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonReconciling, err.Error())
		if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{}, err
	}

	runtimeReady, waitReason, err := r.runtimePrerequisitesReady(ctx, redis)
	if err != nil {
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonReconciling, err.Error())
		if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{}, err
	}
	if !runtimeReady {
		r.setHealthUnhealthy(redis, redisv1alpha1.ReasonReconciling, waitReason)
		if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		log.Info("Runtime prerequisites are not ready, requeue", "reason", waitReason)
		return ctrl.Result{RequeueAfter: runtimePrerequisitesRequeueAfter}, nil
	}

	switch redis.Spec.Mode {
	case redisv1alpha1.RedisModeCluster:
		scalingHandled, scalingResult := r.reconcileClusterScaling(ctx, redis)
		if scalingHandled {
			if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			return scalingResult, nil
		}
		if err := clusterTopology.HealRuntime(ctx, r.RedisAdmin, redis.Namespace, redis.Name); err != nil {
			log.Error(err, "cluster bootstrap failed")
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonClusterCheckFailed, err.Error())
			if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			return ctrl.Result{}, err
		}
	case redisv1alpha1.RedisModeFailover:
		if err := failoverTopology.HealRuntime(ctx, r.RedisAdmin, redis.Namespace, redis.Name); err != nil {
			log.Error(err, "failover heal failed")
			r.setHealthUnhealthy(redis, redisv1alpha1.ReasonFailoverCheckFailed, err.Error())
			if patchErr := r.patchStatusIfChanged(ctx, before, redis); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			return ctrl.Result{}, err
		}
	}

	r.setHealthHealthy(redis, "Runtime health checks passed.")
	if err := r.patchStatusIfChanged(ctx, before, redis); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *RedisReconciler) runtimePrerequisitesReady(ctx context.Context, redis *redisv1alpha1.Redis) (bool, string, error) {
	switch redis.Spec.Mode {
	case redisv1alpha1.RedisModeCluster:
		if redis.Spec.Cluster == nil {
			return false, "spec.cluster is nil", nil
		}
		for shard := int32(0); shard < redis.Spec.Cluster.Shards; shard++ {
			stsName := fmt.Sprintf("%s-shard-%d", redis.Name, shard)
			ready, err := r.statefulSetReady(ctx, redis.Namespace, stsName)
			if err != nil {
				return false, "", err
			}
			if !ready {
				return false, fmt.Sprintf("statefulset %s is not ready", stsName), nil
			}
		}
	case redisv1alpha1.RedisModeFailover:
		for _, stsName := range []string{
			fmt.Sprintf("%s-redis", redis.Name),
			fmt.Sprintf("%s-sentinel", redis.Name),
		} {
			ready, err := r.statefulSetReady(ctx, redis.Namespace, stsName)
			if err != nil {
				return false, "", err
			}
			if !ready {
				return false, fmt.Sprintf("statefulset %s is not ready", stsName), nil
			}
		}
	}
	return true, "", nil
}

func (r *RedisReconciler) statefulSetReady(ctx context.Context, namespace, name string) (bool, error) {
	snapshot, err := r.readStatefulSetStatus(ctx, namespace, name)
	if err != nil {
		return false, err
	}
	if !snapshot.Exists {
		return false, nil
	}
	if snapshot.ObservedGeneration < snapshot.Generation {
		return false, nil
	}
	return snapshot.ReadyReplicas >= snapshot.DesiredReplicas, nil
}

func (r *RedisReconciler) readStatefulSetStatus(ctx context.Context, namespace, name string) (statefulSetStatusSnapshot, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, sts); err != nil {
		if errors.IsNotFound(err) {
			return statefulSetStatusSnapshot{}, nil
		}
		return statefulSetStatusSnapshot{}, err
	}

	replicas := int32(1)
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}
	return statefulSetStatusSnapshot{
		Exists:             true,
		Generation:         sts.Generation,
		ObservedGeneration: sts.Status.ObservedGeneration,
		DesiredReplicas:    replicas,
		ReadyReplicas:      sts.Status.ReadyReplicas,
	}, nil
}

func (r *RedisReconciler) setObservedReadyReplicaCounts(ctx context.Context, redis *redisv1alpha1.Redis) error {
	redisReady := int32(0)
	sentinelReady := int32(0)

	switch redis.Spec.Mode {
	case redisv1alpha1.RedisModeCluster:
		if redis.Spec.Cluster == nil {
			redis.Status.ObservedRedisReadyReplicas = 0
			redis.Status.ObservedSentinelReadyReplicas = 0
			return nil
		}
		for shard := int32(0); shard < redis.Spec.Cluster.Shards; shard++ {
			stsName := fmt.Sprintf("%s-shard-%d", redis.Name, shard)
			snapshot, err := r.readStatefulSetStatus(ctx, redis.Namespace, stsName)
			if err != nil {
				return err
			}
			redisReady += snapshot.ReadyReplicas
		}
	case redisv1alpha1.RedisModeFailover:
		redisSnapshot, err := r.readStatefulSetStatus(ctx, redis.Namespace, fmt.Sprintf("%s-redis", redis.Name))
		if err != nil {
			return err
		}
		sentinelSnapshot, err := r.readStatefulSetStatus(ctx, redis.Namespace, fmt.Sprintf("%s-sentinel", redis.Name))
		if err != nil {
			return err
		}
		redisReady = redisSnapshot.ReadyReplicas
		sentinelReady = sentinelSnapshot.ReadyReplicas
	}

	redis.Status.ObservedRedisReadyReplicas = redisReady
	redis.Status.ObservedSentinelReadyReplicas = sentinelReady
	return nil
}

func (r *RedisReconciler) patchStatusIfChanged(ctx context.Context, before, after *redisv1alpha1.Redis) error {
	if equality.Semantic.DeepEqual(before.Status, after.Status) {
		return nil
	}
	return r.Kube.PatchStatus(ctx, after, client.MergeFrom(before))
}

func (r *RedisReconciler) setHealthUnhealthy(redis *redisv1alpha1.Redis, reason redisv1alpha1.RedisHealthReason, message string) {
	redis.Status.Health = false
	redis.Status.Reason = reason
	meta.SetStatusCondition(&redis.Status.Conditions, metav1.Condition{
		Type:               redisv1alpha1.RedisConditionHealth,
		Status:             metav1.ConditionFalse,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: redis.Generation,
	})
}

func (r *RedisReconciler) setHealthHealthy(redis *redisv1alpha1.Redis, message string) {
	redis.Status.Health = true
	redis.Status.Reason = redisv1alpha1.ReasonHealthy
	meta.SetStatusCondition(&redis.Status.Conditions, metav1.Condition{
		Type:               redisv1alpha1.RedisConditionHealth,
		Status:             metav1.ConditionTrue,
		Reason:             string(redisv1alpha1.ReasonHealthy),
		Message:            message,
		ObservedGeneration: redis.Generation,
	})
}

func (r *RedisReconciler) emitWarning(redis *redisv1alpha1.Redis, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(redis, nil, "Warning", reason, "Reconcile", "%s", message)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	if r.Kube == nil {
		r.Kube = kubeinfra.NewClient(r.Client)
	}
	if r.RedisAdmin == nil {
		r.RedisAdmin = redisinfra.NewAdminClient(r.Client)
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("redis-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&redisv1alpha1.Redis{}).
		Named("redis").
		Complete(r)
}
