package manifests

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestNewNodePortService(t *testing.T) {
	svc := NewNodePortService(
		"sample-redis-external-0",
		"default",
		map[string]string{"app": "redis"},
		map[string]string{
			"app":                                "redis",
			"statefulset.kubernetes.io/pod-name": "sample-redis-0",
		},
		"redis",
		6379,
		6379,
		32100,
	)

	if svc.Spec.Type != corev1.ServiceTypeNodePort {
		t.Fatalf("expected NodePort service type, got %q", svc.Spec.Type)
	}
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("expected one service port, got %d", len(svc.Spec.Ports))
	}
	if got := svc.Spec.Ports[0].NodePort; got != 32100 {
		t.Fatalf("unexpected nodePort: got %d want %d", got, 32100)
	}
	if svc.Spec.Selector["statefulset.kubernetes.io/pod-name"] != "sample-redis-0" {
		t.Fatalf("unexpected pod selector: %v", svc.Spec.Selector)
	}
}

func TestNewNodePortServiceWithPorts(t *testing.T) {
	svc := NewNodePortServiceWithPorts(
		"sample-cluster-external-0",
		"default",
		map[string]string{"app": "redis"},
		map[string]string{
			"app":                                "redis",
			"statefulset.kubernetes.io/pod-name": "sample-shard-0-0",
		},
		[]NodePortServicePort{
			{
				Name:       "redis",
				Port:       6379,
				TargetPort: 6379,
				NodePort:   32100,
			},
			{
				Name:       "cluster-bus",
				Port:       16379,
				TargetPort: 16379,
				NodePort:   32200,
			},
		},
	)
	if svc.Spec.Type != corev1.ServiceTypeNodePort {
		t.Fatalf("expected NodePort service type, got %q", svc.Spec.Type)
	}
	if len(svc.Spec.Ports) != 2 {
		t.Fatalf("expected two service ports, got %d", len(svc.Spec.Ports))
	}
	if got := svc.Spec.Ports[0].NodePort; got != 32100 {
		t.Fatalf("unexpected first nodePort: got %d want %d", got, 32100)
	}
	if got := svc.Spec.Ports[1].NodePort; got != 32200 {
		t.Fatalf("unexpected second nodePort: got %d want %d", got, 32200)
	}
	if svc.Spec.Ports[1].Port != 16379 {
		t.Fatalf("unexpected second service port: got %d want %d", svc.Spec.Ports[1].Port, 16379)
	}
}
