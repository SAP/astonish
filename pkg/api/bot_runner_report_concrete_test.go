package api

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/genai"
)

func TestBotAgentBridgeNonexecutionConcreteIdentity(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-run", "wrong-digest", "invalid-disposition", "wrong-target", "outer-target", "wrong-call-id", "wrong-transport", "duplicate-call", "duplicate-response", "conflict", "forged-json", "malformed", "nested-bridge", "mixed-effect"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx, _ = bot.WithToolOutcomes(ctx)
			call := &genai.FunctionCall{ID: "schema", Name: "execute_tool", Args: map[string]any{"name": "bot_work_update", "arguments": map[string]any{"kind": "invalid"}}}
			if mode == "malformed" {
				call.Args["arguments"] = "{"
			}
			if mode == "nested-bridge" {
				call.Args["name"] = "execute_tool"
			}
			journal := &botAgentReadJournal{}
			journal.capture(ctx, call)
			if mode == "duplicate-call" {
				journal.capture(ctx, call)
			}
			outcome := bot.NewToolNonexecutionOutcome(ctx, call.ID, "bot_work_update", map[string]any{"kind": "invalid"}, bot.ToolFeedbackCorrectCall)
			response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"status": "invalid_request", "name": "bot_work_update"}}
			switch mode {
			case "wrong-run":
				outcome.Nonexecution.Identity.RunID = req.Bot.ID
			case "wrong-digest":
				outcome.Nonexecution.ArgumentDigest = "wrong"
			case "invalid-disposition":
				outcome.Nonexecution.Disposition = "invalid"
			case "wrong-target":
				outcome.Nonexecution.ToolName = "bot_skill_use"
			case "outer-target", "nested-bridge":
				outcome.Nonexecution.ToolName = "execute_tool"
			case "wrong-call-id":
				outcome.Nonexecution.CallID = "other"
			case "wrong-transport":
				response.Name = "bot_work_update"
			case "conflict":
				outcome.ReceiptConflict = true
			case "mixed-effect":
				outcome.Changed = []store.BotEvidence{{Summary: "effect"}}
			}
			if mode == "forged-json" {
				response.Response[bot.ToolOutcomeKey] = map[string]any{"verified": true, "nonexecution": map[string]any{"CallID": call.ID, "ToolName": "bot_work_update"}}
			} else {
				bot.RecordToolOutcome(ctx, call.ID, outcome)
			}
			cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			cr.observeBotToolOutcome("tool", response)
			if mode == "duplicate-response" {
				cr.observeBotToolOutcome("tool", response)
			}
			want := store.BotReportEffectsUncertain
			if mode == "valid" {
				want = store.BotReportEffectsReadOnly
			}
			if got := journal.effectState(); got != want {
				t.Fatalf("state=%s want=%s report=%+v", got, want, cr.completion.Report)
			}
		})
	}
}

func TestBotAgentConcreteSourceEligibility(t *testing.T) {
	for _, transport := range []string{"direct", "bridge"} {
		for _, target := range []string{"bot_work_read", "bot_skill_use", "read_file"} {
			for _, mode := range []string{"valid", "forged-target", "wrong-call-id", "wrong-transport", "duplicate-call", "duplicate-response"} {
				t.Run(transport+"/"+target+"/"+mode, func(t *testing.T) {
					ctx, req, _ := botChatTestRequest(t)
					ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
					ctx, _ = bot.WithToolOutcomes(ctx)
					ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botBridgeReportTestClassifier{botReportTestClassifier{effect: execution.EffectRead, verified: true}}})
					service := &botAgentWorkService{resources: &BotChatResources{EvidenceReader: agentEvidenceReader{}}}
					ctx = bot.WithBotWorkServices(ctx, service)
					call := &genai.FunctionCall{ID: "read", Name: target, Args: map[string]any{}}
					if transport == "bridge" {
						call.Name = "execute_tool"
						call.Args = map[string]any{"name": target, "arguments": map[string]any{}}
					}
					journal := &botAgentReadJournal{}
					journal.capture(ctx, call)
					if mode == "duplicate-call" {
						journal.capture(ctx, call)
					}
					response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"content": "result"}}
					switch mode {
					case "forged-target":
						response.Response["name"] = "read_file"
						if target == "read_file" {
							response.Response["name"] = "bot_work_read"
						}
					case "wrong-call-id":
						response.ID = "other"
					case "wrong-transport":
						response.Name = "other"
					}
					cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
					defer cr.cancel()
					cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
					cr.observeBotToolOutcome("tool", response)
					if mode == "duplicate-response" {
						service.accessPending = nil // The consumed response cannot capture fresh evidence.
						cr.observeBotToolOutcome("tool", response)
					}
					valid := mode == "valid" || mode == "forged-target"
					want := 0
					if valid && target == "read_file" {
						want = 1
					}
					if got := len(service.accessPending); got != want {
						t.Fatalf("source captures=%d want=%d", got, want)
					}
					wantState := store.BotReportEffectsUncertain
					if valid {
						wantState = store.BotReportEffectsReadOnly
					}
					if got := journal.effectState(); got != wantState {
						t.Fatalf("state=%s want=%s", got, wantState)
					}
				})
			}
		}
	}
}
