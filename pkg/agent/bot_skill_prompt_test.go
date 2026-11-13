package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Each independently bounded input is exactly at its accepted JSON byte limit.
// This catches combined prompt caps that are smaller than the persisted contract.
func botSkillPromptFixture(t *testing.T) bot.RunRequest {
	t.Helper()
	id, runID := uuid.New(), uuid.New()
	provenance := store.BotSkillProvenance{Level: store.BotSkillOwnerSpecified, BotID: id, RunID: runID, SessionID: uuid.New(), EventID: "owner-message", SourceDigest: strings.Repeat("a", 64), Quote: "Include observations and limitations", RecordedAt: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	revision := store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, ID: uuid.New(), BotID: id, SkillID: uuid.New(), Revision: 1, Responsibility: "Weather report", Expectations: []store.BotSkillExpectation{{ID: "limitations", Text: "Include observations and limitations", Provenance: provenance}}, Method: []string{"Inspect the actual weather source", "METHOD-START--METHOD-END"}, ChangeReason: "Initial owner assignment", Provenance: []store.BotSkillProvenance{provenance}}
	revision.Digest = revision.ContentDigest()
	encoded, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	revision.Method[1] = "METHOD-START-" + strings.Repeat("m", store.BotSkillMaxRevisionBytes-len(encoded)) + "-METHOD-END"
	revision.Digest = revision.ContentDigest()
	if err := revision.Validate(); err != nil {
		t.Fatalf("invalid skill fixture: %v", err)
	}
	contract := store.BotWorkContract{BotID: id, RunID: runID, Version: 1, Instructions: "ASSIGNMENT-START--ASSIGNMENT-END", Charter: "Give a useful truthful report"}
	encoded, _ = json.Marshal(contract)
	contract.Instructions = "ASSIGNMENT-START-" + strings.Repeat("a", store.BotWorkMaxContractBytes-len(encoded)) + "-ASSIGNMENT-END"
	contract.Skill = &store.BotSkillInvocation{Intent: "Produce the weather report", SkillID: revision.SkillID, Revision: revision.Revision, Digest: revision.Digest, Snapshot: revision}
	work := store.NewBotWorkState(contract)
	work.SkillState = &store.BotSkillRunState{Stage: store.BotSkillOriented}
	work.Phase = store.BotWorkProducing
	work.Criteria = []store.BotWorkCriterion{{ID: "limitations", Text: "CRITERIA-START--CRITERIA-END", SourceIDs: []string{"assignment", "charter"}}}
	encoded, _ = json.Marshal(work.Criteria)
	work.Criteria[0].Text = "CRITERIA-START-" + strings.Repeat("c", store.BotWorkMaxContractBytes-len(encoded)) + "-CRITERIA-END"
	work.Unresolved = []string{"UNRESOLVED-START--UNRESOLVED-END"}
	encoded, _ = json.Marshal(work.Unresolved)
	work.Unresolved[0] = "UNRESOLVED-START-" + strings.Repeat("u", store.BotWorkMaxContractBytes-len(encoded)) + "-UNRESOLVED-END"
	if err := work.Validate(); err != nil {
		t.Fatalf("invalid work fixture: %v", err)
	}
	return bot.RunRequest{Bot: store.Bot{ID: id, Name: "Orbit"}, Version: store.BotVersion{Version: 1}, Run: store.BotRun{ID: runID}, Work: work, Instructions: "Continue the pinned assignment"}
}

func TestBotSkillPromptFixtureBounds(t *testing.T) {
	req := botSkillPromptFixture(t)
	base := req.Work.Contract
	base.Skill = nil
	for name, tc := range map[string]struct {
		value any
		limit int
	}{
		"skill":      {req.Work.Contract.Skill.Snapshot, store.BotSkillMaxRevisionBytes},
		"assignment": {base, store.BotWorkMaxContractBytes},
		"criteria":   {req.Work.Criteria, store.BotWorkMaxContractBytes},
		"unresolved": {req.Work.Unresolved, store.BotWorkMaxContractBytes},
	} {
		encoded, err := json.Marshal(tc.value)
		if err != nil || len(encoded) != tc.limit {
			t.Fatalf("%s bytes=%d, want %d; err=%v", name, len(encoded), tc.limit, err)
		}
	}
}

func TestBotSkillPromptCompleteProducerContext(t *testing.T) {
	req := botSkillPromptFixture(t)
	service := session.InMemoryService()
	llm := &ADKMockModel{Responses: []*genai.Content{genai.NewContentFromText("done", genai.RoleModel)}}
	chat := NewChatAgent(llm, nil, nil, service, &SystemPromptBuilder{}, false, false)
	root, err := adkagent.New(adkagent.Config{Name: "chat", Run: chat.Run})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "bot-skill-prompt", Agent: root, SessionService: service, AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPromptOverrides(context.Background(), NewBotPromptOverrides(nil, req))
	for _, err := range r.Run(ctx, "owner", "producer", genai.NewContentFromText(req.Instructions, genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(llm.Requests) != 1 {
		t.Fatalf("producer model calls=%d, want 1", len(llm.Requests))
	}
	var prompt strings.Builder
	for _, part := range llm.Requests[0].Config.SystemInstruction.Parts {
		prompt.WriteString(part.Text)
	}
	if strings.Contains(prompt.String(), "unavailable or oversized") {
		t.Fatal("valid separately bounded skill, assignment and criteria were omitted from the producer prompt")
	}
	for name, value := range map[string]any{"skill": req.Work.Contract.Skill, "assignment": req.Work.Contract.Instructions, "criteria": req.Work.Criteria, "unresolved": req.Work.Unresolved} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(prompt.String(), EscapeCurlyPlaceholders(string(encoded))) != 1 {
			t.Errorf("producer prompt must contain exact complete %s once (%d JSON bytes)", name, len(encoded))
		}
	}
}

func TestBotSkillPromptInvalidContextUnavailable(t *testing.T) {
	for _, name := range []string{"skill-too-large", "assignment-too-large", "blocked-assignment-too-large", "interrupted-assignment-too-large", "criteria-too-large", "unresolved-too-large", "skill-tampered", "skill-foreign"} {
		t.Run(name, func(t *testing.T) {
			req := botSkillPromptFixture(t)
			switch name {
			case "skill-too-large":
				req.Work.Contract.Skill.Snapshot.Method[1] += "x"
				req.Work.Contract.Skill.Snapshot.Digest = req.Work.Contract.Skill.Snapshot.ContentDigest()
				req.Work.Contract.Skill.Digest = req.Work.Contract.Skill.Snapshot.Digest
			case "assignment-too-large", "blocked-assignment-too-large", "interrupted-assignment-too-large":
				req.Work.Contract.Instructions += "x"
				if name == "blocked-assignment-too-large" {
					req.Work.Phase = store.BotWorkBlocked
				}
				if name == "interrupted-assignment-too-large" {
					req.Work.Phase = store.BotWorkInterrupted
				}
			case "criteria-too-large":
				req.Work.Criteria[0].Text += "x"
			case "unresolved-too-large":
				req.Work.Unresolved[0] += "x"
			case "skill-tampered":
				req.Work.Contract.Skill.Snapshot.Method[0] = "tampered"
			case "skill-foreign":
				req.Work.Contract.Skill.Snapshot.BotID = uuid.New()
			}
			// Recompute only the outer pin digest, so each case exercises its own
			// bound/integrity check rather than an incidental stale contract hash.
			req.Work.RequirementsDigest = store.NewBotWorkState(req.Work.Contract).RequirementsDigest
			builder := &SystemPromptBuilder{}
			builder.applyBotPrompt(NewBotPromptOverrides(nil, req))
			prompt := builder.Build()
			if !strings.Contains(prompt, "unavailable or oversized") || strings.Contains(prompt, "METHOD-START") || strings.Contains(prompt, "CRITERIA-START") {
				t.Fatal("invalid context must be explicitly unavailable, not partially rendered")
			}
		})
	}
}
