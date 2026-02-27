package redis

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	appinterfaces "github.com/storbase/redis-operator/internal/interfaces"
)

const (
	clusterPort       = 6379
	sentinelPort      = 26379
	totalClusterSlots = 16384
	defaultMasterName = "mymaster"
	clusterDomain     = "cluster.local"
)

// AdminClient executes short runtime checks and bootstrap actions for Redis.
type AdminClient struct {
	kube           client.Client
	commandTimeout time.Duration
}

// NewAdminClient returns the default runtime Redis admin client.
func NewAdminClient(kube client.Client) appinterfaces.RedisAdminClient {
	return &AdminClient{
		kube:           kube,
		commandTimeout: 5 * time.Second,
	}
}

func (c *AdminClient) HealCluster(ctx context.Context, namespace, name string) error {
	obj, err := c.loadRedis(ctx, namespace, name)
	if err != nil {
		return err
	}
	if obj.Spec.Mode != redisv1alpha1.RedisModeCluster || obj.Spec.Cluster == nil {
		return nil
	}

	password, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.RedisPasswordSecretRef)
	if err != nil {
		return err
	}
	return c.healCluster(ctx, obj, password)
}

func (c *AdminClient) HealFailover(ctx context.Context, namespace, name string) error {
	obj, err := c.loadRedis(ctx, namespace, name)
	if err != nil {
		return err
	}
	if obj.Spec.Mode != redisv1alpha1.RedisModeFailover || obj.Spec.Failover == nil {
		return nil
	}

	redisPassword, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.RedisPasswordSecretRef)
	if err != nil {
		return err
	}
	sentinelPassword, err := c.readSecretValue(ctx, namespace, obj.Spec.Auth.SentinelPasswordSecretRef)
	if err != nil {
		return err
	}
	return c.healFailover(ctx, obj, redisPassword, sentinelPassword)
}

func (c *AdminClient) loadRedis(ctx context.Context, namespace, name string) (*redisv1alpha1.Redis, error) {
	obj := &redisv1alpha1.Redis{}
	if err := c.kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (c *AdminClient) readSecretValue(ctx context.Context, namespace string, ref *corev1.SecretKeySelector) (string, error) {
	if ref == nil {
		return "", nil
	}
	if ref.Name == "" || ref.Key == "" {
		return "", fmt.Errorf("invalid secret reference: both name and key must be set")
	}

	secret := &corev1.Secret{}
	if err := c.kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, secret); err != nil {
		return "", fmt.Errorf("read secret %s/%s failed: %w", namespace, ref.Name, err)
	}
	value, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s key %q not found", namespace, ref.Name, ref.Key)
	}
	return string(value), nil
}

func (c *AdminClient) commandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.commandTimeout)
}

func parseInfoFields(raw string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		fields[parts[0]] = parts[1]
	}
	return fields
}

func parseIntField(fields map[string]string, key string) (int, error) {
	value, ok := fields[key]
	if !ok {
		return 0, fmt.Errorf("field %q not found", key)
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse field %q failed: %w", key, err)
	}
	return n, nil
}

func splitAddress(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", fmt.Errorf("split address %q failed: %w", addr, err)
	}
	return host, port, nil
}

func (c *AdminClient) newRedisClient(addr, password string) *redis.Client {
	return redis.NewClient(c.newRedisOptions(addr, password))
}

func (c *AdminClient) newSentinelClient(addr, password string) *redis.SentinelClient {
	return redis.NewSentinelClient(c.newRedisOptions(addr, password))
}

func (c *AdminClient) newRedisOptions(addr, password string) *redis.Options {
	options := &redis.Options{
		Addr:         addr,
		Password:     password,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
	return options
}

func pickPreferredHost(fallback string, ips []string) string {
	if len(ips) == 0 {
		return fallback
	}
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed != nil && parsed.To4() != nil {
			return ip
		}
	}
	return ips[0]
}

func (c *AdminClient) lookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}
