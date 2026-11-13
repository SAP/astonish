package main

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func skillsTestPin() (*store.BotWorkState, store.BotRun, store.BotSkillRevision) {
	bot, run, skill := uuid.New(), uuid.New(), uuid.New()
	source := store.BotSkillProvenance{Level: store.BotSkillOwnerSpecified, BotID: bot, RunID: run, SessionID: uuid.New(), EventID: "owner-event", SourceDigest: strings.Repeat("a", 64), Quote: "Markdown", RecordedAt: time.Now().UTC()}
	rev := store.BotSkillRevision{SchemaVersion: 1, ID: uuid.New(), BotID: bot, SkillID: skill, Revision: 1, Responsibility: "weather", Expectations: []store.BotSkillExpectation{{ID: "format", Text: "Markdown", Provenance: source}}, Method: []string{"retrieve current data"}, Provenance: []store.BotSkillProvenance{source}, ChangeReason: "owner assignment"}
	rev.Digest = rev.ContentDigest()
	receipt := &store.BotSkillReceipt{SchemaVersion: 1, BotID: bot, RunID: run, SkillID: skill, Kind: store.BotSkillUnchanged, UsedRevision: 1, Digest: rev.Digest}
	r := store.BotRun{ID: run, BotID: bot, UserEventID: "owner-event", Report: store.BotReport{Skill: receipt}}
	w := &store.BotWorkState{Contract: store.BotWorkContract{BotID: bot, RunID: run, Skill: &store.BotSkillInvocation{SkillID: skill, Revision: 1, Digest: rev.Digest, Snapshot: rev}}, SkillState: &store.BotSkillRunState{Stage: store.BotSkillLearningCommitted, Receipt: receipt}}
	return w, r, rev
}
func TestSkillsAgentSaveDoesNotInventInitialInvocation(t *testing.T) {
	for _, mutation := range []string{"valid", "missing-receipt", "uncommitted", "invented-use", "foreign-run", "foreign-skill", "wrong-digest", "deferred", "no-method", "old-owner", "legacy"} {
		t.Run(mutation, func(t *testing.T) {
			_, r, revision := skillsTestPin()
			r.SessionID = revision.Expectations[0].Provenance.SessionID
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: r.BotID, RunID: r.ID, Version: 1, Instructions: "Current report", Charter: "Useful work"})
			r.Report.Skill.Kind = store.BotSkillCreated
			r.Report.Skill.UsedRevision = 0
			r.Report.Skill.CommittedRevision = revision.Revision
			switch mutation {
			case "missing-receipt":
				r.Report.Skill = nil
			case "uncommitted":
				r.Report.Skill.CommittedRevision = 0
			case "invented-use":
				r.Report.Skill.UsedRevision = 1
			case "foreign-run":
				r.Report.Skill.RunID = uuid.New()
			case "foreign-skill":
				r.Report.Skill.SkillID = uuid.New()
			case "wrong-digest":
				r.Report.Skill.Digest = strings.Repeat("f", 64)
			case "deferred":
				r.Report.Skill.Kind = store.BotSkillDeferred
			case "no-method":
				revision.Method = nil
				revision.Digest = revision.ContentDigest()
				r.Report.Skill.Digest = revision.Digest
			case "old-owner":
				r.UserEventID = "another-owner-event"
			case "legacy":
				w.Protocol = store.BotWorkProtocolLegacyAssessment
			}
			err := skillsAgentSaved(w, r, revision)
			if (err == nil) != (mutation == "valid") {
				t.Fatalf("save validation: %v", err)
			}
			if skillsPinned(w, r, revision) == nil {
				t.Fatal("save-only evidence certified actual reuse")
			}
			if w.Contract.Skill != nil {
				t.Fatal("validator invented a skill invocation")
			}
		})
	}
}

func TestSkillsAgentUnchangedCommitDoesNotRequireNewRevision(t *testing.T) {
	w, r, revision := skillsTestPin()
	w.Protocol = store.BotWorkProtocolAgentLedV1
	r.Report.Skill.CommittedRevision = r.Report.Skill.UsedRevision
	if err := skillsPinned(w, r, revision); err != nil {
		t.Fatal(err)
	}
	w.Protocol = store.BotWorkProtocolLegacyAssessment
	if skillsPinned(w, r, revision) == nil {
		t.Fatal("changed legacy acceptance")
	}
	w.Protocol = store.BotWorkProtocolAgentLedV1
	r.Report.Skill.Kind = store.BotSkillUpdated
	if skillsPinned(w, r, revision) == nil {
		t.Fatal("an update must advance revision")
	}
}

func TestSkillsTestPinValidRevision(t *testing.T) {
	_, _, revision := skillsTestPin()
	if err := revision.Validate(); err != nil {
		t.Fatalf("invalid shared skill fixture: %v", err)
	}
	raw, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	var decoded store.BotSkillRevision
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("invalid endpoint fixture after JSON round trip: %v", err)
	}
	if decoded.Digest != revision.Digest || decoded.Expectations[0].Text != decoded.Expectations[0].Provenance.Quote {
		t.Fatal("fixture lost digest or exact owner quote")
	}
}

func TestSkillsPinnedRejectsMissingForeignMismatchedReplayed(t *testing.T) {
	w, r, rev := skillsTestPin()
	if err := skillsPinned(w, r, rev); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*store.BotWorkState, *store.BotRun, *store.BotSkillRevision){
		func(w *store.BotWorkState, r *store.BotRun, v *store.BotSkillRevision) { w.Contract.Skill = nil },
		func(w *store.BotWorkState, r *store.BotRun, v *store.BotSkillRevision) { r.ID = uuid.New() },
		func(w *store.BotWorkState, r *store.BotRun, v *store.BotSkillRevision) { r.BotID = uuid.New() },
		func(w *store.BotWorkState, r *store.BotRun, v *store.BotSkillRevision) {
			w.Contract.Skill.Digest = "wrong"
		},
		func(w *store.BotWorkState, r *store.BotRun, v *store.BotSkillRevision) { w.Contract.Skill.Revision++ },
		func(w *store.BotWorkState, r *store.BotRun, v *store.BotSkillRevision) {
			r.Report.Skill.Kind = store.BotSkillDeferred
		},
		func(w *store.BotWorkState, r *store.BotRun, v *store.BotSkillRevision) {
			w.SkillState.Stage = store.BotSkillLearningStaged
		},
	} {
		w, r, rev := skillsTestPin()
		mutate(w, &r, &rev)
		if skillsPinned(w, r, rev) == nil {
			t.Fatal("accepted bad pin")
		}
	}
}
func TestSkillsReceiptBindsSourceAndRejectsDuplicates(t *testing.T) {
	p := &skillsProbe{root: t.TempDir(), home: "owned", state: map[string]any{"skills_nonce": "fresh", "binary_sha256": "candidate", "browser_sha256": "browser", "bot_id": "bot"}, receipts: map[string]map[string]any{}}
	p.receipt(skillsRequired[0], map[string]any{"endpoint": "actual"}, "observed")
	raw, err := os.ReadFile(filepath.Join(p.root, "skills-"+skillsRequired[0]+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["nonce"] != "fresh" || doc["gate"] != skillsRequired[0] || doc["candidate"] != "candidate" {
		t.Fatal(doc)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate accepted")
		}
	}()
	p.receipt(skillsRequired[0], doc, "replay")
}
func TestSkillsInspectionRejectsForeignDuplicateAndMissing(t *testing.T) {
	_, run, head := skillsTestPin()
	history := store.BotSkillRevisionList{Revisions: []store.BotSkillRevision{head}}
	runs := store.BotSkillRunList{Runs: []store.BotRun{run}}
	if err := skillsInspection(head, history, runs, run.ID); err != nil {
		t.Fatal(err)
	}
	if skillsInspection(head, store.BotSkillRevisionList{}, runs, run.ID) == nil {
		t.Fatal("missing head")
	}
	if skillsInspection(head, history, runs, uuid.New()) == nil {
		t.Fatal("missing run")
	}
	if skillsInspection(head, store.BotSkillRevisionList{Revisions: []store.BotSkillRevision{head, head}}, runs, run.ID) == nil {
		t.Fatal("duplicate revision")
	}
	if skillsInspection(head, history, store.BotSkillRunList{Runs: []store.BotRun{run, run}}, run.ID) == nil {
		t.Fatal("duplicate run")
	}
	runs.Runs[0].BotID = uuid.New()
	if skillsInspection(head, history, runs, run.ID) == nil {
		t.Fatal("foreign run")
	}
}

func TestSkillsPersistedOrientationDoesNotRequireInventedLearning(t *testing.T) {
	w, run, revision := skillsTestPin()
	w.SkillState.Stage = store.BotSkillOriented
	run.Report.Skill.Kind = store.BotSkillSelected
	if err := skillsPinned(w, run, revision); err != nil {
		t.Fatal(err)
	}
	run.Report.Skill.Digest = "wrong"
	if skillsPinned(w, run, revision) == nil {
		t.Fatal("unchanged digest mismatch")
	}
}

func TestSkillsAgentWorkNoLegacyReview(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Current report", Charter: "Useful work"})
	w.Phase = store.BotWorkFinished
	w.AgentWork.Disposition = store.AgentWorkComplete
	w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "answer", Text: "Fresh result"}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	at := started.Add(time.Second)
	r := store.BotRun{ID: w.Contract.RunID, BotID: w.Contract.BotID, SessionID: uuid.New(), Status: store.RunSucceeded, CreatedAt: at, StartedAt: &at, FinishedAt: &at, Work: store.BuildBotWorkProjection(w), Report: store.BotReport{Summary: "Fresh result"}}
	p := &skillsProbe{agent: true, startedAt: started, state: map[string]any{"bot_id": r.BotID.String()}}
	decode := func() error {
		raw, err := json.Marshal(map[string]any{"run": r, "work": w})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = p.decodeWork(employeeDiskRow{ID: r.ID.String(), BotID: r.BotID.String(), Data: string(raw)})
		return err
	}
	if err := decode(); err != nil {
		t.Fatal(err)
	}
	p.startedAt = at.Add(time.Second)
	if decode() == nil {
		t.Fatal("accepted historical run")
	}
	p.startedAt = started
	w.Review = &store.BotWorkReview{}
	if decode() == nil {
		t.Fatal("accepted invented legacy review")
	}
	w.Review = nil
	r.Work.WorkRevision++
	if decode() == nil {
		t.Fatal("accepted mismatched projection")
	}
}

func TestSkillsAgentOccurrencesRequireDistinctSessionsAndKeys(t *testing.T) {
	runs := []store.BotRun{}
	for n := 0; n < 3; n++ {
		runs = append(runs, store.BotRun{ID: uuid.New(), SessionID: uuid.New(), IdempotencyKey: uuid.NewString()})
	}
	if err := skillsDistinctOccurrences(runs); err != nil {
		t.Fatal(err)
	}
	if skillsDistinctOccurrences(runs[:2]) == nil {
		t.Fatal("two occurrences accepted")
	}
	for _, field := range []string{"run", "session", "key", "missing-key"} {
		bad := append([]store.BotRun{}, runs...)
		switch field {
		case "run":
			bad[1].ID = bad[0].ID
		case "session":
			bad[1].SessionID = bad[0].SessionID
		case "key":
			bad[1].IdempotencyKey = bad[0].IdempotencyKey
		case "missing-key":
			bad[1].IdempotencyKey = ""
		}
		if skillsDistinctOccurrences(bad) == nil {
			t.Fatal("accepted duplicate", field)
		}
	}
}

func TestSkillsAgentCaptureUsesEachActualWorkerStream(t *testing.T) {
	root := t.TempDir()
	p := &skillsProbe{root: root, botPath: "/api/studio/bots/owned", agent: true, state: map[string]any{"agent_nonce": "fresh"}, captures: map[uuid.UUID]string{}, receipts: map[string]map[string]any{}}
	calls := []string{}
	bot := store.Bot{ID: uuid.New(), OwnerID: uuid.New(), Scope: store.BotScopePersonal}
	p.api = func(method, path string, body any) string {
		if method != "GET" || body != nil {
			t.Fatal("mutating observer")
		}
		calls = append(calls, path)
		if path == p.botPath+"?scope=personal" {
			raw, _ := json.Marshal(bot)
			return string(raw)
		}
		return `{"messages":[{"type":"tool_call","toolName":"web_fetch","toolId":"source"},{"type":"tool_result","toolName":"web_fetch","toolId":"source","toolResult":{"status":200,"url":"https://weather.example/current","content":"Current measured weather report payload"}}]}`
	}
	for n := 0; n < 3; n++ {
		r := store.BotRun{ID: uuid.New(), BotID: bot.ID, SessionID: uuid.New()}
		p.captureRun(r)
		p.captureRun(r) // readback cannot replace receipt-bound source files
		if calls[2*n] != p.botPath+"/sessions/"+r.SessionID.String()+"/stream?scope=personal" {
			t.Fatal(calls)
		}
		raw, err := os.ReadFile(filepath.Join(root, "agent-skills-run-"+r.ID.String()+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var capture struct {
			Bot store.Bot `json:"bot"`
		}
		if err := json.Unmarshal(raw, &capture); err != nil || capture.Bot.ID != bot.ID || capture.Bot.OwnerID != bot.OwnerID {
			t.Fatal("missing retained Bot ownership", err)
		}
	}
	if len(calls) != 6 || len(p.captures) != 3 {
		t.Fatal("missing independent capture")
	}
	p.receipt("reuse-pinned-skill", "actual", "observed")
	if p.receipts["reuse-pinned-skill"]["Nonce"] != "fresh" || p.receipts["reuse-pinned-skill"]["Source"] != "agent-skills-reuse-pinned-skill.json" {
		t.Fatal(p.receipts)
	}
}

func TestSkillsAgentFinishRejectsPartialAndDoesNotCertifyGrounding(t *testing.T) {
	p := &skillsProbe{agent: true, state: map[string]any{"agent_nonce": "fresh"}, receipts: map[string]map[string]any{}, captures: map[uuid.UUID]string{}}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("partial evidence accepted")
			}
		}()
		p.finishAgent()
	}()
	for _, key := range skillsRequired {
		p.receipts[key] = map[string]any{"Status": "passed"}
	}
	for n := 0; n < 7; n++ {
		p.captures[uuid.New()] = "digest"
	}
	evidence := p.finishAgent()
	if evidence["contract"] != agentSkillsContract || evidence["grounding_verified"] != false || evidence["nonce"] != "fresh" {
		t.Fatal(evidence)
	}
}

func TestSkillsAgentCaptureRejectsForeignBotOwner(t *testing.T) {
	for _, bad := range []store.Bot{
		{ID: uuid.New(), OwnerID: uuid.New(), Scope: store.BotScopePersonal},
		{OwnerID: uuid.Nil, Scope: store.BotScopePersonal},
		{OwnerID: uuid.New(), Scope: store.BotScopeTeam},
	} {
		t.Run(string(bad.Scope)+bad.ID.String(), func(t *testing.T) {
			r := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New()}
			if bad.ID == uuid.Nil {
				bad.ID = r.BotID
			}
			p := &skillsProbe{root: t.TempDir(), agent: true, api: func(_, path string, _ any) string {
				if path == "?scope=personal" {
					raw, _ := json.Marshal(bad)
					return string(raw)
				}
				return `{"messages":[]}`
			}}
			defer func() {
				if recover() == nil {
					t.Error("foreign capture accepted")
				}
				if len(p.captures) != 0 {
					t.Error("foreign capture certified")
				}
			}()
			p.captureRun(r)
		})
	}
}

func TestSkillsAgentFailedRetrievalRetainsCaptureWithoutReceipt(t *testing.T) {
	r := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New()}
	p := &skillsProbe{root: t.TempDir(), agent: true, api: func(_, path string, _ any) string {
		if path == "?scope=personal" {
			raw, _ := json.Marshal(store.Bot{ID: r.BotID, OwnerID: uuid.New(), Scope: store.BotScopePersonal})
			return string(raw)
		}
		return `{"messages":[]}`
	}}
	defer func() {
		if recover() == nil {
			t.Error("missing retrieval accepted")
		}
		if len(p.captures) != 0 {
			t.Error("failed retrieval produced passed capture")
		}
		if _, err := os.Stat(filepath.Join(p.root, "agent-skills-run-"+r.ID.String()+".json")); err != nil {
			t.Error("failed output not retained", err)
		}
	}()
	p.captureRun(r)
}

func TestSkillsAgentRejectsMissingFreshIdentityAndLifecycle(t *testing.T) {
	for _, state := range []map[string]any{nil, {"workflow": agentLiveContract}, {"workflow": agentLiveContract, "agent_learning_attempted": true}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("accepted reusable/missing identity")
				}
			}()
			startAgentSkills("", "", state, nil, nil, nil, nil)
		}()
	}
	defer func() {
		if recover() == nil {
			t.Error("accepted incomplete lifecycle")
		}
	}()
	runAgentSkillsJourney("", "", nil, nil, agentSkillsJourneyHooks{})
}

func TestSkillsFinishRequiresQualityAndAllReceipts(t *testing.T) {
	for _, evidence := range []map[string]any{{"contract": employeeContract}, {"contract": qualityContract}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("incomplete accepted")
				}
			}()
			p := &skillsProbe{receipts: map[string]map[string]any{}}
			p.finish(evidence)
		}()
	}
}
