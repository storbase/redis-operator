package plan

import (
	"testing"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestValidateSemanticAcceptsValidRedisDirectives(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{RedisConfig: []string{"save 900 1"}}}
	if err := ValidateSemantic(obj); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSemanticRejectsReservedRedisDirective(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{RedisConfig: []string{"cluster-enabled yes"}}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected error for reserved redis directive")
	}
}

func TestValidateSemanticRejectsInlineRedisCredential(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{RedisConfig: []string{"requirepass plain"}}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected error for inline redis credentials")
	}
}

func TestValidateSemanticRejectsInlineSentinelCredential(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode:           redisv1alpha1.RedisModeFailover,
		SentinelConfig: []string{"requirepass plain"},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected error for inline sentinel credentials")
	}
}
