package store

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotAgentWorkRejectsUnknownProtocol(t *testing.T) {
	w := NewBotWorkState(attachmentWorkContract())
	raw, _ := json.Marshal(w)
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["protocol"] = "unknown"
	raw, _ = json.Marshal(document)
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if w.Validate() == nil {
		t.Fatal("unknown work protocol accepted as legacy")
	}
}

func TestBotAgentWorkContracts(t *testing.T) {
	w := NewBotAgentWorkState(attachmentWorkContract())
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if w.Phase != BotWorkWorking || w.Review != nil || len(w.Criteria) != 0 {
		t.Fatal("agent work invented a review")
	}
	w.AgentWork.Items = []BotWorkItem{{ID: uuid.New(), Summary: "Read sources", Status: BotWorkItemInProgress}, {ID: uuid.New(), Summary: "Inspect file", Status: BotWorkItemInProgress}}
	if err := w.Validate(); err != nil {
		t.Fatal("parallel agent items rejected", err)
	}
	copy := w.Clone()
	copy.AgentWork.Items[0].Summary = "changed"
	if w.AgentWork.Items[0].Summary == "changed" {
		t.Fatal("clone aliases work")
	}
	p := BuildBotWorkProjection(w)
	if p.Validate() != nil {
		t.Fatal("invalid projection")
	}
	p.Items[0].Summary = "projection change"
	if w.AgentWork.Items[0].Summary == p.Items[0].Summary {
		t.Fatal("projection aliases private work")
	}
	for _, item := range w.AgentWork.Items {
		if item.Status == BotWorkItemCompleted {
			t.Fatal("progress became completion")
		}
	}

	legacy := NewBotWorkState(attachmentWorkContract())
	raw, _ := json.Marshal(legacy)
	if bytes.Contains(raw, []byte(`"protocol"`)) || bytes.Contains(raw, []byte(`"agent_work"`)) {
		t.Fatal("legacy JSON changed")
	}
	var roundtrip BotWorkState
	if json.Unmarshal(raw, &roundtrip) != nil || roundtrip.Validate() != nil || roundtrip.RequirementsDigest != legacy.RequirementsDigest {
		t.Fatal("legacy roundtrip changed")
	}
	rawAgain, _ := json.Marshal(roundtrip)
	if !bytes.Equal(raw, rawAgain) {
		t.Fatal("legacy bytes changed")
	}
}

func TestBotAgentWorkTransitions(t *testing.T) {
	working := NewBotAgentWorkState(attachmentWorkContract())
	for _, phase := range []BotWorkPhase{BotWorkWorking, BotWorkFinished, BotWorkBlocked, BotWorkInterrupted, BotWorkAccepted, BotWorkReviewing} {
		next := working.Clone()
		next.Phase = phase
		if phase == BotWorkFinished {
			next.AgentWork.Disposition = AgentWorkAnswered
			next.Candidate = &BotWorkCandidate{Revision: 1, EventID: "final", Text: "Answer"}
			next.Candidate.Digest = next.Candidate.ContentDigest()
		}
		if phase == BotWorkBlocked || phase == BotWorkInterrupted {
			next.Unresolved = []string{"Needs owner input"}
		}
		err := ValidateBotWorkUpdate(working, next)
		want := phase != BotWorkAccepted && phase != BotWorkReviewing
		if (err == nil) != want {
			t.Fatalf("phase %s: %v", phase, err)
		}
		if want && phase != BotWorkWorking {
			resume := next.Clone()
			resume.Phase = BotWorkWorking
			if ValidateBotWorkUpdate(next, resume) == nil {
				t.Fatal("generic update resumed terminal work")
			}
		}
	}
	legacy := NewBotWorkState(working.Contract)
	if ValidateBotWorkUpdate(legacy, working) == nil {
		t.Fatal("protocol changed through ordinary update")
	}
	next := working.Clone()
	next.Contract.Instructions = "different"
	next.RequirementsDigest = botWorkDigest(next.Contract)
	if ValidateBotWorkUpdate(working, next) == nil {
		t.Fatal("owner assignment replaced")
	}
}

func TestBotAgentWorkValidation(t *testing.T) {
	base := NewBotAgentWorkState(attachmentWorkContract())
	base.AgentWork.Items = []BotWorkItem{{ID: uuid.New(), Summary: "Read", Status: BotWorkItemCompleted}}
	base.AgentWork.Evidence = []BotWorkEvidenceReference{{Ref: "capture:1", RunID: base.Contract.RunID, EventID: "event", CallID: "call", Digest: strings.Repeat("a", 64)}}
	base.AgentWork.SourceAccess = []BotSourceAccessFact{{EvidenceRef: "capture:1", CallID: "call", OriginalURL: "https://example.org", Status: BotSourceAccessReturned, HTTPStatus: 429, ObservedAt: time.Now().UTC(), BodyDigest: strings.Repeat("a", 64)}}
	base.AgentWork.Checks = []BotWorkCheckReceipt{{ID: uuid.New(), CallID: "check", Kind: BotWorkCheckModelFeedback, Status: BotWorkCheckFailed, Judgment: BotWorkCheckUnverified, Summary: "Unavailable", DraftDigest: strings.Repeat("b", 64), ObservedAt: time.Now().UTC()}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*BotWorkState){
		"unknown disposition":    func(w *BotWorkState) { w.AgentWork.Disposition = "pass" },
		"unknown item":           func(w *BotWorkState) { w.AgentWork.Items[0].Status = "pass" },
		"duplicate item":         func(w *BotWorkState) { w.AgentWork.Items = append(w.AgentWork.Items, w.AgentWork.Items[0]) },
		"blocked without reason": func(w *BotWorkState) { w.AgentWork.Items[0].Status = BotWorkItemBlocked },
		"foreign evidence":       func(w *BotWorkState) { w.AgentWork.Evidence[0].RunID = uuid.New() },
		"missing ref":            func(w *BotWorkState) { w.AgentWork.Items[0].EvidenceRefs = []string{"missing"} },
		"false check pass":       func(w *BotWorkState) { w.AgentWork.Checks[0].Judgment = BotWorkCheckSupported },
		"unknown check kind":     func(w *BotWorkState) { w.AgentWork.Checks[0].Kind = "trusted" },
		"unknown check status":   func(w *BotWorkState) { w.AgentWork.Checks[0].Status = "done" },
		"source call mismatch":   func(w *BotWorkState) { w.AgentWork.SourceAccess[0].CallID = "other" },
		"source digest mismatch": func(w *BotWorkState) { w.AgentWork.SourceAccess[0].BodyDigest = strings.Repeat("c", 64) },
		"fake review":            func(w *BotWorkState) { w.Review = &BotWorkReview{} },
		"oversized notes":        func(w *BotWorkState) { w.AgentWork.Notes = strings.Repeat("x", BotAgentWorkMaxNotesBytes+1) },
		"oversized aggregate": func(w *BotWorkState) {
			w.AgentWork.Items[0].Summary = strings.Repeat("x", BotAgentWorkMaxMetadataBytes)
		},
		"too many items":       func(w *BotWorkState) { w.AgentWork.Items = make([]BotWorkItem, BotAgentWorkMaxItems+1) },
		"too many obligations": func(w *BotWorkState) { w.AgentWork.Remaining = make([]BotWorkObligation, BotAgentWorkMaxObligations+1) },
		"too many checks":      func(w *BotWorkState) { w.AgentWork.Checks = make([]BotWorkCheckReceipt, BotAgentWorkMaxChecks+1) },
		"foreign obligation": func(w *BotWorkState) {
			w.AgentWork.Remaining = []BotWorkObligation{{ID: uuid.New(), SourceRunID: uuid.New(), Summary: "Finish", Reason: "Missing", Continuation: BotWorkContinueOwnerRequest}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := base.Clone()
			mutate(w)
			if w.Validate() == nil {
				t.Fatal("invalid agent work accepted")
			}
		})
	}
	next := base.Clone()
	next.AgentWork.Checks[0].Summary = "rewritten"
	if ValidateBotWorkUpdate(base, next) == nil {
		t.Fatal("check history rewritten")
	}
	next = base.Clone()
	next.AgentWork.Evidence = nil
	next.AgentWork.SourceAccess = nil
	if ValidateBotWorkUpdate(base, next) == nil {
		t.Fatal("evidence history removed")
	}
}

func TestBotAgentWorkCheckSurvivesDraftCorrection(t *testing.T) {
	w := NewBotAgentWorkState(attachmentWorkContract())
	w.Candidate = &BotWorkCandidate{Revision: 1, EventID: "draft-1", Text: "First draft"}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w.AgentWork.Evidence = []BotWorkEvidenceReference{{Ref: "capture:source-1", RunID: w.Contract.RunID, EventID: "source-event", CallID: "source-call", Digest: strings.Repeat("a", 64)}}
	w.AgentWork.Checks = []BotWorkCheckReceipt{{ID: uuid.New(), CallID: "check-1", Kind: BotWorkCheckModelFeedback, Status: BotWorkCheckReturned, Judgment: BotWorkCheckNeedsWork, Summary: "Correct the draft", DraftDigest: w.Candidate.Digest, EvidenceRefs: []string{"capture:source-1"}, ObservedAt: time.Now().UTC()}}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	next := w.Clone()
	next.Candidate.Revision++
	next.Candidate.Text = "Corrected draft"
	next.Candidate.EventID = "draft-2"
	next.Candidate.Digest = next.Candidate.ContentDigest()
	if err := ValidateBotWorkUpdate(w, next); err != nil {
		t.Fatal("retained check prevents repair", err)
	}
	if next.AgentWork.Checks[0].DraftDigest != w.Candidate.Digest {
		t.Fatal("historical check retargeted")
	}
	bad := next.Clone()
	bad.AgentWork.Checks[0].EvidenceRefs = []string{"observation:0"}
	if bad.Validate() == nil {
		t.Fatal("positional evidence accepted")
	}
	bad = next.Clone()
	bad.AgentWork.Evidence[0].Digest = strings.Repeat("b", 64)
	if ValidateBotWorkUpdate(next, bad) == nil {
		t.Fatal("old capture retargeted")
	}
}

func TestBotWorkVerification(t *testing.T) {
	for _, kind := range []BotWorkVerificationKind{"", BotWorkVerificationModel, BotWorkVerificationSourceURL, BotWorkVerificationRetrievalTime, "unknown"} {
		w := NewBotWorkState(attachmentWorkContract())
		w.Phase = BotWorkProducing
		w.Criteria = []BotWorkCriterion{{ID: "c", SourceIDs: []string{"assignment", "charter"}, Text: "Requirement", VerificationKind: kind}}
		if (w.Validate() == nil) != (kind != "unknown") {
			t.Fatalf("kind validation %q", kind)
		}
		if kind == "unknown" {
			continue
		}
		next := w.Clone()
		next.Phase = BotWorkBlocked
		next.Unresolved = []string{"blocked"}
		next.Criteria[0].VerificationKind = BotWorkVerificationSourceURL
		if kind == BotWorkVerificationSourceURL {
			next.Criteria[0].VerificationKind = BotWorkVerificationModel
		}
		if ValidateBotWorkUpdate(w, next) == nil {
			t.Fatal("verification policy mutated after extraction")
		}
	}
	if (BotWorkCriterion{}).EffectiveVerificationKind() != BotWorkVerificationModel {
		t.Fatal("legacy default changed")
	}
}
