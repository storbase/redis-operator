package redis

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

func TestBuildSlotRanges(t *testing.T) {
	ranges := buildSlotRanges(3)
	if len(ranges) != 3 {
		t.Fatalf("expected 3 ranges, got %d", len(ranges))
	}

	if ranges[0].Start != 0 {
		t.Fatalf("first range should start at 0, got %d", ranges[0].Start)
	}
	if ranges[len(ranges)-1].End != totalClusterSlots-1 {
		t.Fatalf("last range should end at %d, got %d", totalClusterSlots-1, ranges[len(ranges)-1].End)
	}

	for i := 1; i < len(ranges); i++ {
		if ranges[i-1].End+1 != ranges[i].Start {
			t.Fatalf("ranges are not contiguous between %d and %d", i-1, i)
		}
	}
}

func TestBuildClusterTopology(t *testing.T) {
	topology, err := buildClusterTopology("redis-e2e", "sample", 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if topology.ExpectedNodes != 6 {
		t.Fatalf("expected 6 nodes, got %d", topology.ExpectedNodes)
	}
	if topology.ExpectedShards != 3 {
		t.Fatalf("expected 3 shards, got %d", topology.ExpectedShards)
	}
	if len(topology.Masters) != 3 {
		t.Fatalf("expected 3 masters, got %d", len(topology.Masters))
	}
	if len(topology.ReplicaToMaster) != 3 {
		t.Fatalf("expected 3 replicas, got %d", len(topology.ReplicaToMaster))
	}

	wantMaster := "sample-shard-0-0.sample-shard-0.redis-e2e.svc.cluster.local:6379"
	if topology.Masters[0] != wantMaster {
		t.Fatalf("unexpected first master, got %q want %q", topology.Masters[0], wantMaster)
	}

	wantReplica := "sample-shard-0-1.sample-shard-0.redis-e2e.svc.cluster.local:6379"
	if topology.ReplicaToMaster[wantReplica] != wantMaster {
		t.Fatalf("unexpected replica mapping for %q", wantReplica)
	}
}

func TestBuildSentinelAddresses(t *testing.T) {
	addrs := buildSentinelAddresses("redis-e2e", "sample", 3)
	if len(addrs) != 3 {
		t.Fatalf("expected 3 addresses, got %d", len(addrs))
	}

	want := "sample-sentinel-0.sample-sentinel.redis-e2e.svc.cluster.local:26379"
	if addrs[0] != want {
		t.Fatalf("unexpected first sentinel address, got %q want %q", addrs[0], want)
	}
}

func TestBuildFailoverRedisAddresses(t *testing.T) {
	addrs := buildFailoverRedisAddresses("redis-e2e", "sample", 4)
	if len(addrs) != 4 {
		t.Fatalf("expected 4 addresses, got %d", len(addrs))
	}

	want := "sample-redis-0.sample-redis-headless.redis-e2e.svc.cluster.local:6379"
	if addrs[0] != want {
		t.Fatalf("unexpected first redis address, got %q want %q", addrs[0], want)
	}
}

func TestNormalizeFailoverMasterAddress(t *testing.T) {
	redisAddrs := buildFailoverRedisAddresses("redis-e2e", "sample", 3)

	got, err := normalizeFailoverMasterAddress("sample-redis-2:6379", redisAddrs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "sample-redis-2.sample-redis-headless.redis-e2e.svc.cluster.local:6379"
	if got != want {
		t.Fatalf("unexpected normalized master address, got %q want %q", got, want)
	}
}

func TestNormalizeFailoverMasterAddressRejectsIP(t *testing.T) {
	redisAddrs := buildFailoverRedisAddresses("redis-e2e", "sample", 3)

	if _, err := normalizeFailoverMasterAddress("10.0.0.9:6379", redisAddrs, nil); err == nil {
		t.Fatalf("expected ip-like master host to be rejected")
	}
}

func TestNormalizeFailoverMasterAddressAcceptsConfiguredExternalIP(t *testing.T) {
	redisAddrs := buildFailoverRedisAddresses("redis-e2e", "sample", 3)
	external := []redisv1alpha1.ExternalNodeAddress{
		{
			Ordinal: 2,
			ExternalAddress: redisv1alpha1.ExternalAddress{
				Host: "10.0.0.10",
				Port: 32102,
			},
		},
	}

	got, err := normalizeFailoverMasterAddress("10.0.0.10:32102", redisAddrs, external)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "sample-redis-2.sample-redis-headless.redis-e2e.svc.cluster.local:6379"
	if got != want {
		t.Fatalf("unexpected normalized master address, got %q want %q", got, want)
	}
}

func TestReplicaTracksMaster(t *testing.T) {
	redisAddrs := buildFailoverRedisAddresses("redis-e2e", "sample", 3)
	aliases := buildFailoverHostAliases(redisAddrs)
	masterHost := "sample-redis-0.sample-redis-headless.redis-e2e.svc.cluster.local"

	fields := map[string]string{
		"role":               "slave",
		"master_host":        "sample-redis-0",
		"master_port":        "6379",
		"master_link_status": "up",
	}
	if !replicaTracksMaster(fields, aliases, masterHost, "6379") {
		t.Fatalf("expected replica fields to match master")
	}

	fields["master_host"] = "10.0.0.10"
	if replicaTracksMaster(fields, aliases, masterHost, "6379") {
		t.Fatalf("expected ip-based master host to be rejected")
	}
}

func TestParseInfoFields(t *testing.T) {
	raw := "# Header\ncluster_state:ok\ncluster_slots_assigned:16384\n"
	fields := parseInfoFields(raw)
	if fields["cluster_state"] != "ok" {
		t.Fatalf("unexpected cluster_state: %q", fields["cluster_state"])
	}
	if fields["cluster_slots_assigned"] != "16384" {
		t.Fatalf("unexpected cluster_slots_assigned: %q", fields["cluster_slots_assigned"])
	}
}

func TestIgnorableClusterErrors(t *testing.T) {
	if !isIgnorableMeetError(assertionErr("ERR already known node")) {
		t.Fatalf("expected meet error to be ignorable")
	}
	if !isIgnorableAddSlotsError(assertionErr("ERR Slot 123 is already busy")) {
		t.Fatalf("expected addslots error to be ignorable")
	}
	if !isIgnorableReplicateError(assertionErr("ERR I can only replicate a master, not a replica.")) {
		t.Fatalf("expected replicate error to be ignorable")
	}
	if !isRetryableReplicateError(assertionErr("ERR Unknown node 1234567890abcdef")) {
		t.Fatalf("expected unknown node replicate error to be retryable")
	}
	if !isRetryableReplicateError(assertionErr("ERR No such master with that ID")) {
		t.Fatalf("expected no such master replicate error to be retryable")
	}
	if isRetryableReplicateError(assertionErr("WRONGPASS invalid username-password pair")) {
		t.Fatalf("expected auth error not to be retryable")
	}
}

func TestPickMeetHost(t *testing.T) {
	host := "redis-0.redis.redis-e2e.svc.cluster.local"
	if got := pickMeetHost(host, nil); got != host {
		t.Fatalf("expected fallback host %q, got %q", host, got)
	}

	ips := []string{"fe80::1", "10.0.0.23"}
	if got := pickMeetHost(host, ips); got != "10.0.0.23" {
		t.Fatalf("expected IPv4 address, got %q", got)
	}

	if got := pickMeetHost(host, []string{"fe80::1"}); got != "fe80::1" {
		t.Fatalf("expected first address fallback, got %q", got)
	}
}

func TestReadTLSConfigReturnsNilWhenTLSDisabled(t *testing.T) {
	admin := &AdminClient{}
	cfg, err := admin.readTLSConfig(context.Background(), "default", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil tls config when tls is disabled")
	}
}

func TestReadTLSConfigLoadsCA(t *testing.T) {
	caPEM := mustBuildTestCAPEM(t)

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme failed: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "redis-tls",
		},
		Data: map[string][]byte{
			"ca.crt": caPEM,
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	admin := &AdminClient{kube: kubeClient}

	cfg, err := admin.readTLSConfig(context.Background(), "default", &redisv1alpha1.TLSSpec{SecretName: "redis-tls"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected non-nil tls config")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected tls min version: %d", cfg.MinVersion)
	}
	if cfg.RootCAs == nil {
		t.Fatalf("expected tls root CAs to be configured")
	}
}

func TestReadTLSConfigRejectsMissingCA(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme failed: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "redis-tls",
		},
		Data: map[string][]byte{},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	admin := &AdminClient{kube: kubeClient}

	_, err := admin.readTLSConfig(context.Background(), "default", &redisv1alpha1.TLSSpec{SecretName: "redis-tls"})
	if err == nil {
		t.Fatalf("expected error for missing ca.crt")
	}
}

func mustBuildTestCAPEM(t *testing.T) []byte {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key failed: %v", err)
	}

	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatalf("generate serial failed: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "redis-test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	raw, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate failed: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}

type assertionErr string

func (e assertionErr) Error() string {
	return string(e)
}
