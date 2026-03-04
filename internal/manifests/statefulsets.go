package manifests

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

// RedisStatefulSetOptions contains parameters used to create redis StatefulSets.
type RedisStatefulSetOptions struct {
	Name          string
	Namespace     string
	ServiceName   string
	Labels        map[string]string
	Replicas      int32
	Policy        redisv1alpha1.PodPolicy
	Storage       redisv1alpha1.StorageSpec
	Command       string
	ConfigMapName string
	TLSSecretName string
}

// SentinelStatefulSetOptions contains parameters used to create sentinel StatefulSets.
type SentinelStatefulSetOptions struct {
	Name                string
	Namespace           string
	ServiceName         string
	Labels              map[string]string
	Replicas            int32
	Policy              redisv1alpha1.PodPolicy
	MasterName          string
	ConfigMapName       string
	RedisPasswordRef    *corev1.SecretKeySelector
	SentinelPasswordRef *corev1.SecretKeySelector
	Image               string
	ImagePullPolicy     corev1.PullPolicy
	TLSSecretName       string
	ExternalEndpoints   []redisv1alpha1.ExternalNodeAddress
}

// NewRedisStatefulSet builds a redis StatefulSet for either cluster or failover mode.
func NewRedisStatefulSet(redis *redisv1alpha1.Redis, opts RedisStatefulSetOptions) *appsv1.StatefulSet {
	replicas := opts.Replicas
	if replicas < 1 {
		replicas = 1
	}

	redisContainer := newRedisContainer(redis, opts.Command, opts.ServiceName, opts.TLSSecretName)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
			Labels:    opts.Labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: opts.ServiceName,
			Replicas:    int32Ptr(replicas),
			Selector:    &metav1.LabelSelector{MatchLabels: opts.Labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: opts.Labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{redisContainer},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: opts.ConfigMapName},
							}},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{newDataPVC(opts.Storage)},
		},
	}
	if opts.TLSSecretName != "" {
		sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: opts.TLSSecretName,
			}},
		})
	}
	if redis.Spec.Exporter.Enabled {
		sts.Spec.Template.Spec.Containers = append(sts.Spec.Template.Spec.Containers, newExporterContainer(redis, opts.ServiceName, opts.TLSSecretName))
	}
	applyPodPolicy(&sts.Spec.Template.Spec, &sts.Spec.Template.Spec.Containers[0], opts.Policy)
	return sts
}

// NewSentinelStatefulSet builds sentinel StatefulSet.
func NewSentinelStatefulSet(opts SentinelStatefulSetOptions) *appsv1.StatefulSet {
	replicas := opts.Replicas
	if replicas < 1 {
		replicas = 1
	}

	container := corev1.Container{
		Name:            "sentinel",
		Image:           imageOrDefault(opts.Image, DefaultRedisImage),
		ImagePullPolicy: pullPolicyOrDefault(opts.ImagePullPolicy),
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{renderSentinelCommand(opts.ExternalEndpoints)},
		Ports: []corev1.ContainerPort{{
			Name:          "sentinel",
			ContainerPort: 26379,
		}},
		Env: []corev1.EnvVar{{Name: "MASTER_NAME", Value: opts.MasterName}},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "config",
			MountPath: "/etc/redis-template",
			ReadOnly:  true,
		}},
	}
	if opts.TLSSecretName != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "tls",
			MountPath: "/etc/redis-tls",
			ReadOnly:  true,
		})
	}
	if opts.RedisPasswordRef != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:      "REDIS_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: opts.RedisPasswordRef},
		})
	}
	if opts.SentinelPasswordRef != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:      "SENTINEL_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: opts.SentinelPasswordRef},
		})
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
			Labels:    opts.Labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: opts.ServiceName,
			Replicas:    int32Ptr(replicas),
			Selector:    &metav1.LabelSelector{MatchLabels: opts.Labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: opts.Labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{container},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: opts.ConfigMapName},
						}},
					}},
				},
			},
		},
	}
	if opts.TLSSecretName != "" {
		sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: opts.TLSSecretName,
			}},
		})
	}
	applyPodPolicy(&sts.Spec.Template.Spec, &sts.Spec.Template.Spec.Containers[0], opts.Policy)
	return sts
}

func newRedisContainer(redis *redisv1alpha1.Redis, command, serviceName, tlsSecretName string) corev1.Container {
	container := corev1.Container{
		Name:            "redis",
		Image:           imageOrDefault(redis.Spec.Image, DefaultRedisImage),
		ImagePullPolicy: pullPolicyOrDefault(redis.Spec.ImagePullPolicy),
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{command},
		Ports: []corev1.ContainerPort{{
			Name:          "redis",
			ContainerPort: 6379,
		}},
		Env: []corev1.EnvVar{
			{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
			{Name: "HEADLESS_SERVICE", Value: serviceName},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "config", MountPath: "/etc/redis-template", ReadOnly: true},
			{Name: "data", MountPath: "/data"},
		},
	}
	if tlsSecretName != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "tls",
			MountPath: "/etc/redis-tls",
			ReadOnly:  true,
		})
	}
	if redis.Spec.Auth.RedisPasswordSecretRef != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:      "REDIS_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: redis.Spec.Auth.RedisPasswordSecretRef},
		})
	}
	return container
}

func newExporterContainer(redis *redisv1alpha1.Redis, serviceName, tlsSecretName string) corev1.Container {
	args := []string{"--redis.addr=redis://127.0.0.1:6379"}
	if tlsSecretName != "" {
		args = []string{
			"--redis.addr=rediss://$(POD_NAME).$(HEADLESS_SERVICE).$(POD_NAMESPACE).svc.cluster.local:6379",
			"--tls-ca-cert-file=/etc/redis-tls/ca.crt",
		}
	}
	container := corev1.Container{
		Name:            "redis-exporter",
		Image:           imageOrDefault(redis.Spec.Exporter.Image, DefaultExporterImage),
		ImagePullPolicy: pullPolicyOrDefault(redis.Spec.ImagePullPolicy),
		Args:            args,
		Ports: []corev1.ContainerPort{{
			Name:          "metrics",
			ContainerPort: 9121,
		}},
		Resources: redis.Spec.Exporter.Resources,
	}
	if redis.Spec.Auth.RedisPasswordSecretRef != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:      "REDIS_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: redis.Spec.Auth.RedisPasswordSecretRef},
		})
	}
	if tlsSecretName != "" {
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			corev1.EnvVar{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
			corev1.EnvVar{Name: "HEADLESS_SERVICE", Value: serviceName},
		)
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "tls",
			MountPath: "/etc/redis-tls",
			ReadOnly:  true,
		})
	}
	return container
}

func applyPodPolicy(spec *corev1.PodSpec, container *corev1.Container, policy redisv1alpha1.PodPolicy) {
	spec.NodeSelector = policy.NodeSelector
	spec.Affinity = policy.Affinity
	spec.Tolerations = policy.Tolerations
	spec.PriorityClassName = policy.PriorityClassName
	spec.TopologySpreadConstraints = policy.TopologySpreadConstraints
	container.Resources = policy.Resources
}

func newDataPVC(storage redisv1alpha1.StorageSpec) corev1.PersistentVolumeClaim {
	size := storage.Size
	if size == "" {
		size = defaultStorageSize
	}
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
			StorageClassName: storage.StorageClassName,
		},
	}
}

func int32Ptr(v int32) *int32 {
	return &v
}

func renderSentinelCommand(externalEndpoints []redisv1alpha1.ExternalNodeAddress) string {
	var builder strings.Builder
	builder.WriteString(`set -eu
cp /etc/redis-template/sentinel.conf /tmp/sentinel.conf
ordinal="${HOSTNAME##*-}"
announce_ip=""
announce_port=""
`)
	if len(externalEndpoints) > 0 {
		builder.WriteString("case \"${ordinal}\" in\n")
		for _, endpoint := range externalEndpoints {
			builder.WriteString(fmt.Sprintf("%d)\n", endpoint.Ordinal))
			builder.WriteString(fmt.Sprintf("  announce_ip=\"%s\"\n", endpoint.Host))
			builder.WriteString(fmt.Sprintf("  announce_port=\"%d\"\n", endpoint.Port))
			builder.WriteString("  ;;\n")
		}
		builder.WriteString("esac\n")
	}
	builder.WriteString(`if [ -n "${announce_ip}" ]; then
  echo "sentinel announce-ip ${announce_ip}" >> /tmp/sentinel.conf
fi
if [ -n "${announce_port}" ]; then
  echo "sentinel announce-port ${announce_port}" >> /tmp/sentinel.conf
fi
if [ -n "${SENTINEL_PASSWORD:-}" ]; then
  echo "requirepass ${SENTINEL_PASSWORD}" >> /tmp/sentinel.conf
fi
if [ -n "${REDIS_PASSWORD:-}" ]; then
  echo "sentinel auth-pass ${MASTER_NAME} ${REDIS_PASSWORD}" >> /tmp/sentinel.conf
fi
exec redis-server /tmp/sentinel.conf --sentinel`)
	return builder.String()
}
