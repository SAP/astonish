package store

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotKnowledgeExtractionTransitions(t *testing.T) {
	states := []BotKnowledgeExtractionState{BotKnowledgeExtractionPending, BotKnowledgeExtractionExtracting, BotKnowledgeExtractionPrepared, BotKnowledgeExtractionFilesReady, BotKnowledgeExtractionCommitted, BotKnowledgeExtractionNoChange, BotKnowledgeExtractionHeld, BotKnowledgeExtractionAborted, "unknown"}
	allowed := map[[2]BotKnowledgeExtractionState]bool{
		{BotKnowledgeExtractionPending, BotKnowledgeExtractionExtracting}: true, {BotKnowledgeExtractionPending, BotKnowledgeExtractionHeld}: true, {BotKnowledgeExtractionPending, BotKnowledgeExtractionAborted}: true,
		{BotKnowledgeExtractionExtracting, BotKnowledgeExtractionPrepared}: true, {BotKnowledgeExtractionExtracting, BotKnowledgeExtractionNoChange}: true, {BotKnowledgeExtractionExtracting, BotKnowledgeExtractionHeld}: true,
		{BotKnowledgeExtractionPrepared, BotKnowledgeExtractionFilesReady}: true, {BotKnowledgeExtractionPrepared, BotKnowledgeExtractionHeld}: true, {BotKnowledgeExtractionPrepared, BotKnowledgeExtractionAborted}: true,
		{BotKnowledgeExtractionFilesReady, BotKnowledgeExtractionCommitted}: true, {BotKnowledgeExtractionFilesReady, BotKnowledgeExtractionHeld}: true, {BotKnowledgeExtractionFilesReady, BotKnowledgeExtractionAborted}: true,
		{BotKnowledgeExtractionHeld, BotKnowledgeExtractionAborted}: true,
	}
	for _, from := range states {
		for _, to := range states {
			if (ValidateBotKnowledgeExtractionTransition(from, to, false) == nil) != allowed[[2]BotKnowledgeExtractionState{from, to}] {
				t.Fatalf("%s -> %s", from, to)
			}
		}
	}
	if ValidateBotKnowledgeExtractionTransition(BotKnowledgeExtractionHeld, BotKnowledgeExtractionPrepared, true) != nil {
		t.Fatal("prepared retry denied")
	}
}
func knowledgeTestInput() BotKnowledgeExtractionInput {
	o := BotKnowledgeOutcome{Status: "failed", Failure: "search failed", Tools: []BotKnowledgeToolEvidence{{ID: "x", Name: "search", Failure: "timeout"}}}
	return BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: true, OwnerMessage: "EUR", Source: BotKnowledgeExtractionSource{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Sequence: 1, OutcomeDigest: BotKnowledgeDigest(o)}, Outcome: o, PriorFiles: []BotKnowledgeFileSummary{{Path: "preferences/currency.md", Digest: BotKnowledgeDigest("EUR"), Summary: "EUR"}}}
}
func TestBotKnowledgeExtractionInputBoundsAndClone(t *testing.T) {
	i := knowledgeTestInput()
	if i.Validate() != nil {
		t.Fatal("valid rejected")
	}
	c := i.Clone()
	c.Outcome.Tools[0].Failure = "changed"
	c.PriorFiles[0].Summary = "changed"
	if i.Outcome.Tools[0].Failure != "timeout" || i.PriorFiles[0].Summary != "EUR" {
		t.Fatal("clone alias")
	}
	for _, mutate := range []func(*BotKnowledgeExtractionInput){func(i *BotKnowledgeExtractionInput) { i.SchemaVersion = 2 }, func(i *BotKnowledgeExtractionInput) { i.Complete = false }, func(i *BotKnowledgeExtractionInput) {
		i.Conversation = strings.Repeat("x", BotKnowledgeExtractionMaxInputBytes)
	}, func(i *BotKnowledgeExtractionInput) { i.Source.Sequence = 0 }, func(i *BotKnowledgeExtractionInput) { i.Outcome.Failure = "drift" }, func(i *BotKnowledgeExtractionInput) { i.PriorFiles = append(i.PriorFiles, i.PriorFiles[0]) }} {
		c := i.Clone()
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid accepted")
		}
	}
	c = i.Clone()
	c.PriorFiles[0].Path = "tasks/daily/scripts/helper.py"
	if c.Validate() != nil {
		t.Fatal("prior supporting file summary rejected")
	}
	c.PriorFiles[0].Path = "../secret"
	if c.Validate() == nil {
		t.Fatal("outside summary accepted")
	}
}
func TestBotKnowledgeExtractionProposalClosedSchema(t *testing.T) {
	d := BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: BotKnowledgeDecisionChanges, Reason: "correction", Proposals: []BotKnowledgeProposal{{Kind: BotKnowledgeProposalPreference, Path: "preferences/currency.md", Content: "EUR", EvidenceIDs: []string{"x"}}}}
	if d.Validate() != nil {
		t.Fatal("valid rejected")
	}
	for _, p := range []string{"../secret.md", "preferences/../secrets.md", "preferences//x.md", "procedures/run.py", "goals/GOAL.md", "tasks/TASK.md", "/preferences/x.md", "preferences/x\\y.md"} {
		c := d.Clone()
		c.Proposals[0].Path = p
		if c.Validate() == nil {
			t.Fatalf("unsafe path %s", p)
		}
	}
	c := d.Clone()
	c.Proposals[0].Kind = "script"
	if c.Validate() == nil {
		t.Fatal("script promotion accepted")
	}
	c = d.Clone()
	c.Proposals = append(c.Proposals, c.Proposals[0])
	if c.Validate() == nil {
		t.Fatal("duplicate path")
	}
	c = d.Clone()
	c.Kind = BotKnowledgeDecisionNoChange
	if c.Validate() == nil {
		t.Fatal("no-change with edits")
	}
	c = d.Clone()
	c.Proposals[0].EvidenceIDs[0] = "mutated"
	if d.Proposals[0].EvidenceIDs[0] != "x" {
		t.Fatal("clone alias")
	}
}
func TestBotKnowledgeExtractionDecisionClonePreservesDigest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		proposals []BotKnowledgeProposal
	}{
		{name: "nil"},
		{name: "empty", proposals: []BotKnowledgeProposal{}},
		{name: "nonempty_nil_evidence", proposals: []BotKnowledgeProposal{{Kind: BotKnowledgeProposalPreference, Path: "preferences/currency.md", Content: "EUR"}}},
		{name: "nonempty_empty_evidence", proposals: []BotKnowledgeProposal{{Kind: BotKnowledgeProposalPreference, Path: "preferences/currency.md", Content: "EUR", EvidenceIDs: []string{}}}},
		{name: "nonempty_evidence", proposals: []BotKnowledgeProposal{{Kind: BotKnowledgeProposalPreference, Path: "preferences/currency.md", Content: "EUR", EvidenceIDs: []string{"x"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := BotKnowledgeExtractionDecision{SchemaVersion: BotKnowledgeExtractionSchemaVersion, Kind: BotKnowledgeDecisionNoChange, Reason: "preference", Proposals: tc.proposals}
			if len(d.Proposals) > 0 {
				d.Kind = BotKnowledgeDecisionChanges
			}
			if err := d.Validate(); err != nil {
				t.Fatal(err)
			}
			before := BotKnowledgeDigest(d)
			clone := d.Clone()
			if !reflect.DeepEqual(d, clone) {
				t.Errorf("clone changed decision shape: original %#v; clone %#v", d, clone)
			}
			if got := BotKnowledgeDigest(clone); got != before {
				t.Errorf("clone changed digest: got %s, want %s", got, before)
			}
			if len(clone.Proposals) > 0 {
				clone.Proposals[0].Content = "changed"
				if len(clone.Proposals[0].EvidenceIDs) > 0 {
					clone.Proposals[0].EvidenceIDs[0] = "changed"
				}
				clone.Proposals[0].EvidenceIDs = append(clone.Proposals[0].EvidenceIDs, "new")
			}
			clone.Proposals = append(clone.Proposals, BotKnowledgeProposal{Content: "new"})
			if got := BotKnowledgeDigest(d); got != before {
				t.Errorf("mutating clone changed original digest: got %s, want %s", got, before)
			}
		})
	}
}

func TestBotKnowledgeExtractionLeaseFreshAuthority(t *testing.T) {
	now := time.Now()
	l := BotKnowledgeExtractionLease{BotID: uuid.New(), ExtractionID: uuid.New(), WorkerID: "worker", Fence: 1, ExpiresAt: now.Add(time.Minute)}
	if l.Validate(now) != nil {
		t.Fatal("valid lease")
	}
	if l.Validate(l.ExpiresAt) == nil {
		t.Fatal("expired lease accepted")
	}
	l.Fence = 0
	if l.Validate(now) == nil {
		t.Fatal("zero fence")
	}
}
func TestBotKnowledgeExtractionPublicationReadbackCAS(t *testing.T) {
	now := time.Now()
	d := BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: BotKnowledgeDecisionChanges, Reason: "preference", Proposals: []BotKnowledgeProposal{{Kind: BotKnowledgeProposalPreference, Path: "preferences/currency.md", Content: "EUR"}}}
	r := BotKnowledgeExtraction{ID: uuid.New(), Input: knowledgeTestInput(), State: BotKnowledgeExtractionPrepared, Revision: 3, AttemptID: uuid.New(), Decision: &d, DecisionDigest: BotKnowledgeDigest(d)}
	l := BotKnowledgeExtractionLease{BotID: r.Input.Source.BotID, ExtractionID: r.ID, WorkerID: "w", Fence: 2, ExpiresAt: now.Add(time.Minute)}
	sum := sha256.Sum256([]byte("EUR"))
	q := BotKnowledgeExtractionTransition{Lease: l, ExpectedRevision: 3, From: r.State, To: BotKnowledgeExtractionFilesReady, Readback: []BotKnowledgeFileReadback{{Path: d.Proposals[0].Path, Digest: hex.EncodeToString(sum[:])}}}
	if q.Validate(r, now) != nil {
		t.Fatal("valid readback rejected")
	}
	bad := q
	bad.Readback = nil
	if bad.Validate(r, now) == nil {
		t.Fatal("missing readback")
	}
	bad = q
	bad.ExpectedRevision--
	if bad.Validate(r, now) == nil {
		t.Fatal("stale revision")
	}
	bad = q
	changed := d.Clone()
	changed.Proposals[0].Content = "USD"
	bad.Decision = &changed
	bad.DecisionDigest = BotKnowledgeDigest(changed)
	if bad.Validate(r, now) == nil {
		t.Fatal("prepared decision replaced")
	}
	bad = q
	bad.Readback = append(bad.Readback, bad.Readback[0])
	if bad.Validate(r, now) == nil {
		t.Fatal("duplicate readback")
	}
	r.State = BotKnowledgeExtractionHeld
	q.From = r.State
	q.To = BotKnowledgeExtractionPrepared
	q.Readback = nil
	if q.Validate(r, now) != nil {
		t.Fatal("fresh prepared retry rejected")
	}
	r.Decision = nil
	if q.Validate(r, now) == nil {
		t.Fatal("uncertain replay permitted")
	}
}
