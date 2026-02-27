package redis

import "testing"

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

type assertionErr string

func (e assertionErr) Error() string {
	return string(e)
}
