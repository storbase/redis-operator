package manifests

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestNewRedisStatefulSetWithTLSAndExporter(t *testing.T) {
	obj := &redisv1alpha1.Redis{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "default"},
		Spec: redisv1alpha1.RedisSpec{
			Exporter: redisv1alpha1.ExporterSpec{Enabled: true},
		},
	}
	sts := NewRedisStatefulSet(obj, RedisStatefulSetOptions{
		Name:          "sample-shard-0",
		Namespace:     "default",
		ServiceName:   "sample-shard-0",
		Labels:        map[string]string{"app": "redis"},
		Replicas:      2,
		Command:       "redis-server /tmp/redis.conf",
		ConfigMapName: "sample-config",
		TLSSecretName: "sample-tls",
	})

	if !hasSecretVolume(sts.Spec.Template.Spec.Volumes, "tls", "sample-tls") {
		t.Fatalf("expected tls secret volume to be mounted")
	}

	redisContainer := mustFindContainer(t, sts.Spec.Template.Spec.Containers, "redis")
	if !hasMount(redisContainer.VolumeMounts, "tls", "/etc/redis-tls") {
		t.Fatalf("expected redis container tls mount")
	}

	exporterContainer := mustFindContainer(t, sts.Spec.Template.Spec.Containers, "redis-exporter")
	expectedArgs := []string{
		"--redis.addr=rediss://$(POD_NAME).$(HEADLESS_SERVICE).$(POD_NAMESPACE).svc.cluster.local:6379",
		"--tls-ca-cert-file=/etc/redis-tls/ca.crt",
	}
	for _, item := range expectedArgs {
		if !containsString(exporterContainer.Args, item) {
			t.Fatalf("expected exporter arg %q", item)
		}
	}
	if !hasMount(exporterContainer.VolumeMounts, "tls", "/etc/redis-tls") {
		t.Fatalf("expected exporter tls mount")
	}
	if !hasEnvValue(exporterContainer.Env, "HEADLESS_SERVICE", "sample-shard-0") {
		t.Fatalf("expected exporter HEADLESS_SERVICE env")
	}
	if !hasEnvName(exporterContainer.Env, "POD_NAME") || !hasEnvName(exporterContainer.Env, "POD_NAMESPACE") {
		t.Fatalf("expected exporter pod metadata env")
	}
}

func TestNewSentinelStatefulSetWithTLS(t *testing.T) {
	sts := NewSentinelStatefulSet(SentinelStatefulSetOptions{
		Name:          "sample-sentinel",
		Namespace:     "default",
		ServiceName:   "sample-sentinel",
		Labels:        map[string]string{"app": "sentinel"},
		Replicas:      3,
		MasterName:    "mymaster",
		ConfigMapName: "sample-sentinel-config",
		TLSSecretName: "sample-tls",
	})

	if !hasSecretVolume(sts.Spec.Template.Spec.Volumes, "tls", "sample-tls") {
		t.Fatalf("expected sentinel tls secret volume to be mounted")
	}

	sentinelContainer := mustFindContainer(t, sts.Spec.Template.Spec.Containers, "sentinel")
	if !hasMount(sentinelContainer.VolumeMounts, "tls", "/etc/redis-tls") {
		t.Fatalf("expected sentinel tls mount")
	}
}

func mustFindContainer(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, container := range containers {
		if container.Name == name {
			return container
		}
	}
	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func hasSecretVolume(volumes []corev1.Volume, volumeName, secretName string) bool {
	for _, volume := range volumes {
		if volume.Name != volumeName || volume.Secret == nil {
			continue
		}
		return volume.Secret.SecretName == secretName
	}
	return false
}

func hasMount(mounts []corev1.VolumeMount, name, path string) bool {
	for _, mount := range mounts {
		if mount.Name == name && mount.MountPath == path {
			return true
		}
	}
	return false
}

func hasEnvName(envs []corev1.EnvVar, name string) bool {
	for _, env := range envs {
		if env.Name == name {
			return true
		}
	}
	return false
}

func hasEnvValue(envs []corev1.EnvVar, name, value string) bool {
	for _, env := range envs {
		if env.Name == name && strings.TrimSpace(env.Value) == value {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
