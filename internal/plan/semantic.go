package plan

import (
	"fmt"
	"strings"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
	cfg "github.com/storbase/redis-operator/internal/config"
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
