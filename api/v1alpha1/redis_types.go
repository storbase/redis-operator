/*
Copyright 2026 storbase.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RedisMode declares the topology mode of a Redis resource.
type RedisMode string

const (
	// RedisModeCluster deploys Redis Cluster topology.
	RedisModeCluster RedisMode = "cluster"
	// RedisModeFailover deploys Redis + Sentinel failover topology.
	RedisModeFailover RedisMode = "failover"
)

// ExternalAccessType declares how services are exposed out of cluster.
type ExternalAccessType string

const (
	// ExternalAccessTypeNodePort exposes per-pod services with NodePort.
	ExternalAccessTypeNodePort ExternalAccessType = "NodePort"
	// ExternalAccessTypeLoadBalancer is reserved for future implementation.
	ExternalAccessTypeLoadBalancer ExternalAccessType = "LoadBalancer"
)

const (
	// RedisConditionHealth reports whether the Redis resource is healthy.
	RedisConditionHealth = "Health"
)

// RedisHealthReason is the machine-readable reason for the current health status.
type RedisHealthReason string

const (
	ReasonReconciling         RedisHealthReason = "Reconciling"
	ReasonInvalidSpec         RedisHealthReason = "InvalidSpec"
	ReasonBuildFailed         RedisHealthReason = "BuildFailed"
	ReasonApplyFailed         RedisHealthReason = "ApplyFailed"
	ReasonClusterCheckFailed  RedisHealthReason = "ClusterCheckFailed"
	ReasonFailoverCheckFailed RedisHealthReason = "FailoverCheckFailed"
	ReasonScaling             RedisHealthReason = "Scaling"
	ReasonScaleFailed         RedisHealthReason = "ScaleFailed"
	ReasonHealthy             RedisHealthReason = "Healthy"
)

// ClusterScalePhase represents the current phase of cluster shard scaling.
type ClusterScalePhase string

const (
	ClusterScalePhaseIdle       ClusterScalePhase = "Idle"
	ClusterScalePhasePending    ClusterScalePhase = "Pending"
	ClusterScalePhasePreparing  ClusterScalePhase = "Preparing"
	ClusterScalePhaseRunning    ClusterScalePhase = "Running"
	ClusterScalePhaseFinalizing ClusterScalePhase = "Finalizing"
	ClusterScalePhaseFailed     ClusterScalePhase = "Failed"
)

// AuthSpec configures references to password secrets.
type AuthSpec struct {
	RedisPasswordSecretRef    *corev1.SecretKeySelector `json:"redisPasswordSecretRef,omitempty"`
	SentinelPasswordSecretRef *corev1.SecretKeySelector `json:"sentinelPasswordSecretRef,omitempty"`
}

// TLSSpec configures TLS with user-provided certificates.
type TLSSpec struct {
	// SecretName references a Secret in the same namespace containing tls.crt, tls.key, and ca.crt.
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
}

// StorageSpec configures data volume claims.
type StorageSpec struct {
	// Size is the requested PVC size.
	// +kubebuilder:validation:Pattern=`^[0-9]+(Ei|Pi|Ti|Gi|Mi|Ki)$`
	// +kubebuilder:default:="10Gi"
	Size string `json:"size,omitempty"`

	StorageClassName *string `json:"storageClassName,omitempty"`
}

// PodPolicy allows basic pod-level scheduling and resource tuning.
type PodPolicy struct {
	Resources                 corev1.ResourceRequirements       `json:"resources,omitempty"`
	NodeSelector              map[string]string                 `json:"nodeSelector,omitempty"`
	Affinity                  *corev1.Affinity                  `json:"affinity,omitempty"`
	Tolerations               []corev1.Toleration               `json:"tolerations,omitempty"`
	PriorityClassName         string                            `json:"priorityClassName,omitempty"`
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

// ClusterSpec defines shard-based Redis Cluster topology.
type ClusterSpec struct {
	// Shards is the number of hash-slot shards.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=128
	Shards int32 `json:"shards"`

	// ReplicasPerShard is the number of replica pods per shard, excluding the primary (master) pod.
	// Each shard runs one primary pod plus ReplicasPerShard replica pods.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=5
	// +kubebuilder:default:=1
	ReplicasPerShard int32 `json:"replicasPerShard,omitempty"`

	Storage  StorageSpec `json:"storage,omitempty"`
	RedisPod PodPolicy   `json:"redisPod,omitempty"`
}

// ClusterRuntimeStatus stores observed cluster topology information.
type ClusterRuntimeStatus struct {
	// ObservedShards is the latest observed number of cluster masters.
	ObservedShards int32 `json:"observedShards,omitempty"`
}

// ClusterScaleStatus stores shard scaling operation runtime state.
type ClusterScaleStatus struct {
	Phase              ClusterScalePhase `json:"phase,omitempty"`
	FromShards         int32             `json:"fromShards,omitempty"`
	ToShards           int32             `json:"toShards,omitempty"`
	ObservedGeneration int64             `json:"observedGeneration,omitempty"`
	// RetryToken stores the last observed manual retry token value.
	RetryToken  string       `json:"retryToken,omitempty"`
	JobName     string       `json:"jobName,omitempty"`
	LastError   string       `json:"lastError,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// FailoverSpec defines Redis + Sentinel failover topology.
type FailoverSpec struct {
	// RedisReplicas is the number of redis pods managed by one StatefulSet.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=9
	RedisReplicas int32 `json:"redisReplicas"`

	// SentinelReplicas is the number of sentinel pods.
	// +kubebuilder:validation:Minimum=3
	// +kubebuilder:validation:Maximum=9
	SentinelReplicas int32 `json:"sentinelReplicas"`

	// Quorum is the sentinel quorum.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=9
	Quorum int32 `json:"quorum"`

	// MasterName is the sentinel master set name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default:="mymaster"
	MasterName string `json:"masterName,omitempty"`

	Storage     StorageSpec `json:"storage,omitempty"`
	RedisPod    PodPolicy   `json:"redisPod,omitempty"`
	SentinelPod PodPolicy   `json:"sentinelPod,omitempty"`
}

// ExternalAddress represents one host:port endpoint.
type ExternalAddress struct {
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// ExternalNodeAddress represents one endpoint mapped to a StatefulSet ordinal.
type ExternalNodeAddress struct {
	// +kubebuilder:validation:Minimum=0
	Ordinal int32 `json:"ordinal"`

	ExternalAddress `json:",inline"`
}

// FailoverExternalAccessNodeSet stores per-pod external endpoints.
type FailoverExternalAccessNodeSet struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Nodes []ExternalNodeAddress `json:"nodes"`
}

// FailoverExternalAccessSpec configures failover external exposure.
type FailoverExternalAccessSpec struct {
	// +kubebuilder:validation:Enum=NodePort;LoadBalancer
	Type ExternalAccessType `json:"type"`

	Sentinel FailoverExternalAccessNodeSet `json:"sentinel"`
	Redis    FailoverExternalAccessNodeSet `json:"redis"`
}

// ClusterExternalNodeAddress represents one cluster external endpoint mapped to shard+ordinal.
type ClusterExternalNodeAddress struct {
	// +kubebuilder:validation:Minimum=0
	Shard int32 `json:"shard"`

	// +kubebuilder:validation:Minimum=0
	Ordinal int32 `json:"ordinal"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	BusPort int32 `json:"busPort"`

	ExternalAddress `json:",inline"`
}

// ClusterExternalAccessSpec configures cluster external exposure.
type ClusterExternalAccessSpec struct {
	// +kubebuilder:validation:Enum=NodePort;LoadBalancer
	Type ExternalAccessType `json:"type"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=768
	Nodes []ClusterExternalNodeAddress `json:"nodes"`
}

// ExternalAccessSpec configures external service exposure.
type ExternalAccessSpec struct {
	Failover *FailoverExternalAccessSpec `json:"failover,omitempty"`
	Cluster  *ClusterExternalAccessSpec  `json:"cluster,omitempty"`
}

// ExporterSpec configures optional redis_exporter sidecar.
type ExporterSpec struct {
	// Enabled controls whether exporter sidecar is injected.
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`

	// Image is the exporter image reference.
	// +kubebuilder:default:="docker.io/oliver006/redis_exporter:v1.67.0"
	Image string `json:"image,omitempty"`

	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// RedisSpec defines the desired state of Redis.
type RedisSpec struct {
	// Mode selects cluster or failover topology.
	// +kubebuilder:validation:Enum=cluster;failover
	Mode RedisMode `json:"mode"`

	// Image is a full image reference, for example docker.io/library/redis:8.6.1.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default:="docker.io/library/redis:8.6.1"
	Image string `json:"image,omitempty"`

	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default:="IfNotPresent"
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	Auth AuthSpec `json:"auth,omitempty"`

	TLS *TLSSpec `json:"tls,omitempty"`

	// RedisConfig accepts one Redis directive per line.
	// +kubebuilder:validation:MaxItems=512
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=512
	RedisConfig []string `json:"redisConfig,omitempty"`

	// SentinelConfig accepts one Sentinel directive per line.
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=512
	SentinelConfig []string `json:"sentinelConfig,omitempty"`

	Cluster  *ClusterSpec  `json:"cluster,omitempty"`
	Failover *FailoverSpec `json:"failover,omitempty"`

	ExternalAccess *ExternalAccessSpec `json:"externalAccess,omitempty"`

	Exporter ExporterSpec `json:"exporter,omitempty"`
}

// EndpointStatus stores user-facing internal and external endpoints.
type EndpointStatus struct {
	Internal []ExternalAddress `json:"internal,omitempty"`
	External []ExternalAddress `json:"external,omitempty"`
}

// RedisStatus defines the observed state of Redis.
type RedisStatus struct {
	Endpoints EndpointStatus       `json:"endpoints,omitempty"`
	Health    bool                 `json:"health,omitempty"`
	Reason    RedisHealthReason    `json:"reason,omitempty"`
	Cluster   ClusterRuntimeStatus `json:"cluster,omitempty"`
	// ClusterScale tracks shard scale operation status in Cluster mode.
	ClusterScale ClusterScaleStatus `json:"clusterScale,omitempty"`

	// ObservedRedisReadyReplicas is the current number of ready redis pods observed from StatefulSet status.
	ObservedRedisReadyReplicas int32 `json:"observedRedisReadyReplicas,omitempty"`

	// ObservedSentinelReadyReplicas is the current number of ready sentinel pods observed from StatefulSet status.
	ObservedSentinelReadyReplicas int32 `json:"observedSentinelReadyReplicas,omitempty"`

	// Conditions contains the latest observations of this resource's runtime state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=redis,scope=Namespaced,shortName=rds
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="InternalEndpoint",type=string,JSONPath=`.status.endpoints.internal[0].host`
// +kubebuilder:printcolumn:name="Health",type=boolean,JSONPath=`.status.health`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:validation:XValidation:rule="(self.spec.mode == 'cluster' && has(self.spec.cluster) && !has(self.spec.failover)) || (self.spec.mode == 'failover' && has(self.spec.failover) && !has(self.spec.cluster))",message="mode and sub-spec must match exactly one"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == 'failover' || !has(self.spec.sentinelConfig) || size(self.spec.sentinelConfig) == 0",message="sentinelConfig is only allowed in failover mode"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == 'failover' || !has(self.spec.externalAccess) || !has(self.spec.externalAccess.failover)",message="externalAccess.failover is only allowed in failover mode"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == 'cluster' || !has(self.spec.externalAccess) || !has(self.spec.externalAccess.cluster)",message="externalAccess.cluster is only allowed in cluster mode"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.externalAccess) || !(has(self.spec.externalAccess.failover) && has(self.spec.externalAccess.cluster))",message="externalAccess.failover and externalAccess.cluster are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.externalAccess) || !has(self.spec.externalAccess.failover) || self.spec.externalAccess.failover.type == 'NodePort'",message="externalAccess.failover.type must be NodePort in this release"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.externalAccess) || !has(self.spec.externalAccess.cluster) || self.spec.externalAccess.cluster.type == 'NodePort'",message="externalAccess.cluster.type must be NodePort in this release"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.failover) || self.spec.failover.quorum <= self.spec.failover.sentinelReplicas",message="failover.quorum must be <= failover.sentinelReplicas"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == oldSelf.spec.mode",message="mode is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.cluster) || !has(self.spec.cluster) || self.spec.cluster.shards == oldSelf.spec.cluster.shards || self.spec.cluster.shards == oldSelf.spec.cluster.shards + 1 || self.spec.cluster.shards == oldSelf.spec.cluster.shards - 1",message="cluster.shards can only change by 1 per update"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.cluster) || !has(self.spec.cluster) || self.spec.cluster.replicasPerShard == oldSelf.spec.cluster.replicasPerShard",message="cluster.replicasPerShard is immutable in v1"

// Redis is the Schema for the redis API.
type Redis struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RedisSpec   `json:"spec,omitempty"`
	Status RedisStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RedisList contains a list of Redis.
type RedisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Redis `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Redis{}, &RedisList{})
}
