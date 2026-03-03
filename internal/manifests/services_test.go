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
