package manifests

import (
	"crypto/sha256"
	"encoding/hex"
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
		ConfigData:    "save 900 1\n",
		TLSSecretName: "sample-tls",
	})

	if sts.Spec.UpdateStrategy.Type != "OnDelete" {
		t.Fatalf("expected redis statefulset update strategy OnDelete, got %q", sts.Spec.UpdateStrategy.Type)
	}
	expectedHash := sha256.Sum256([]byte("save 900 1\n"))
	if got := sts.Spec.Template.Annotations[redisConfigHashAnnotationKey]; got != hex.EncodeToString(expectedHash[:]) {
		t.Fatalf("unexpected redis config hash annotation: got %q", got)
	}

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
		ConfigData:    "sentinel monitor mymaster redis 6379 2\n",
		TLSSecretName: "sample-tls",
	})

	if sts.Spec.UpdateStrategy.Type != "OnDelete" {
		t.Fatalf("expected sentinel statefulset update strategy OnDelete, got %q", sts.Spec.UpdateStrategy.Type)
	}
	expectedHash := sha256.Sum256([]byte("sentinel monitor mymaster redis 6379 2\n"))
	if got := sts.Spec.Template.Annotations[sentinelConfigHashAnnotationKey]; got != hex.EncodeToString(expectedHash[:]) {
		t.Fatalf("unexpected sentinel config hash annotation: got %q", got)
	}

	if !hasSecretVolume(sts.Spec.Template.Spec.Volumes, "tls", "sample-tls") {
		t.Fatalf("expected sentinel tls secret volume to be mounted")
	}

	sentinelContainer := mustFindContainer(t, sts.Spec.Template.Spec.Containers, "sentinel")
	if !hasMount(sentinelContainer.VolumeMounts, "tls", "/etc/redis-tls") {
		t.Fatalf("expected sentinel tls mount")
	}
}

func TestNewSentinelStatefulSetIncludesAnnounceConfig(t *testing.T) {
	sts := NewSentinelStatefulSet(SentinelStatefulSetOptions{
		Name:          "sample-sentinel",
		Namespace:     "default",
		ServiceName:   "sample-sentinel",
		Labels:        map[string]string{"app": "sentinel"},
		Replicas:      3,
		MasterName:    "mymaster",
		ConfigMapName: "sample-sentinel-config",
		ConfigData:    "sentinel monitor mymaster redis 6379 2\n",
		ExternalEndpoints: []redisv1alpha1.ExternalNodeAddress{
			{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32079}},
			{Ordinal: 1, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32080}},
		},
	})

	sentinelContainer := mustFindContainer(t, sts.Spec.Template.Spec.Containers, "sentinel")
	if len(sentinelContainer.Args) == 0 {
		t.Fatalf("expected sentinel command args to be set")
	}
	command := sentinelContainer.Args[0]
	required := []string{
		`announce_ip=""`,
		`case "${ordinal}" in`,
		`announce_ip="10.0.0.10"`,
		`announce_port="32079"`,
		`echo "sentinel announce-ip ${announce_ip}" >> /tmp/sentinel.conf`,
		`echo "sentinel announce-port ${announce_port}" >> /tmp/sentinel.conf`,
	}
	for _, item := range required {
		if !strings.Contains(command, item) {
			t.Fatalf("expected sentinel command to contain %q", item)
		}
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
