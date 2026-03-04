package plan

import (
	"fmt"
	"net"
	"strings"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	cfg "github.com/storbase/redis-operator/internal/config"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	minNodePort = 30000
	maxNodePort = 32767
)

// ValidateSemantic performs controller-side semantic validation.
func ValidateSemantic(redis *redisv1alpha1.Redis) error {
	if redis.Spec.TLS != nil && strings.TrimSpace(redis.Spec.TLS.SecretName) == "" {
		return fmt.Errorf("spec.tls.secretName must be set when spec.tls is configured")
	}

	userRedisConfig, err := cfg.ValidateUserDirectives(redis.Spec.RedisConfig, cfg.IsReservedRedisDirective)
	if err != nil {
		return fmt.Errorf("invalid redisConfig: %w", err)
	}
	if err := rejectInlineRedisCredentials(userRedisConfig); err != nil {
		return err
	}

	if redis.Spec.Mode == redisv1alpha1.RedisModeFailover {
		userSentinelConfig, err := cfg.ValidateUserDirectives(redis.Spec.SentinelConfig, cfg.IsReservedSentinelDirective)
		if err != nil {
			return fmt.Errorf("invalid sentinelConfig: %w", err)
		}
		if err := rejectInlineSentinelCredentials(userSentinelConfig); err != nil {
			return err
		}
	}

	if err := validateExternalAccess(redis); err != nil {
		return err
	}

	return nil
}

func rejectInlineRedisCredentials(lines []string) error {
	for _, line := range lines {
		tokens, err := cfg.ParseDirective(line)
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			continue
		}
		switch strings.ToLower(tokens[0]) {
		case "requirepass", "masterauth":
			return fmt.Errorf("redis credential directive %q is not allowed in redisConfig; use spec.auth.redisPasswordSecretRef", tokens[0])
		}
	}
	return nil
}

func rejectInlineSentinelCredentials(lines []string) error {
	for _, line := range lines {
		tokens, err := cfg.ParseDirective(line)
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			continue
		}
		if strings.ToLower(tokens[0]) == "requirepass" {
			return fmt.Errorf("sentinel credential directive %q is not allowed in sentinelConfig; use spec.auth.sentinelPasswordSecretRef", tokens[0])
		}
		if strings.ToLower(tokens[0]) == "sentinel" && len(tokens) > 1 && strings.ToLower(tokens[1]) == "auth-pass" {
			return fmt.Errorf("sentinel auth-pass is managed by operator; configure password via spec.auth.redisPasswordSecretRef")
		}
	}
	return nil
}

func validateExternalAccess(redis *redisv1alpha1.Redis) error {
	if redis.Spec.ExternalAccess == nil {
		return nil
	}

	if redis.Spec.ExternalAccess.Failover != nil && redis.Spec.ExternalAccess.Cluster != nil {
		return fmt.Errorf("spec.externalAccess.failover and spec.externalAccess.cluster are mutually exclusive")
	}

	switch redis.Spec.Mode {
	case redisv1alpha1.RedisModeFailover:
		return validateFailoverExternalAccess(redis)
	case redisv1alpha1.RedisModeCluster:
		return validateClusterExternalAccess(redis)
	default:
		return nil
	}
}

func validateFailoverExternalAccess(redis *redisv1alpha1.Redis) error {
	failover := redis.Spec.ExternalAccess.Failover
	if failover == nil {
		return fmt.Errorf("spec.externalAccess.failover must be set when spec.externalAccess is configured in failover mode")
	}
	if redis.Spec.Failover == nil {
		return fmt.Errorf("spec.failover must be set when spec.externalAccess.failover is configured")
	}
	if failover.Type != redisv1alpha1.ExternalAccessTypeNodePort {
		return fmt.Errorf("spec.externalAccess.failover.type %q is not implemented; only NodePort is supported", failover.Type)
	}
	if err := validateExternalNodeSet("sentinel", failover.Sentinel.Nodes, redis.Spec.Failover.SentinelReplicas); err != nil {
		return err
	}
	if err := validateExternalNodeSet("redis", failover.Redis.Nodes, redis.Spec.Failover.RedisReplicas); err != nil {
		return err
	}
	return nil
}

func validateClusterExternalAccess(redis *redisv1alpha1.Redis) error {
	clusterExternal := redis.Spec.ExternalAccess.Cluster
	if clusterExternal == nil {
		return fmt.Errorf("spec.externalAccess.cluster must be set when spec.externalAccess is configured in cluster mode")
	}
	if redis.Spec.Cluster == nil {
		return fmt.Errorf("spec.cluster must be set when spec.externalAccess.cluster is configured")
	}
	if clusterExternal.Type != redisv1alpha1.ExternalAccessTypeNodePort {
		return fmt.Errorf("spec.externalAccess.cluster.type %q is not implemented; only NodePort is supported", clusterExternal.Type)
	}

	shards := redis.Spec.Cluster.Shards
	replicasPerShard := redis.Spec.Cluster.ReplicasPerShard
	expectedNodes := int(shards * (replicasPerShard + 1))
	if len(clusterExternal.Nodes) != expectedNodes {
		return fmt.Errorf(
			"spec.externalAccess.cluster.nodes must have %d items, got %d",
			expectedNodes,
			len(clusterExternal.Nodes),
		)
	}

	seen := make(map[string]struct{}, len(clusterExternal.Nodes))
	usedPorts := make(map[int32]string, len(clusterExternal.Nodes)*2)

	for _, node := range clusterExternal.Nodes {
		if node.Shard < 0 || node.Shard >= shards {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes shard %d is out of range [0,%d)",
				node.Shard,
				shards,
			)
		}
		if node.Ordinal < 0 || node.Ordinal > replicasPerShard {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes shard %d ordinal %d is out of range [0,%d]",
				node.Shard,
				node.Ordinal,
				replicasPerShard,
			)
		}

		identity := fmt.Sprintf("%d/%d", node.Shard, node.Ordinal)
		if _, exists := seen[identity]; exists {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes has duplicate shard/ordinal pair %d/%d",
				node.Shard,
				node.Ordinal,
			)
		}
		seen[identity] = struct{}{}

		if err := validateExternalHost(node.Host); err != nil {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes shard %d ordinal %d host %q is invalid: %w",
				node.Shard,
				node.Ordinal,
				node.Host,
				err,
			)
		}
		if node.Port < minNodePort || node.Port > maxNodePort {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes shard %d ordinal %d port %d must be in [%d,%d]",
				node.Shard,
				node.Ordinal,
				node.Port,
				minNodePort,
				maxNodePort,
			)
		}
		if node.BusPort < minNodePort || node.BusPort > maxNodePort {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes shard %d ordinal %d busPort %d must be in [%d,%d]",
				node.Shard,
				node.Ordinal,
				node.BusPort,
				minNodePort,
				maxNodePort,
			)
		}
		if node.Port == node.BusPort {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes shard %d ordinal %d must use different values for port and busPort",
				node.Shard,
				node.Ordinal,
			)
		}

		label := fmt.Sprintf("shard %d ordinal %d", node.Shard, node.Ordinal)
		if prev, exists := usedPorts[node.Port]; exists {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes %s port %d conflicts with %s",
				label,
				node.Port,
				prev,
			)
		}
		usedPorts[node.Port] = fmt.Sprintf("%s port", label)

		if prev, exists := usedPorts[node.BusPort]; exists {
			return fmt.Errorf(
				"spec.externalAccess.cluster.nodes %s busPort %d conflicts with %s",
				label,
				node.BusPort,
				prev,
			)
		}
		usedPorts[node.BusPort] = fmt.Sprintf("%s busPort", label)
	}

	for shard := int32(0); shard < shards; shard++ {
		for ordinal := int32(0); ordinal <= replicasPerShard; ordinal++ {
			identity := fmt.Sprintf("%d/%d", shard, ordinal)
			if _, ok := seen[identity]; !ok {
				return fmt.Errorf("spec.externalAccess.cluster.nodes missing shard/ordinal pair %d/%d", shard, ordinal)
			}
		}
	}
	return nil
}

func validateExternalNodeSet(component string, nodes []redisv1alpha1.ExternalNodeAddress, expectedReplicas int32) error {
	if len(nodes) != int(expectedReplicas) {
		return fmt.Errorf(
			"spec.externalAccess.failover.%s.nodes must have %d items, got %d",
			component,
			expectedReplicas,
			len(nodes),
		)
	}
	seen := make(map[int32]struct{}, len(nodes))
	for _, node := range nodes {
		if node.Ordinal < 0 || node.Ordinal >= expectedReplicas {
			return fmt.Errorf(
				"spec.externalAccess.failover.%s.nodes ordinal %d is out of range [0,%d)",
				component,
				node.Ordinal,
				expectedReplicas,
			)
		}
		if _, exists := seen[node.Ordinal]; exists {
			return fmt.Errorf("spec.externalAccess.failover.%s.nodes has duplicate ordinal %d", component, node.Ordinal)
		}
		seen[node.Ordinal] = struct{}{}
		if err := validateExternalHost(node.Host); err != nil {
			return fmt.Errorf("spec.externalAccess.failover.%s.nodes ordinal %d host %q is invalid: %w", component, node.Ordinal, node.Host, err)
		}
		if node.Port < minNodePort || node.Port > maxNodePort {
			return fmt.Errorf(
				"spec.externalAccess.failover.%s.nodes ordinal %d port %d must be in [%d,%d]",
				component,
				node.Ordinal,
				node.Port,
				minNodePort,
				maxNodePort,
			)
		}
	}
	for ordinal := int32(0); ordinal < expectedReplicas; ordinal++ {
		if _, ok := seen[ordinal]; !ok {
			return fmt.Errorf("spec.externalAccess.failover.%s.nodes missing ordinal %d", component, ordinal)
		}
	}
	return nil
}

func validateExternalHost(host string) error {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return fmt.Errorf("host is empty")
	}
	if ip := net.ParseIP(strings.Trim(trimmed, "[]")); ip != nil {
		return nil
	}
	dnsName := strings.TrimSuffix(strings.ToLower(trimmed), ".")
	if errs := validation.IsDNS1123Subdomain(dnsName); len(errs) > 0 {
		return fmt.Errorf("host must be an IP or RFC1123 DNS name")
	}
	return nil
}
