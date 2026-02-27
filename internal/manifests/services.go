package manifests

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NewHeadlessService creates a headless Service.
func NewHeadlessService(name, namespace string, labels map[string]string, portName string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  labels,
			Ports: []corev1.ServicePort{{
				Name:       portName,
				Port:       port,
				TargetPort: intstr.FromInt(int(port)),
			}},
		},
	}
}

// NewService creates a normal ClusterIP service.
func NewService(name, namespace string, labels map[string]string, portName string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       portName,
				Port:       port,
				TargetPort: intstr.FromInt(int(port)),
			}},
		},
	}
}
