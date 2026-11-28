package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotSkillProposalEvidenceAndSelectionRoundTrip(t *testing.T) {
	evidence := []BotWorkEvidenceReference{{Ref: "capture:current"}}
	for _, tc := range []struct {
		refs []string
		want bool
	}{
		{nil, false}, {[]string{"capture:foreign"}, false},
		{[]string{"capture:current", "capture:foreign"}, false},
		{[]string{"capture:current"}, true},
	} {
		if got := (&BotSkillProposal{EvidenceRefs: tc.refs}).HasEvidence(evidence); got != tc.want {
			t.Fatalf("refs=%v got=%v", tc.refs, got)
		}
	}
	var missing *BotSkillProposal
	if missing.HasEvidence(evidence) {
		t.Fatal("absent proposal certified")
	}
	at := time.Now().UTC()
	state := BotSkillRunState{SelectedAt: &at, SelectionRevision: 3}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored BotSkillRunState
	if err = json.Unmarshal(raw, &restored); err != nil || restored.SelectedAt == nil || !restored.SelectedAt.Equal(at) || restored.SelectionRevision != 3 {
		t.Fatalf("selection boundary lost: %+v %v", restored, err)
	}
}

func skillFixture() BotSkillRevision {
	botID, skillID, runID := uuid.New(), uuid.New(), uuid.New()
	p := BotSkillProvenance{Level: BotSkillOwnerSpecified, BotID: botID, RunID: runID, SessionID: uuid.New(), EventID: "owner-message", SourceDigest: strings.Repeat("a", 64), Quote: "Send a concise market briefing", RecordedAt: time.Now().UTC()}
	r := BotSkillRevision{SchemaVersion: BotSkillSchemaVersion, ID: uuid.New(), BotID: botID, SkillID: skillID, Revision: 1, Responsibility: "Market briefing", Expectations: []BotSkillExpectation{{ID: "format", Text: p.Quote, Provenance: p}}, Method: []string{"Discover suitable sources and inspect publication dates"}, ChangeReason: "Initial owner assignment", Provenance: []BotSkillProvenance{p}}
	r.Digest = r.ContentDigest()
	return r
}

func TestBotSkillSameKnowledge(t *testing.T) {
	old := skillFixture()
	next := old.Clone()
	next.ID, next.Revision, next.ParentRevision, next.ParentDigest = uuid.New(), 2, 1, old.Digest
	next.ChangeReason = "Reaffirm"
	next.Digest = next.ContentDigest()
	if old.Digest == next.Digest || !old.SameKnowledge(*next) {
		t.Fatal("envelope must change canonical hash, not knowledge")
	}
	next.Provenance[0].RecordedAt = next.Provenance[0].RecordedAt.Add(time.Second)
	if old.SameKnowledge(*next) {
		t.Fatal("new evidence was incorrectly discarded")
	}
}

func TestBotSkillContracts(t *testing.T) {
	r := skillFixture()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	inv := BotSkillInvocation{Intent: "Send the market briefing", SkillID: r.SkillID, Revision: r.Revision, Digest: r.Digest, Snapshot: r}
	if err := inv.Validate(r.BotID); err != nil {
		t.Fatal(err)
	}
	clone := inv.Clone()
	clone.Snapshot.Method[0] = "changed"
	if inv.Snapshot.Method[0] == "changed" {
		t.Fatal("pin aliases mutable method")
	}
	if clone.Validate(r.BotID) == nil {
		t.Fatal("changed snapshot accepted with old digest")
	}
	if !errors.Is(inv.Validate(uuid.New()), ErrBotForbidden) {
		t.Fatal("foreign bot accepted")
	}
	r.Responsibility = " "
	r.Digest = r.ContentDigest()
	if r.Validate() == nil {
		t.Fatal("empty responsibility invented")
	}
}

func TestBotSkillExpectationProvenance(t *testing.T) {
	for _, mutate := range []func(*BotSkillRevision){
		func(r *BotSkillRevision) { r.Expectations[0].Text = "Unrelated model-authored requirement" },
		func(r *BotSkillRevision) { r.Expectations[0].Provenance.Level = BotSkillProposed },
		func(r *BotSkillRevision) { r.Expectations[0].Provenance.EventID = "" },
		func(r *BotSkillRevision) { r.Expectations[0].Provenance.SourceDigest = "not-a-digest" },
		func(r *BotSkillRevision) { r.Expectations[0].Provenance.BotID = uuid.New() },
		func(r *BotSkillRevision) { r.Expectations = append(r.Expectations, r.Expectations[0]) },
	} {
		r := skillFixture()
		mutate(&r)
		r.Digest = r.ContentDigest()
		if r.Validate() == nil {
			t.Fatal("unbacked expectation accepted")
		}
	}
	r := skillFixture()
	r.Sources = []BotSkillSource{{URL: "https://example.org/news", Purpose: "Headlines", AccessMethod: "GET", Provenance: BotSkillProvenance{Level: BotSkillProposed, BotID: r.BotID}}}
	r.Digest = r.ContentDigest()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:password@example.org/news"} {
		r.Sources[0].URL = raw
		r.Digest = r.ContentDigest()
		if r.Validate() == nil {
			t.Fatal("unsafe source URL accepted")
		}
	}
	r.Sources[0].URL = "https://example.org/news"
	r.Sources[0].Provenance.Level = BotSkillObserved
	r.Digest = r.ContentDigest()
	if r.Validate() == nil {
		t.Fatal("unobserved source marked observed")
	}
}

func TestBotSkillLimits(t *testing.T) {
	for name, mutate := range map[string]func(*BotSkillRevision){
		"bytes":        func(r *BotSkillRevision) { r.OutputFormat = strings.Repeat("x", BotSkillMaxRevisionBytes) },
		"steps":        func(r *BotSkillRevision) { r.Method = make([]string, BotSkillMaxMethodSteps+1) },
		"sources":      func(r *BotSkillRevision) { r.Sources = make([]BotSkillSource, BotSkillMaxSources+1) },
		"expectations": func(r *BotSkillRevision) { r.Expectations = make([]BotSkillExpectation, BotSkillMaxExpectations+1) },
		"evidence":     func(r *BotSkillRevision) { r.Provenance = make([]BotSkillProvenance, BotSkillMaxEvidenceRefs+1) },
		"revisions":    func(r *BotSkillRevision) { r.Revision = BotSkillMaxRevisions + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := skillFixture()
			mutate(&r)
			r.Digest = r.ContentDigest()
			if r.Validate() == nil {
				t.Fatal("limit not enforced")
			}
		})
	}
	if (BotSkillPage{Limit: BotSkillMaxPageSize + 1}).Validate() == nil {
		t.Fatal("unbounded page accepted")
	}
	if ValidateBotSkillQuota(BotSkillMaxSkills, 0, 1) == nil || ValidateBotSkillQuota(0, BotSkillMaxTotalBytes, 1) == nil {
		t.Fatal("quota not enforced")
	}
}

func TestBotSkillLegacyDigest(t *testing.T) {
	// Golden representations predate skills: nil additions must not change hashes.
	for name, tc := range map[string]struct {
		value any
		old   string
	}{
		"trigger":  {TriggerSpec{}, `{"id":"00000000-0000-0000-0000-000000000000","kind":"","instructions":"","enabled":false,"catch_up_once":false}`},
		"task":     {BotControlTaskOnce{Instructions: "report"}, `{"instructions":"report"}`},
		"contract": {BotWorkContract{}, `{"bot_id":"00000000-0000-0000-0000-000000000000","run_id":"00000000-0000-0000-0000-000000000000","version":0,"instructions":"","charter":""}`},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.old {
				t.Fatalf("legacy bytes changed: %s", b)
			}
		})
	}
	old := NewBotWorkState(BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "report", Charter: "help"})
	next := old.Clone()
	next.Phase = BotWorkProducing
	next.Criteria = []BotWorkCriterion{{ID: "c", Text: "report", SourceIDs: []string{"assignment", "charter"}}}
	if err := ValidateBotWorkUpdate(old, next); err != nil {
		t.Fatal(err)
	}
	if old.RequirementsDigest != next.RequirementsDigest {
		t.Fatal("legacy digest changed")
	}
	r := skillFixture()
	pin := &BotSkillInvocation{Intent: "report", SkillID: r.SkillID, Revision: 1, Digest: r.Digest, Snapshot: r}
	c := BotWorkContract{BotID: r.BotID, RunID: uuid.New(), Version: 1, Instructions: "report", Charter: "help", Skill: pin}
	w := NewBotWorkState(c)
	w.SkillState = &BotSkillRunState{Stage: BotSkillOriented}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	copied := w.Clone()
	copied.Contract.Skill.Snapshot.Method[0] = "mutated"
	if reflect.DeepEqual(w, copied) {
		t.Fatal("work clone not detached")
	}
	next = old.Clone()
	next.SkillState = &BotSkillRunState{Stage: BotSkillUnoriented}
	next.Phase = BotWorkProducing
	next.Criteria = []BotWorkCriterion{{ID: "c", Text: "report", SourceIDs: []string{"assignment", "charter"}}}
	if ValidateBotWorkUpdate(old, next) == nil {
		t.Fatal("ordinary work writer changed skill state")
	}
}

func TestBotSkillReceiptIntegrity(t *testing.T) {
	r := skillFixture()
	pin := &BotSkillInvocation{Intent: "report", SkillID: r.SkillID, Revision: 1, Digest: r.Digest, Snapshot: r}
	c := BotWorkContract{BotID: r.BotID, RunID: uuid.New(), Skill: pin}
	next := *r.Clone()
	next.ID = uuid.New()
	next.Revision = 2
	next.ParentRevision = 1
	next.ParentDigest = r.Digest
	next.Method = []string{"Use observed source"}
	next.Digest = next.ContentDigest()
	learning := &BotSkillLearning{SkillID: r.SkillID, ParentRevision: 1, ParentDigest: r.Digest, SourceRunID: c.RunID, CandidateDigest: strings.Repeat("b", 64), EvidenceDigest: strings.Repeat("c", 64), Proposed: next}
	receipt := BotSkillReceipt{SchemaVersion: 1, BotID: r.BotID, RunID: c.RunID, SkillID: r.SkillID, Kind: BotSkillUpdated, UsedRevision: 1, CommittedRevision: 2, Digest: next.Digest}
	s := BotSkillRunState{Stage: BotSkillLearningCommitted, Learning: learning, Receipt: &receipt}
	if err := s.Validate(c); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BotSkillReceipt){func(x *BotSkillReceipt) { x.CommittedRevision = 1 }, func(x *BotSkillReceipt) { x.CommittedRevision = 99 }, func(x *BotSkillReceipt) { x.Digest = strings.Repeat("d", 64) }, func(x *BotSkillReceipt) { x.Kind = BotSkillUnchanged }} {
		bad := receipt
		mutate(&bad)
		s.Receipt = &bad
		if s.Validate(c) == nil {
			t.Fatal("forged receipt accepted")
		}
	}
}

func TestBotSkillScheduleSelection(t *testing.T) {
	id := uuid.New()
	for _, tr := range []TriggerSpec{{SkillID: &id}, {Invocation: "report"}, {SkillID: new(uuid.UUID), Invocation: "report"}} {
		p := BotControlPayload{Operation: BotControlScheduleCreate, Reason: "schedule", Schedule: &BotControlSchedule{Trigger: tr}}
		if p.Validate() == nil {
			t.Fatal("invalid schedule skill selection accepted")
		}
	}
	p := BotControlPayload{Operation: BotControlTaskOnceOperation, Reason: "run", TaskOnce: &BotControlTaskOnce{SkillID: &id, Invocation: "report"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBotSkillStateTransitions(t *testing.T) {
	noSkill := BotSkillRunState{Stage: BotSkillOriented, NoSkillNeeded: true}
	if err := noSkill.Validate(BotWorkContract{}); err != nil {
		t.Fatal(err)
	}
	noSkill.Stage = BotSkillLearningStaged
	if noSkill.Validate(BotWorkContract{}) == nil {
		t.Fatal("learning without a skill accepted")
	}
	for _, tc := range []struct {
		from, to           BotSkillStatus
		explicit, verified bool
		valid              bool
	}{
		{BotSkillDraft, BotSkillActive, false, true, true},
		{BotSkillDraft, BotSkillActive, false, false, false},
		{BotSkillActive, BotSkillArchived, true, false, true},
		{BotSkillActive, BotSkillArchived, false, false, false},
		{BotSkillArchived, BotSkillActive, true, true, true},
		{BotSkillArchived, BotSkillActive, false, true, false},
		{BotSkillArchived, BotSkillDraft, true, false, true},
		{BotSkillActive, BotSkillDraft, true, false, false},
	} {
		if got := ValidateBotSkillLifecycle(tc.from, tc.to, tc.explicit, tc.verified) == nil; got != tc.valid {
			t.Fatalf("transition %s -> %s: %v", tc.from, tc.to, got)
		}
	}
	for _, tc := range []struct {
		from, to BotSkillStage
		valid    bool
	}{
		{BotSkillUnoriented, BotSkillOriented, true},
		{BotSkillOriented, BotSkillLearningStaged, true},
		{BotSkillLearningStaged, BotSkillLearningCommitted, true},
		{BotSkillLearningStaged, BotSkillLearningDeferred, true},
		{BotSkillUnoriented, BotSkillLearningCommitted, false},
		{BotSkillLearningCommitted, BotSkillOriented, false},
		{BotSkillLearningDeferred, BotSkillLearningStaged, false},
	} {
		if got := ValidateBotSkillStage(tc.from, tc.to) == nil; got != tc.valid {
			t.Fatalf("stage %s -> %s: %v", tc.from, tc.to, got)
		}
	}
}
