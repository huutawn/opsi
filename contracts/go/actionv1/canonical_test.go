package actionv1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCanonicalPlanHashStableAcrossRoundTrip(t *testing.T) {
	plan := testPlan()
	first, err := PlanHash(plan)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(plan)
	var decoded ActionPlan
	if err := DecodeStrict(body, &decoded); err != nil {
		t.Fatal(err)
	}
	second, err := PlanHash(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hash changed across round trip: %s != %s", first, second)
	}
}

func TestCanonicalPlanHashBindsAuthorityAndExpiry(t *testing.T) {
	base := testPlan()
	baseHash, err := PlanHash(base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*ActionPlan){
		func(p *ActionPlan) { p.ProjectID = "other"; p.Target.ProjectID = "other" },
		func(p *ActionPlan) { p.Target.RuntimeID = "other" },
		func(p *ActionPlan) { p.Parameters.ScaleWorkload.Replicas++ },
		func(p *ActionPlan) { p.CurrentStateHash = strings.Repeat("c", 64) },
		func(p *ActionPlan) { p.ExpiresAt = p.ExpiresAt.Add(time.Second) },
	}
	for i, mutate := range mutations {
		changed := testPlan()
		mutate(&changed)
		hash, err := PlanHash(changed)
		if err != nil {
			t.Fatal(err)
		}
		if hash == baseHash {
			t.Fatalf("mutation %d did not change hash", i)
		}
	}
}

func TestCurrentStateHashIsDeterministicAndSensitive(t *testing.T) {
	state := CurrentState{SchemaVersion: SchemaVersion, ProjectID: "p1", Target: TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1"}, Workload: &WorkloadState{UID: "uid", ResourceVersion: "7", Generation: 3, DesiredReplicas: 2, ObservedReplicas: 2, ReadyReplicas: 2}}
	first, err := StateHash(state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StateHash(state)
	if err != nil || first != second {
		t.Fatalf("state hash is not deterministic: %s %s %v", first, second, err)
	}
	state.Workload.ReadyReplicas--
	changed, _ := StateHash(state)
	if changed == first {
		t.Fatal("factual state change did not change hash")
	}
}

func TestChallengeSigningBytesExcludeSignatureAndTerminalMetadata(t *testing.T) {
	challenge := ApprovalChallenge{SchemaVersion: SchemaVersion, ID: "ch", ActionID: "a", ProjectID: "p", PlanHash: strings.Repeat("a", 64), StateHash: strings.Repeat("b", 64), Nonce: "n", IssuedAt: time.Unix(1_800_000_000, 0).UTC(), ExpiresAt: time.Unix(1_800_000_060, 0).UTC()}
	first, err := ChallengeSigningBytes(challenge)
	if err != nil {
		t.Fatal(err)
	}
	challenge.Summary = "mutable display text"
	second, err := ChallengeSigningBytes(challenge)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("mutable summary changed signing bytes")
	}
}
