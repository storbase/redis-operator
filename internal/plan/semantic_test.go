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

func TestValidateSemanticRejectsEmptyTLSSecretName(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		TLS: &redisv1alpha1.TLSSpec{},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected error for empty tls secret name")
	}
}

func TestValidateSemanticAcceptsFailoverExternalNodePort(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeFailover,
		Failover: &redisv1alpha1.FailoverSpec{
			RedisReplicas:    2,
			SentinelReplicas: 3,
			Quorum:           2,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Failover: &redisv1alpha1.FailoverExternalAccessSpec{
				Type: redisv1alpha1.ExternalAccessTypeNodePort,
				Sentinel: redisv1alpha1.FailoverExternalAccessNodeSet{
					Nodes: []redisv1alpha1.ExternalNodeAddress{
						{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32079}},
						{Ordinal: 1, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32080}},
						{Ordinal: 2, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32081}},
					},
				},
				Redis: redisv1alpha1.FailoverExternalAccessNodeSet{
					Nodes: []redisv1alpha1.ExternalNodeAddress{
						{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "redis-0.example.com", Port: 32100}},
						{Ordinal: 1, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "redis-1.example.com", Port: 32101}},
					},
				},
			},
		},
	}}
	if err := ValidateSemantic(obj); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSemanticRejectsExternalAccessOnClusterMode(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeCluster,
		Cluster: &redisv1alpha1.ClusterSpec{
			Shards: 1,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Failover: &redisv1alpha1.FailoverExternalAccessSpec{
				Type: redisv1alpha1.ExternalAccessTypeNodePort,
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected error for failover externalAccess in cluster mode")
	}
}

func TestValidateSemanticRejectsExternalAccessLengthMismatch(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeFailover,
		Failover: &redisv1alpha1.FailoverSpec{
			RedisReplicas:    3,
			SentinelReplicas: 3,
			Quorum:           2,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Failover: &redisv1alpha1.FailoverExternalAccessSpec{
				Type: redisv1alpha1.ExternalAccessTypeNodePort,
				Sentinel: redisv1alpha1.FailoverExternalAccessNodeSet{
					Nodes: []redisv1alpha1.ExternalNodeAddress{
						{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32079}},
						{Ordinal: 1, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32080}},
						{Ordinal: 2, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32081}},
					},
				},
				Redis: redisv1alpha1.FailoverExternalAccessNodeSet{
					Nodes: []redisv1alpha1.ExternalNodeAddress{
						{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "redis-0.example.com", Port: 32100}},
						{Ordinal: 1, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "redis-1.example.com", Port: 32101}},
					},
				},
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected length mismatch error")
	}
}

func TestValidateSemanticRejectsExternalAccessPortRange(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeFailover,
		Failover: &redisv1alpha1.FailoverSpec{
			RedisReplicas:    1,
			SentinelReplicas: 1,
			Quorum:           1,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Failover: &redisv1alpha1.FailoverExternalAccessSpec{
				Type: redisv1alpha1.ExternalAccessTypeNodePort,
				Sentinel: redisv1alpha1.FailoverExternalAccessNodeSet{
					Nodes: []redisv1alpha1.ExternalNodeAddress{
						{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 20000}},
					},
				},
				Redis: redisv1alpha1.FailoverExternalAccessNodeSet{
					Nodes: []redisv1alpha1.ExternalNodeAddress{
						{Ordinal: 0, ExternalAddress: redisv1alpha1.ExternalAddress{Host: "10.0.0.10", Port: 32100}},
					},
				},
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected nodePort range validation error")
	}
}
