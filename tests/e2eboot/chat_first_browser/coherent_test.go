package main

import (
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestExactGrantRejectsScopeExpansion(t *testing.T) {
	p := store.BotControlProposal{Payload: store.BotControlPayload{Configuration: &store.BotControlConfiguration{Autonomy: store.BotAutonomy{Level: execution.AutonomySupervised, AllowedTools: []string{"write_file", "load_file"}, AllowedPaths: []string{retainedArtifactPath}}}}}
	if !exactGrant(p) {
		t.Fatal("exact grant rejected")
	}
	p.Payload.Configuration.Autonomy.AllowedPaths = []string{"/root"}
	if exactGrant(p) {
		t.Fatal("broad root permitted")
	}
	p.Payload.Configuration.Autonomy.AllowedPaths = []string{retainedArtifactPath}
	p.Payload.Configuration.Autonomy.AllowedHosts = []string{"example.com"}
	if exactGrant(p) {
		t.Fatal("network permitted")
	}
}
func TestBoundedWaitFailsClosed(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("missing assertion passed")
		}
	}()
	boundedWait("missing", 0, func() bool { return false })
}
func TestBoundedWaitImmediateSuccess(t *testing.T) {
	boundedWait("ready", time.Second, func() bool { return true })
}
func TestArtifactCardsMatchIndependentWriteAndLoad(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3} {
		panicked := false
		func() {
			defer func() { panicked = recover() != nil }()
			requireArtifactCards(count)
		}()
		if panicked != (count != 2) {
			t.Errorf("count=%d panicked=%v; exactly one card for each of write and load required", count, panicked)
		}
	}
}

func TestCoherentEvidenceContract(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range coherentRequired {
		if seen[n] {
			t.Fatal("duplicate", n)
		}
		seen[n] = true
	}
	if len(seen) != 9 {
		t.Fatal("expected nine independent assertions")
	}
}
