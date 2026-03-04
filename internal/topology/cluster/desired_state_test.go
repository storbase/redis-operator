package cluster

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestBuildDesiredStateWithClusterExternalNodePort(t *testing.T) {
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-cluster",
			Namespace: "default",
		},
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           2,
				ReplicasPerShard: 1,
			},
			ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
				Cluster: &redisv1alpha1.ClusterExternalAccessSpec{
					Type: redisv1alpha1.ExternalAccessTypeNodePort,
					Nodes: []redisv1alpha1.ClusterExternalNodeAddress{
						{
							Shard:   0,
							Ordinal: 0,
							ExternalAddress: redisv1alpha1.ExternalAddress{
								Host: "node-a.example.com",
								Port: 32100,
							},
							BusPort: 32200,
						},
						{
							Shard:   0,
							Ordinal: 1,
							ExternalAddress: redisv1alpha1.ExternalAddress{
								Host: "node-a.example.com",
								Port: 32101,
							},
							BusPort: 32201,
						},
						{
							Shard:   1,
							Ordinal: 0,
							ExternalAddress: redisv1alpha1.ExternalAddress{
								Host: "node-b.example.com",
								Port: 32110,
							},
							BusPort: 32210,
						},
						{
							Shard:   1,
							Ordinal: 1,
							ExternalAddress: redisv1alpha1.ExternalAddress{
								Host: "node-b.example.com",
								Port: 32111,
							},
							BusPort: 32211,
						},
					},
				},
			},
		},
	}

	objects, endpoints, err := BuildDesiredState(obj)
	if err != nil {
		t.Fatalf("BuildDesiredState returned error: %v", err)
	}

	serviceByName := map[string]*corev1.Service{}
	statefulSetByName := map[string]*appsv1.StatefulSet{}
	for _, current := range objects {
		switch typed := current.(type) {
		case *corev1.Service:
			serviceByName[typed.Name] = typed
		case *appsv1.StatefulSet:
			statefulSetByName[typed.Name] = typed
		}
	}

	for _, name := range []string{
		"redis-cluster-shard-0-external-0",
		"redis-cluster-shard-0-external-1",
		"redis-cluster-shard-1-external-0",
		"redis-cluster-shard-1-external-1",
	} {
		svc, ok := serviceByName[name]
		if !ok {
			t.Fatalf("expected external service %q to be rendered", name)
		}
		if svc.Spec.Type != corev1.ServiceTypeNodePort {
			t.Fatalf("expected service %q type NodePort, got %q", name, svc.Spec.Type)
		}
		if len(svc.Spec.Ports) != 2 {
			t.Fatalf("expected service %q to have 2 ports, got %d", name, len(svc.Spec.Ports))
		}
	}

	shard0Service := serviceByName["redis-cluster-shard-0-external-0"]
	if shard0Service.Spec.Ports[0].NodePort != 32100 {
		t.Fatalf("unexpected redis nodePort on shard-0 ordinal-0: got %d", shard0Service.Spec.Ports[0].NodePort)
	}
	if shard0Service.Spec.Ports[1].NodePort != 32200 {
		t.Fatalf("unexpected bus nodePort on shard-0 ordinal-0: got %d", shard0Service.Spec.Ports[1].NodePort)
	}

	shard0STS := statefulSetByName["redis-cluster-shard-0"]
	if shard0STS == nil {
		t.Fatalf("expected shard-0 statefulset to be rendered")
	}
	command := shard0STS.Spec.Template.Spec.Containers[0].Args[0]
	requiredFragments := []string{
		`announce_host="node-a.example.com"`,
		`announce_ip="node-a.example.com"`,
		`announce_port="32100"`,
		`announce_bus_port="32200"`,
		`echo "cluster-announce-ip ${announce_ip}" >> /tmp/redis.conf`,
		`echo "cluster-announce-bus-port ${announce_bus_port}" >> /tmp/redis.conf`,
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(command, fragment) {
			t.Fatalf("expected shard-0 command to contain %q", fragment)
		}
	}

	if len(endpoints.Internal) != 2 {
		t.Fatalf("expected 2 internal endpoints, got %d", len(endpoints.Internal))
	}
	if len(endpoints.External) != 2 {
		t.Fatalf("expected 2 external endpoints, got %d", len(endpoints.External))
	}
	if endpoints.External[0].Host != "node-a.example.com" || endpoints.External[0].Port != 32100 {
		t.Fatalf("unexpected first external endpoint: %+v", endpoints.External[0])
	}
	if endpoints.External[1].Host != "node-b.example.com" || endpoints.External[1].Port != 32110 {
		t.Fatalf("unexpected second external endpoint: %+v", endpoints.External[1])
	}
}

func TestBuildDesiredStateWithoutClusterExternalKeepsExternalStatusEmpty(t *testing.T) {
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-cluster",
			Namespace: "default",
		},
		Spec: redisv1alpha1.RedisSpec{
			Mode: redisv1alpha1.RedisModeCluster,
			Cluster: &redisv1alpha1.ClusterSpec{
				Shards:           1,
				ReplicasPerShard: 1,
			},
		},
	}

	objects, endpoints, err := BuildDesiredState(obj)
	if err != nil {
		t.Fatalf("BuildDesiredState returned error: %v", err)
	}
	if len(endpoints.External) != 0 {
		t.Fatalf("expected no external endpoints, got %d", len(endpoints.External))
	}
	for _, current := range objects {
		svc, ok := current.(*corev1.Service)
		if !ok {
			continue
		}
		if strings.Contains(svc.Name, "-external-") {
			t.Fatalf("did not expect external service %q when externalAccess.cluster is not configured", svc.Name)
		}
	}
}
