//go:build integration
// +build integration

package controller

import (
	"context"
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
	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
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
	bootstrap := meta.FindStatusCondition(fetched.Status.Conditions, redisv1alpha1.RedisConditionClusterBootstrapCompleted)
	if bootstrap == nil || bootstrap.Status != metav1.ConditionTrue {
		t.Fatalf("expected bootstrap condition to be true, got %#v", bootstrap)
	}
	if bootstrap.Reason != redisv1alpha1.RedisReasonClusterBootstrapSucceeded {
		t.Fatalf("unexpected bootstrap condition reason: got %q", bootstrap.Reason)
	}
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
	if _, err := r.Reconcile(testCtx, req); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
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
	wantEndpoint := fmt.Sprintf("%s-redis.default.svc:6379", name)
	if fetched.Status.Endpoint != wantEndpoint {
		t.Fatalf("unexpected endpoint: got %q want %q", fetched.Status.Endpoint, wantEndpoint)
	}
	if admin.clusterCalls != 0 {
		t.Fatalf("unexpected cluster bootstrap calls: got %d want 0", admin.clusterCalls)
	}
	if admin.failoverCalls != 2 {
		t.Fatalf("unexpected failover heal calls: got %d want 2", admin.failoverCalls)
	}
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
}

func (t *trackingAdminClient) HealCluster(_ context.Context, _, _ string) error {
	t.clusterCalls++
	return nil
}

func (t *trackingAdminClient) HealFailover(_ context.Context, _, _ string) error {
	t.failoverCalls++
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
