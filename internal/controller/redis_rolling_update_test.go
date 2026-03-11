package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

func TestReconcileRollingUpdateFailoverDeletesReplicaBeforeMaster(t *testing.T) {
	const namespace = "default"
	redis := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: namespace},
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeFailover,
			Failover: &redisv1alpha1.FailoverSpec{
				RedisReplicas:    3,
				SentinelReplicas: 3,
				Quorum:           2,
			},
		},
	}
	selector := map[string]string{"app": "redis", "component": "redis"}
	sts := newRolloutStatefulSet("sample-redis", namespace, selector, 3, "rev-old", "rev-new")
	pod0 := newReadyRolloutPod("sample-redis-0", selector, "rev-old")
	pod1 := newReadyRolloutPod("sample-redis-1", selector, "rev-old")
	pod2 := newReadyRolloutPod("sample-redis-2", selector, "rev-old")

	admin := &rollingAdminClient{
		failoverObservation: appinterfaces.FailoverObservation{
			MasterOrdinal:          0,
			ConsensusMasterOrdinal: 0,
			SentinelQuorumHealthy:  true,
			Nodes: []appinterfaces.ReplicationNodeObservation{
				{Ordinal: 0, Role: "master"},
				{Ordinal: 1, Role: "replica", MasterLinkStatus: "up"},
				{Ordinal: 2, Role: "replica", MasterLinkStatus: "up"},
			},
		},
	}
	r, kubeClient := newRollingTestReconciler(t, admin, sts, pod0, pod1, pod2)

	handled, result, err := r.reconcileRollingUpdate(context.Background(), redis)
	if err != nil {
		t.Fatalf("reconcileRollingUpdate failed: %v", err)
	}
	if !handled {
		t.Fatalf("expected rolling update to handle outdated pods")
	}
	if result.RequeueAfter != rollingUpdateRequeueAfter {
		t.Fatalf("unexpected requeue delay: got %s want %s", result.RequeueAfter, rollingUpdateRequeueAfter)
	}
	if admin.requestFailoverCalls != 0 {
		t.Fatalf("unexpected failover switchover call count: got %d want 0", admin.requestFailoverCalls)
	}

	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "sample-redis-2"}, &corev1.Pod{}); err == nil {
		t.Fatalf("expected highest-ordinal replica pod to be deleted first")
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "sample-redis-0"}, &corev1.Pod{}); err != nil {
		t.Fatalf("expected master pod to remain before replica rollout completes: %v", err)
	}
	if redis.Status.Reason != redisv1alpha1.ReasonRollingUpdate {
		t.Fatalf("unexpected redis health reason: got %q want %q", redis.Status.Reason, redisv1alpha1.ReasonRollingUpdate)
	}
}

func TestReconcileRollingUpdateFailoverSwitchesMasterBeforeDelete(t *testing.T) {
	const namespace = "default"
	redis := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: namespace},
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeFailover,
			Failover: &redisv1alpha1.FailoverSpec{
				RedisReplicas:    3,
				SentinelReplicas: 3,
				Quorum:           2,
			},
		},
	}
	selector := map[string]string{"app": "redis", "component": "redis"}
	sts := newRolloutStatefulSet("sample-redis", namespace, selector, 3, "rev-old", "rev-new")
	pod0 := newReadyRolloutPod("sample-redis-0", selector, "rev-old")
	pod1 := newReadyRolloutPod("sample-redis-1", selector, "rev-new")
	pod2 := newReadyRolloutPod("sample-redis-2", selector, "rev-new")

	admin := &rollingAdminClient{
		failoverObservation: appinterfaces.FailoverObservation{
			MasterOrdinal:          0,
			ConsensusMasterOrdinal: 0,
			SentinelQuorumHealthy:  true,
			Nodes: []appinterfaces.ReplicationNodeObservation{
				{Ordinal: 0, Role: "master"},
				{Ordinal: 1, Role: "replica", MasterLinkStatus: "up"},
				{Ordinal: 2, Role: "replica", MasterLinkStatus: "up"},
			},
		},
	}
	r, kubeClient := newRollingTestReconciler(t, admin, sts, pod0, pod1, pod2)

	handled, _, err := r.reconcileRollingUpdate(context.Background(), redis)
	if err != nil {
		t.Fatalf("reconcileRollingUpdate failed: %v", err)
	}
	if !handled {
		t.Fatalf("expected rolling update to handle master switchover")
	}
	if admin.requestFailoverCalls != 1 {
		t.Fatalf("unexpected failover switchover call count: got %d want 1", admin.requestFailoverCalls)
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "sample-redis-0"}, &corev1.Pod{}); err != nil {
		t.Fatalf("expected old master pod to stay until switchover completes: %v", err)
	}

	admin.failoverObservation = appinterfaces.FailoverObservation{
		MasterOrdinal:          1,
		ConsensusMasterOrdinal: 1,
		SentinelQuorumHealthy:  true,
		Nodes: []appinterfaces.ReplicationNodeObservation{
			{Ordinal: 0, Role: "replica", MasterLinkStatus: "up"},
			{Ordinal: 1, Role: "master"},
			{Ordinal: 2, Role: "replica", MasterLinkStatus: "up"},
		},
	}
	handled, _, err = r.reconcileRollingUpdate(context.Background(), redis)
	if err != nil {
		t.Fatalf("reconcileRollingUpdate after switchover failed: %v", err)
	}
	if !handled {
		t.Fatalf("expected rolling update to delete former master after switchover")
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "sample-redis-0"}, &corev1.Pod{}); err == nil {
		t.Fatalf("expected former master pod to be deleted after switchover converged")
	}
}

func TestReconcileRollingUpdateClusterDeletesReplicaBeforePrimaryAndPrefersUpdatedTarget(t *testing.T) {
	const namespace = "default"
	redis := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: namespace},
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           1,
				ReplicasPerShard: 2,
			},
		},
	}
	selector := map[string]string{"app": "redis", "component": "redis", "shard": "0"}
	sts := newRolloutStatefulSet("sample-shard-0", namespace, selector, 3, "rev-old", "rev-new")
	pod0 := newReadyRolloutPod("sample-shard-0-0", selector, "rev-old")
	pod1 := newReadyRolloutPod("sample-shard-0-1", selector, "rev-old")
	pod2 := newReadyRolloutPod("sample-shard-0-2", selector, "rev-old")

	admin := &rollingAdminClient{
		clusterShardObservations: []appinterfaces.ClusterShardObservation{
			{
				Shard:          0,
				PrimaryOrdinal: 0,
				Nodes: []appinterfaces.ReplicationNodeObservation{
					{Ordinal: 0, Role: "master"},
					{Ordinal: 1, Role: "replica", MasterLinkStatus: "up"},
					{Ordinal: 2, Role: "replica", MasterLinkStatus: "up"},
				},
			},
		},
	}
	r, kubeClient := newRollingTestReconciler(t, admin, sts, pod0, pod1, pod2)

	handled, _, err := r.reconcileRollingUpdate(context.Background(), redis)
	if err != nil {
		t.Fatalf("reconcileRollingUpdate failed: %v", err)
	}
	if !handled {
		t.Fatalf("expected rolling update to handle outdated cluster replica")
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "sample-shard-0-2"}, &corev1.Pod{}); err == nil {
		t.Fatalf("expected highest-ordinal cluster replica to be deleted first")
	}
	if admin.requestClusterFailoverCalls != 0 {
		t.Fatalf("unexpected cluster failover request count: got %d want 0", admin.requestClusterFailoverCalls)
	}

	pod1 = newReadyRolloutPod("sample-shard-0-1", selector, "rev-new")
	pod2 = newReadyRolloutPod("sample-shard-0-2", selector, "rev-new")
	r, kubeClient = newRollingTestReconciler(t, admin, sts, pod0, pod1, pod2)
	handled, _, err = r.reconcileRollingUpdate(context.Background(), redis)
	if err != nil {
		t.Fatalf("reconcileRollingUpdate for primary switchover failed: %v", err)
	}
	if !handled {
		t.Fatalf("expected rolling update to request primary switchover")
	}
	if admin.requestClusterFailoverCalls != 1 {
		t.Fatalf("unexpected cluster failover request count: got %d want 1", admin.requestClusterFailoverCalls)
	}
	if admin.lastClusterFailoverOrdinal != 2 {
		t.Fatalf("expected updated highest-ordinal replica to be selected, got %d", admin.lastClusterFailoverOrdinal)
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "sample-shard-0-0"}, &corev1.Pod{}); err != nil {
		t.Fatalf("expected old primary pod to remain before switchover completes: %v", err)
	}

	admin.clusterShardObservations = []appinterfaces.ClusterShardObservation{
		{
			Shard:          0,
			PrimaryOrdinal: 2,
			Nodes: []appinterfaces.ReplicationNodeObservation{
				{Ordinal: 0, Role: "replica", MasterLinkStatus: "up"},
				{Ordinal: 1, Role: "replica", MasterLinkStatus: "up"},
				{Ordinal: 2, Role: "master"},
			},
		},
	}
	handled, _, err = r.reconcileRollingUpdate(context.Background(), redis)
	if err != nil {
		t.Fatalf("reconcileRollingUpdate after cluster switchover failed: %v", err)
	}
	if !handled {
		t.Fatalf("expected rolling update to delete former primary after switchover")
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "sample-shard-0-0"}, &corev1.Pod{}); err == nil {
		t.Fatalf("expected former primary pod to be deleted after switchover converged")
	}
}

type rollingAdminClient struct {
	healClusterErr              error
	healFailoverErr             error
	failoverObservation         appinterfaces.FailoverObservation
	clusterShardObservations    []appinterfaces.ClusterShardObservation
	requestFailoverCalls        int
	requestClusterFailoverCalls int
	lastClusterFailoverShard    int32
	lastClusterFailoverOrdinal  int32
}

func (r *rollingAdminClient) HealCluster(_ context.Context, _, _ string) error {
	return r.healClusterErr
}

func (r *rollingAdminClient) ObserveCluster(_ context.Context, _, _ string) (appinterfaces.ClusterObservation, error) {
	return appinterfaces.ClusterObservation{}, nil
}

func (r *rollingAdminClient) ObserveClusterShards(_ context.Context, _, _ string) ([]appinterfaces.ClusterShardObservation, error) {
	return r.clusterShardObservations, nil
}

func (r *rollingAdminClient) RequestClusterFailover(_ context.Context, _, _ string, shard, ordinal int32) error {
	r.requestClusterFailoverCalls++
	r.lastClusterFailoverShard = shard
	r.lastClusterFailoverOrdinal = ordinal
	return nil
}

func (r *rollingAdminClient) HealFailover(_ context.Context, _, _ string) error {
	return r.healFailoverErr
}

func (r *rollingAdminClient) ObserveFailover(_ context.Context, _, _ string) (appinterfaces.FailoverObservation, error) {
	return r.failoverObservation, nil
}

func (r *rollingAdminClient) RequestFailover(_ context.Context, _, _ string) error {
	r.requestFailoverCalls++
	return nil
}

func newRollingTestReconciler(
	t *testing.T,
	admin appinterfaces.RedisAdminClient,
	objects ...client.Object,
) (*RedisReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme failed: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme failed: %v", err)
	}
	if err := redisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add redis scheme failed: %v", err)
	}

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &RedisReconciler{
		Client:     kubeClient,
		Scheme:     scheme,
		RedisAdmin: admin,
	}, kubeClient
}

func newRolloutStatefulSet(name, namespace string, selector map[string]string, replicas int32, currentRevision, updateRevision string) *appsv1.StatefulSet {
	selectorCopy := make(map[string]string, len(selector))
	for key, value := range selector {
		selectorCopy[key] = value
	}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selectorCopy},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1,
			ReadyReplicas:      replicas,
			CurrentReplicas:    replicas,
			CurrentRevision:    currentRevision,
			UpdateRevision:     updateRevision,
		},
	}
}

func newReadyRolloutPod(name string, labels map[string]string, revision string) *corev1.Pod {
	podLabels := make(map[string]string, len(labels)+1)
	for key, value := range labels {
		podLabels[key] = value
	}
	podLabels[controllerRevisionHashLabel] = revision
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    podLabels,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
}
