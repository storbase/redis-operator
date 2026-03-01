//go:build integration
// +build integration

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	kubeinfra "github.com/storbase/redis-operator/internal/infra/kube"
	redisinfra "github.com/storbase/redis-operator/internal/infra/redis"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

func TestReconcileClusterCreatesShardStatefulSets(t *testing.T) {
	name := fmt.Sprintf("cluster-%d", time.Now().UnixNano())
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode:            redisv1alpha1.RedisModeCluster,
			Image:           "docker.io/library/redis:8.6.1",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           2,
				ReplicasPerShard: 1,
			},
			RedisConfig: []string{"save 900 1"},
		},
	}
	mustCreateRedis(t, obj)

	admin := &trackingAdminClient{}
	r := newTestReconcilerWithAdmin(admin)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
	firstResult, err := r.Reconcile(testCtx, req)
	if err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	if firstResult.RequeueAfter != runtimePrerequisitesRequeueAfter {
		t.Fatalf("unexpected first requeue delay: got %s want %s", firstResult.RequeueAfter, runtimePrerequisitesRequeueAfter)
	}
	firstObserved := mustGetRedis(t, obj)
	assertHealthStatus(t, firstObserved, false, redisv1alpha1.ReasonReconciling, metav1.ConditionFalse)
	assertObservedReadyCounts(t, firstObserved, 0, 0)
	for i := 0; i < 2; i++ {
		mustMarkStatefulSetReady(t, "default", fmt.Sprintf("%s-shard-%d", name, i))
	}
	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	for i := 0; i < 2; i++ {
		sts := &appsv1.StatefulSet{}
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: "default", Name: fmt.Sprintf("%s-shard-%d", name, i)}, sts); err != nil {
			t.Fatalf("statefulset shard %d not found: %v", i, err)
		}
		if got := *sts.Spec.Replicas; got != 2 {
			t.Fatalf("unexpected replicas for shard %d: %d", i, got)
		}
	}

	fetched := &redisv1alpha1.Redis{}
	if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(obj), fetched); err != nil {
		t.Fatalf("get redis failed: %v", err)
	}
	if fetched.Status.Endpoint == "" {
		t.Fatalf("expected status.endpoint to be set")
	}
	if admin.clusterCalls != 1 {
		t.Fatalf("unexpected cluster bootstrap calls: got %d want 1", admin.clusterCalls)
	}
	if admin.failoverCalls != 0 {
		t.Fatalf("unexpected failover heal calls: got %d want 0", admin.failoverCalls)
	}
	assertHealthStatus(t, fetched, true, redisv1alpha1.ReasonHealthy, metav1.ConditionTrue)
	assertObservedReadyCounts(t, fetched, 4, 0)
}

func TestReconcileFailoverCreatesTwoStatefulSets(t *testing.T) {
	name := fmt.Sprintf("failover-%d", time.Now().UnixNano())
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode:  redisv1alpha1.RedisModeFailover,
			Image: "docker.io/library/redis:8.6.1",
			Failover: &redisv1alpha1.FailoverSpec{
				RedisReplicas:    3,
				SentinelReplicas: 3,
				Quorum:           2,
				MasterName:       "mymaster",
			},
			SentinelConfig: []string{"sentinel down-after-milliseconds mymaster 30000"},
		},
	}
	mustCreateRedis(t, obj)

	admin := &trackingAdminClient{}
	r := newTestReconcilerWithAdmin(admin)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
	firstResult, err := r.Reconcile(testCtx, req)
	if err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	if firstResult.RequeueAfter != runtimePrerequisitesRequeueAfter {
		t.Fatalf("unexpected first requeue delay: got %s want %s", firstResult.RequeueAfter, runtimePrerequisitesRequeueAfter)
	}
	firstObserved := mustGetRedis(t, obj)
	assertHealthStatus(t, firstObserved, false, redisv1alpha1.ReasonReconciling, metav1.ConditionFalse)
	assertObservedReadyCounts(t, firstObserved, 0, 0)
	mustMarkStatefulSetReady(t, "default", fmt.Sprintf("%s-redis", name))
	mustMarkStatefulSetReady(t, "default", fmt.Sprintf("%s-sentinel", name))
	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	for _, namePart := range []string{"redis", "sentinel"} {
		sts := &appsv1.StatefulSet{}
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: "default", Name: fmt.Sprintf("%s-%s", name, namePart)}, sts); err != nil {
			t.Fatalf("statefulset %s not found: %v", namePart, err)
		}
	}

	fetched := &redisv1alpha1.Redis{}
	if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(obj), fetched); err != nil {
		t.Fatalf("get redis failed: %v", err)
	}
	wantEndpoint := fmt.Sprintf("%s-sentinel.default.svc:26379", name)
	if fetched.Status.Endpoint != wantEndpoint {
		t.Fatalf("unexpected endpoint: got %q want %q", fetched.Status.Endpoint, wantEndpoint)
	}
	if admin.clusterCalls != 0 {
		t.Fatalf("unexpected cluster bootstrap calls: got %d want 0", admin.clusterCalls)
	}
	if admin.failoverCalls != 1 {
		t.Fatalf("unexpected failover heal calls: got %d want 1", admin.failoverCalls)
	}
	assertHealthStatus(t, fetched, true, redisv1alpha1.ReasonHealthy, metav1.ConditionTrue)
	assertObservedReadyCounts(t, fetched, 3, 3)
}

func TestReconcileRejectsReservedDirective(t *testing.T) {
	name := fmt.Sprintf("invalid-%d", time.Now().UnixNano())
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards: 1,
			},
			RedisConfig: []string{"cluster-enabled yes"},
		},
	}
	mustCreateRedis(t, obj)

	r := newTestReconciler()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
	if _, err := r.Reconcile(testCtx, req); err == nil {
		t.Fatalf("expected reserved directive error")
	}
	fetched := mustGetRedis(t, obj)
	assertHealthStatus(t, fetched, false, redisv1alpha1.ReasonInvalidSpec, metav1.ConditionFalse)
}

func TestReconcileRejectsImmutableChanges(t *testing.T) {
	name := fmt.Sprintf("immutable-%d", time.Now().UnixNano())
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           1,
				ReplicasPerShard: 0,
			},
		},
	}
	mustCreateRedis(t, obj)

	r := newTestReconciler()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	fetched := &redisv1alpha1.Redis{}
	if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(obj), fetched); err != nil {
		t.Fatalf("get redis failed: %v", err)
	}
	fetched.Spec.Cluster.Shards = 2
	if err := k8sClient.Update(testCtx, fetched); err != nil {
		if !apierrors.IsInvalid(err) {
			t.Fatalf("expected invalid error on immutable update, got: %v", err)
		}
		return
	}
	t.Fatalf("expected immutable update to be rejected by API validation")
}

func TestReconcileSetsUnhealthyWhenClusterHealFails(t *testing.T) {
	name := fmt.Sprintf("cluster-heal-fail-%d", time.Now().UnixNano())
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode:  redisv1alpha1.RedisModeCluster,
			Image: "docker.io/library/redis:8.6.1",
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           1,
				ReplicasPerShard: 0,
			},
		},
	}
	mustCreateRedis(t, obj)

	admin := &trackingAdminClient{clusterErr: errors.New("cluster check failed")}
	r := newTestReconcilerWithAdmin(admin)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}

	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	mustMarkStatefulSetReady(t, "default", fmt.Sprintf("%s-shard-0", name))
	if _, err := r.Reconcile(testCtx, req); err == nil {
		t.Fatalf("expected cluster heal failure")
	}

	fetched := mustGetRedis(t, obj)
	assertHealthStatus(t, fetched, false, redisv1alpha1.ReasonClusterCheckFailed, metav1.ConditionFalse)
	if admin.clusterCalls != 1 {
		t.Fatalf("unexpected cluster heal calls: got %d want 1", admin.clusterCalls)
	}
}

func TestReconcileSetsUnhealthyWhenFailoverHealFails(t *testing.T) {
	name := fmt.Sprintf("failover-heal-fail-%d", time.Now().UnixNano())
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode:  redisv1alpha1.RedisModeFailover,
			Image: "docker.io/library/redis:8.6.1",
			Failover: &redisv1alpha1.FailoverSpec{
				RedisReplicas:    3,
				SentinelReplicas: 3,
				Quorum:           2,
				MasterName:       "mymaster",
			},
		},
	}
	mustCreateRedis(t, obj)

	admin := &trackingAdminClient{failoverErr: errors.New("failover check failed")}
	r := newTestReconcilerWithAdmin(admin)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}

	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	mustMarkStatefulSetReady(t, "default", fmt.Sprintf("%s-redis", name))
	mustMarkStatefulSetReady(t, "default", fmt.Sprintf("%s-sentinel", name))
	if _, err := r.Reconcile(testCtx, req); err == nil {
		t.Fatalf("expected failover heal failure")
	}

	fetched := mustGetRedis(t, obj)
	assertHealthStatus(t, fetched, false, redisv1alpha1.ReasonFailoverCheckFailed, metav1.ConditionFalse)
	if admin.failoverCalls != 1 {
		t.Fatalf("unexpected failover heal calls: got %d want 1", admin.failoverCalls)
	}
}

func TestReconcileSetsUnhealthyOnBuildFailure(t *testing.T) {
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: "build-fail", Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode:           redisv1alpha1.RedisModeCluster,
			SentinelConfig: []string{"sentinel down-after-milliseconds mymaster 5000"},
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           1,
				ReplicasPerShard: 0,
			},
		},
	}
	kube := &stubKubernetesClient{redis: obj.DeepCopy()}
	r := &RedisReconciler{
		Scheme:     testScheme,
		Kube:       kube,
		RedisAdmin: &trackingAdminClient{},
		Recorder:   events.NewFakeRecorder(100),
	}

	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
	if _, err := r.Reconcile(testCtx, req); err == nil {
		t.Fatalf("expected build failure")
	}
	assertHealthStatus(t, kube.redis, false, redisv1alpha1.ReasonBuildFailed, metav1.ConditionFalse)
}

func TestReconcileSetsUnhealthyOnApplyFailure(t *testing.T) {
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: "apply-fail", Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Mode:  redisv1alpha1.RedisModeCluster,
			Image: "docker.io/library/redis:8.6.1",
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           1,
				ReplicasPerShard: 0,
			},
		},
	}
	kube := &stubKubernetesClient{
		redis:    obj.DeepCopy(),
		applyErr: errors.New("apply failed"),
	}
	r := &RedisReconciler{
		Scheme:     testScheme,
		Kube:       kube,
		RedisAdmin: &trackingAdminClient{},
		Recorder:   events.NewFakeRecorder(100),
	}

	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
	if _, err := r.Reconcile(testCtx, req); err == nil {
		t.Fatalf("expected apply failure")
	}
	assertHealthStatus(t, kube.redis, false, redisv1alpha1.ReasonApplyFailed, metav1.ConditionFalse)
}

func newTestReconciler() *RedisReconciler {
	return newTestReconcilerWithAdmin(redisinfra.NewNoopAdminClient())
}

func newTestReconcilerWithAdmin(admin appinterfaces.RedisAdminClient) *RedisReconciler {
	return &RedisReconciler{
		Client:     k8sClient,
		Scheme:     testScheme,
		Kube:       kubeinfra.NewClient(k8sClient),
		RedisAdmin: admin,
		Recorder:   events.NewFakeRecorder(100),
	}
}

type trackingAdminClient struct {
	clusterCalls  int
	failoverCalls int
	clusterErr    error
	failoverErr   error
}

func (t *trackingAdminClient) HealCluster(_ context.Context, _, _ string) error {
	t.clusterCalls++
	return t.clusterErr
}

func (t *trackingAdminClient) HealFailover(_ context.Context, _, _ string) error {
	t.failoverCalls++
	return t.failoverErr
}

type stubKubernetesClient struct {
	redis          *redisv1alpha1.Redis
	applyErr       error
	patchStatusErr error
}

func (s *stubKubernetesClient) Get(_ context.Context, _ client.ObjectKey, obj client.Object) error {
	redisObj, ok := obj.(*redisv1alpha1.Redis)
	if !ok {
		return fmt.Errorf("unsupported object type %T", obj)
	}
	if s.redis == nil {
		return errors.New("redis object is not configured")
	}
	s.redis.DeepCopyInto(redisObj)
	return nil
}

func (s *stubKubernetesClient) Apply(_ context.Context, _ client.Object) error {
	return s.applyErr
}

func (s *stubKubernetesClient) Patch(_ context.Context, _ client.Object, _ client.Patch) error {
	return nil
}

func (s *stubKubernetesClient) PatchStatus(_ context.Context, obj client.Object, _ client.Patch) error {
	if s.patchStatusErr != nil {
		return s.patchStatusErr
	}
	redisObj, ok := obj.(*redisv1alpha1.Redis)
	if !ok {
		return fmt.Errorf("unsupported object type %T", obj)
	}
	s.redis.Status = redisObj.Status
	s.redis.Generation = redisObj.Generation
	return nil
}

func mustCreateRedis(t *testing.T, obj *redisv1alpha1.Redis) {
	t.Helper()
	if err := k8sClient.Create(context.Background(), obj); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return
		}
		t.Fatalf("create redis failed: %v", err)
	}
}

func mustMarkStatefulSetReady(t *testing.T, namespace, name string) {
	t.Helper()
	sts := &appsv1.StatefulSet{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := k8sClient.Get(testCtx, key, sts); err != nil {
		t.Fatalf("get statefulset %s failed: %v", name, err)
	}

	before := sts.DeepCopy()
	replicas := int32(1)
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}
	sts.Status.ObservedGeneration = sts.Generation
	sts.Status.Replicas = replicas
	sts.Status.ReadyReplicas = replicas
	sts.Status.AvailableReplicas = replicas
	sts.Status.CurrentReplicas = replicas

	if err := k8sClient.Status().Patch(testCtx, sts, client.MergeFrom(before)); err != nil {
		t.Fatalf("patch statefulset %s status failed: %v", name, err)
	}
}

func mustGetRedis(t *testing.T, obj *redisv1alpha1.Redis) *redisv1alpha1.Redis {
	t.Helper()
	fetched := &redisv1alpha1.Redis{}
	if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(obj), fetched); err != nil {
		t.Fatalf("get redis failed: %v", err)
	}
	return fetched
}

func assertHealthStatus(
	t *testing.T,
	redis *redisv1alpha1.Redis,
	wantHealth bool,
	wantReason redisv1alpha1.RedisHealthReason,
	wantConditionStatus metav1.ConditionStatus,
) {
	t.Helper()
	if redis.Status.Health != wantHealth {
		t.Fatalf("unexpected health: got %v want %v", redis.Status.Health, wantHealth)
	}
	if redis.Status.Reason != wantReason {
		t.Fatalf("unexpected reason: got %q want %q", redis.Status.Reason, wantReason)
	}
	condition := meta.FindStatusCondition(redis.Status.Conditions, redisv1alpha1.RedisConditionHealth)
	if condition == nil {
		t.Fatalf("missing health condition")
	}
	if condition.Status != wantConditionStatus {
		t.Fatalf("unexpected health condition status: got %q want %q", condition.Status, wantConditionStatus)
	}
	if condition.Reason != string(wantReason) {
		t.Fatalf("unexpected health condition reason: got %q want %q", condition.Reason, wantReason)
	}
}

func assertObservedReadyCounts(t *testing.T, redis *redisv1alpha1.Redis, wantRedis, wantSentinel int32) {
	t.Helper()
	if redis.Status.ObservedRedisReadyReplicas != wantRedis {
		t.Fatalf("unexpected observed redis ready replicas: got %d want %d", redis.Status.ObservedRedisReadyReplicas, wantRedis)
	}
	if redis.Status.ObservedSentinelReadyReplicas != wantSentinel {
		t.Fatalf("unexpected observed sentinel ready replicas: got %d want %d", redis.Status.ObservedSentinelReadyReplicas, wantSentinel)
	}
}
