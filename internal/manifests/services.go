package manifests

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NodePortServicePort defines one NodePort service port mapping.
type NodePortServicePort struct {
	Name       string
	Port       int32
	TargetPort int32
	NodePort   int32
}

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

// NewNodePortService creates a NodePort service.
func NewNodePortService(
	name,
	namespace string,
	labels,
	selector map[string]string,
	portName string,
	port,
	targetPort,
	nodePort int32,
) *corev1.Service {
	return NewNodePortServiceWithPorts(
		name,
		namespace,
		labels,
		selector,
		[]NodePortServicePort{{
			Name:       portName,
			Port:       port,
			TargetPort: targetPort,
			NodePort:   nodePort,
		}},
	)
}

// NewNodePortServiceWithPorts creates a NodePort service with multiple ports.
func NewNodePortServiceWithPorts(
	name,
	namespace string,
	labels,
	selector map[string]string,
	ports []NodePortServicePort,
) *corev1.Service {
	servicePorts := make([]corev1.ServicePort, 0, len(ports))
	for _, port := range ports {
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:       port.Name,
			Port:       port.Port,
			TargetPort: intstr.FromInt(int(port.TargetPort)),
			NodePort:   port.NodePort,
		})
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: selector,
			Ports:    servicePorts,
		},
	}
}
