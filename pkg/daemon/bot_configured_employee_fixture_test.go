package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// configuredFixtureInteractiveActions admits one private action per owner turn.
// Exact-action resumes terminate their run; subsequent actions are new turns, not
// a replay of the private script or a grant to the rest of its capabilities.
func configuredFixtureInteractiveActions(t *testing.T, ctx context.Context, worker *BotWorker, repo store.BotStore, b *store.Bot, run store.BotRun, scope store.BotScope, m *configuredAdmissionModel, actions []configuredAdmissionCall, post func(string, any, int) []byte) (*store.BotRun, int32) {
	t.Helper()
	var saved *store.BotRun
	for i, action := range actions {
		if i > 0 {
			m.mu.Lock()
			m.script = []configuredAdmissionCall{action}
			m.step, m.delivery, m.employeeStarted = 0, false, false
			m.mu.Unlock()
			var receipt store.BotMessageReceipt
			if err := json.Unmarshal(post("/bots/"+b.ID.String()+"/messages?scope="+string(scope), map[string]any{"version": b.Version, "idempotency_key": fmt.Sprintf("interactive-action-%d", i), "instructions": "Execute the next exact configured fixture action and reply in chat only."}, 202), &receipt); err != nil {
				t.Fatal(err)
			}
			run = receipt.Run
			if err := worker.Poll(ctx); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		saved, err = repo.GetRun(ctx, b.ID, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		approvals, err := repo.ListApprovals(ctx, b.ID, store.BotPage{})
		if err != nil {
			t.Fatal(err)
		}
		var pending *store.BotApproval
		for j := range approvals {
			if approvals[j].Status == store.ApprovalPending {
				pending = &approvals[j]
			}
		}
		expected, _ := json.Marshal(action.args)
		if saved.Status != store.RunWaitingApproval || pending == nil || string(pending.Action.Arguments) != string(expected) {
			t.Fatalf("interactive exact boundary action=%s run=%+v approvals=%+v", action.name, saved, approvals)
		}
		wantTool := action.name
		if action.name == "echo" {
			wantTool = "mcp:configured-fixture/echo"
		}
		if action.name == "a2a_configured_service" {
			wantTool = "a2a/" + action.name
		}
		if pending.Action.Tool != wantTool {
			t.Fatalf("exact tool %s want %s", pending.Action.Tool, wantTool)
		}
		checkpoint, err := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
		if err != nil || !checkpoint.Matches(*pending) {
			t.Fatalf("interactive checkpoint: %v", err)
		}
		body := map[string]any{"action_digest": pending.ActionDigest}
		if pending.Interaction != nil {
			answer := &store.BotInteractionResponse{QuestionID: pending.Interaction.QuestionID, Answer: "yes"}
			if err := pending.Interaction.ValidateResponse(answer); err != nil {
				t.Fatal(err)
			}
			body["response"] = answer
		}
		path := "/bots/" + b.ID.String() + "/approvals/" + pending.ID.String() + "/approve-and-continue?scope=" + string(scope)
		post(path, map[string]any{"action_digest": "altered"}, 409)
		post(path, body, 202)
		if err := worker.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		saved, err = repo.GetRun(ctx, b.ID, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		approvals, err = repo.ListApprovals(ctx, b.ID, store.BotPage{})
		if err != nil {
			t.Fatal(err)
		}
		consumed := false
		for _, approval := range approvals {
			if approval.ID == pending.ID {
				consumed = approval.Status == store.ApprovalConsumed
			}
		}
		if !consumed || (saved.Status != store.RunSucceeded && saved.Status != store.RunPartial && saved.Status != store.RunFailed) {
			t.Fatalf("interactive consume: run=%+v approvals=%+v", saved, approvals)
		}
		post(path, body, 409)
		if err := worker.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return saved, int32(2 * len(actions))
}

// configuredFixtureCall reports metered provider usage for orchestration as well
// as private execution; service dispatch is not free and does not attest effects.
func configuredFixtureCall(name string, args map[string]any) *model.LLMResponse {
	return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}
}

func TestConfiguredAdmissionEmployeeBookkeepingAndUsage(t *testing.T) {
	m := &configuredAdmissionModel{work: func() (*store.BotWorkState, error) { return &store.BotWorkState{Revision: 4}, nil }, script: []configuredAdmissionCall{{"read_file", map[string]any{"path": "/fixture"}}}}
	owner := &model.LLMRequest{Tools: map[string]any{"employee_chat": struct{}{}, "bot_work_update": struct{}{}}}
	for response, err := range m.GenerateContent(t.Context(), owner, false) {
		if err != nil || response == nil {
			t.Fatalf("bookkeeping: %v", err)
		}
		call := response.Content.Parts[0].FunctionCall
		if call == nil || call.Name != "bot_work_update" || call.Args["expected_revision"] != int64(4) || m.step != 0 {
			t.Fatalf("owner bookkeeping: %+v step=%d", call, m.step)
		}
		if response.UsageMetadata == nil || response.UsageMetadata.PromptTokenCount != 7 || response.UsageMetadata.CandidatesTokenCount != 3 {
			t.Fatalf("unmetered bookkeeping: %+v", response.UsageMetadata)
		}
	}
	for response, err := range m.GenerateContent(t.Context(), owner, false) {
		if err != nil || response == nil || response.Content.Parts[0].FunctionCall.Name != "employee_chat" || m.step != 0 || response.UsageMetadata == nil {
			t.Fatalf("dispatch: %+v %v", response, err)
		}
	}
}

// Fixture routing must mirror the production Employee/private-worker tool views.
// The Employee dispatch cannot consume the worker's exact action script.
func TestConfiguredAdmissionEmployeeMediation(t *testing.T) {
	m := &configuredAdmissionModel{script: []configuredAdmissionCall{{"read_file", map[string]any{"path": "/fixture"}}}}
	owner := &model.LLMRequest{Tools: map[string]any{"employee_chat": struct{}{}, "execute_tool": struct{}{}}}
	for response, err := range m.GenerateContent(t.Context(), owner, false) {
		if err != nil || response == nil || response.Content.Parts[0].FunctionCall == nil || response.Content.Parts[0].FunctionCall.Name != "employee_chat" || m.step != 0 {
			t.Fatalf("Employee consumed private action: %+v %v step=%d", response, err, m.step)
		}
	}
	worker := &model.LLMRequest{Tools: map[string]any{"execute_tool": struct{}{}}}
	for response, err := range m.GenerateContent(t.Context(), worker, false) {
		if err != nil || response == nil {
			t.Fatalf("worker: %v", err)
		}
		call := response.Content.Parts[0].FunctionCall
		if call == nil || call.Name != "execute_tool" || call.Args["name"] != "read_file" || m.step != 1 {
			t.Fatalf("private script not dispatched: %+v", call)
		}
	}
	// Final owner response is conversational, not another private execution.
	for response, err := range m.GenerateContent(t.Context(), owner, false) {
		if err != nil || response.Content.Parts[0].FunctionCall != nil || m.step != 1 {
			t.Fatalf("Employee replayed script: %+v %v", response, err)
		}
	}
	// Declarations can be the sole available tool view for controlled providers.
	m = &configuredAdmissionModel{}
	declared := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "employee_chat"}}}}}}
	for response, err := range m.GenerateContent(t.Context(), declared, false) {
		if err != nil || response.Content.Parts[0].FunctionCall == nil || response.Content.Parts[0].FunctionCall.Name != "employee_chat" {
			t.Fatalf("declared Employee tool ignored: %+v %v", response, err)
		}
	}
}

// Repair is a metered no-tool provider turn, never another private action.
func TestConfiguredAdmissionEmployeeRepairUsage(t *testing.T) {
	m := &configuredAdmissionModel{script: []configuredAdmissionCall{{"read_file", map[string]any{"path": "/fixture"}}}}
	r := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("BOT_EMPLOYEE_REPAIR_V1", "system")}}
	for response, err := range m.GenerateContent(t.Context(), r, false) {
		if err != nil || response == nil || response.Content.Parts[0].Text != `{"text":"durable controlled result"}` || response.UsageMetadata == nil || m.step != 0 {
			t.Fatalf("repair replay or invalid response: %+v %v step=%d", response, err, m.step)
		}
	}
	if m.usageTurns() != 1 {
		t.Fatalf("repair usage turns=%d", m.usageTurns())
	}
}
