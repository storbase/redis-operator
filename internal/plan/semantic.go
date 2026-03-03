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
	if redis.Spec.ExternalAccess.Cluster != nil {
		return fmt.Errorf("spec.externalAccess.cluster is reserved and not implemented in this release")
	}
	failover := redis.Spec.ExternalAccess.Failover
	if failover == nil {
		return fmt.Errorf("spec.externalAccess.failover must be set when spec.externalAccess is configured")
	}
	if redis.Spec.Mode != redisv1alpha1.RedisModeFailover {
		return fmt.Errorf("spec.externalAccess.failover is only valid in Failover mode")
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
