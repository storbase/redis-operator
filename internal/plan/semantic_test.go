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

func TestValidateSemanticAcceptsClusterExternalNodePort(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeCluster,
		Cluster: &redisv1alpha1.ClusterSpec{
			Shards:           2,
			ReplicasPerShard: 1,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Cluster: &redisv1alpha1.ClusterExternalAccessSpec{
				Type:  redisv1alpha1.ExternalAccessTypeNodePort,
				Nodes: buildClusterExternalNodes(2, 1),
			},
		},
	}}
	if err := ValidateSemantic(obj); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSemanticRejectsClusterExternalLengthMismatch(t *testing.T) {
	nodes := buildClusterExternalNodes(2, 1)
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeCluster,
		Cluster: &redisv1alpha1.ClusterSpec{
			Shards:           2,
			ReplicasPerShard: 1,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Cluster: &redisv1alpha1.ClusterExternalAccessSpec{
				Type:  redisv1alpha1.ExternalAccessTypeNodePort,
				Nodes: nodes[:len(nodes)-1],
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected length mismatch error")
	}
}

func TestValidateSemanticRejectsClusterExternalDuplicateShardOrdinal(t *testing.T) {
	nodes := buildClusterExternalNodes(2, 1)
	nodes[3] = nodes[0]
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeCluster,
		Cluster: &redisv1alpha1.ClusterSpec{
			Shards:           2,
			ReplicasPerShard: 1,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Cluster: &redisv1alpha1.ClusterExternalAccessSpec{
				Type:  redisv1alpha1.ExternalAccessTypeNodePort,
				Nodes: nodes,
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected duplicate shard/ordinal error")
	}
}

func TestValidateSemanticRejectsClusterExternalPortConflicts(t *testing.T) {
	nodes := buildClusterExternalNodes(2, 1)
	nodes[1].Port = nodes[0].BusPort
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeCluster,
		Cluster: &redisv1alpha1.ClusterSpec{
			Shards:           2,
			ReplicasPerShard: 1,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Cluster: &redisv1alpha1.ClusterExternalAccessSpec{
				Type:  redisv1alpha1.ExternalAccessTypeNodePort,
				Nodes: nodes,
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected global port conflict error")
	}
}

func TestValidateSemanticRejectsClusterExternalPortEqualsBusPort(t *testing.T) {
	nodes := buildClusterExternalNodes(1, 1)
	nodes[0].BusPort = nodes[0].Port
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeCluster,
		Cluster: &redisv1alpha1.ClusterSpec{
			Shards:           1,
			ReplicasPerShard: 1,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Cluster: &redisv1alpha1.ClusterExternalAccessSpec{
				Type:  redisv1alpha1.ExternalAccessTypeNodePort,
				Nodes: nodes,
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected port/busPort mismatch validation error")
	}
}

func TestValidateSemanticRejectsMutuallyExclusiveExternalModes(t *testing.T) {
	obj := &redisv1alpha1.Redis{Spec: redisv1alpha1.RedisSpec{
		Mode: redisv1alpha1.RedisModeCluster,
		Cluster: &redisv1alpha1.ClusterSpec{
			Shards:           1,
			ReplicasPerShard: 0,
		},
		ExternalAccess: &redisv1alpha1.ExternalAccessSpec{
			Failover: &redisv1alpha1.FailoverExternalAccessSpec{
				Type: redisv1alpha1.ExternalAccessTypeNodePort,
			},
			Cluster: &redisv1alpha1.ClusterExternalAccessSpec{
				Type:  redisv1alpha1.ExternalAccessTypeNodePort,
				Nodes: buildClusterExternalNodes(1, 0),
			},
		},
	}}
	if err := ValidateSemantic(obj); err == nil {
		t.Fatalf("expected mutually exclusive external access error")
	}
}

func buildClusterExternalNodes(shards, replicasPerShard int32) []redisv1alpha1.ClusterExternalNodeAddress {
	result := make([]redisv1alpha1.ClusterExternalNodeAddress, 0, shards*(replicasPerShard+1))
	clientPort := int32(32000)
	busPort := int32(32500)
	for shard := int32(0); shard < shards; shard++ {
		for ordinal := int32(0); ordinal <= replicasPerShard; ordinal++ {
			result = append(result, redisv1alpha1.ClusterExternalNodeAddress{
				Shard:   shard,
				Ordinal: ordinal,
				ExternalAddress: redisv1alpha1.ExternalAddress{
					Host: "10.0.0.10",
					Port: clientPort,
				},
				BusPort: busPort,
			})
			clientPort++
			busPort++
		}
	}
	return result
}
