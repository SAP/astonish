package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"testing"
	"time"
)

func publicFetchOutcome(ctx context.Context, id string, args, response map[string]any) bot.ToolOutcome {
	raw, _ := json.Marshal(response)
	sum := sha256.Sum256(raw)
	status := store.BotSourceAccessReturned
	if response["error"] != nil {
		status = store.BotSourceAccessFailed
	}
	return bot.ToolOutcome{SourceRead: bot.NewToolSourceReadReceipt(ctx, id, "web_fetch", args, response), SourceAccess: []store.BotSourceAccessFact{{CallID: id, EvidenceRef: "capture:" + id, OriginalURL: args["url"].(string), Status: status, ObservedAt: time.Now(), BodyDigest: hex.EncodeToString(sum[:])}}}
}

func TestBotOrdinaryPublicFetchRecoveredFinal(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Read sources"})
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectUnknown}, Guard: botChatTestGuard{}})
	ctx, _ = bot.WithToolOutcomes(ctx)
	journal := &botAgentReadJournal{}
	ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	cr.ctx = ctx
	for _, id := range []string{"failed", "successful"} {
		args := map[string]any{"url": "https://example.test/" + id}
		call := &genai.FunctionCall{ID: id, Name: "execute_tool", Args: map[string]any{"name": "web_fetch", "arguments": args}}
		journal.capture(ctx, call)
		cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}})
		response := map[string]any{"status": float64(200), "final_url": args["url"], "content": "Useful alternative evidence"}
		if id == "failed" {
			response = map[string]any{"error": "fetch failed: connection closed"}
		}
		bot.RecordToolOutcome(ctx, id, publicFetchOutcome(ctx, id, args, response))
		cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: id, Name: call.Name, Response: response}}}}}})
	}
	answer := "The primary source was unavailable. The alternate source supports this briefing."
	cr.observeCompletion(&session.Event{ID: "final", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(answer, "model")}})
	c := cr.Completion()
	if c.FinalText != answer || c.Report.EffectState != store.BotReportEffectsReadOnly || len(c.Report.Unverified) != 0 || (c.Err != nil && !botCanRetainReadOnlyFinal(c, req, botCompletionReport(c, req))) {
		t.Fatalf("recovered final vetoed: %+v", c)
	}
}

func TestBotOrdinaryPublicFetchReceiptBoundary(t *testing.T) {
	for _, mode := range []string{"valid", "call", "tool", "args", "identity", "response", "conflict", "changed", "unverified", "nonexecution", "missing", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectUnknown}})
			j := &botAgentReadJournal{}
			args := map[string]any{"url": "https://example.test/source"}
			call := &genai.FunctionCall{ID: "read", Name: "execute_tool", Args: map[string]any{"name": "web_fetch", "arguments": args}}
			j.capture(ctx, call)
			response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"error": "connection closed"}}
			o := publicFetchOutcome(ctx, call.ID, args, response.Response)
			switch mode {
			case "call":
				o.SourceRead.CallID = "other"
			case "tool":
				o.SourceRead.ToolName = "shell_command"
			case "args":
				o.SourceRead.ArgumentDigest = "other"
			case "identity":
				o.SourceRead.Identity.RunID = req.Bot.ID
			case "response":
				response.Response["error"] = "changed"
			case "conflict":
				o.ReceiptConflict = true
			case "changed":
				o.Changed = []store.BotEvidence{{Summary: "write"}}
			case "unverified":
				o.Unverified = []string{"unknown"}
			case "nonexecution":
				o.Nonexecution = bot.NewToolNonexecutionOutcome(ctx, call.ID, "web_fetch", args, bot.ToolFeedbackTerminal).Nonexecution
			case "missing":
				o.SourceRead = nil
			case "duplicate":
				j.capture(ctx, call)
			}
			read, _ := j.take(response, o)
			want := mode == "valid"
			if (j.effectState() == store.BotReportEffectsReadOnly) != want || (want && !read) {
				t.Fatalf("unsafe disposition: read=%v state=%v", read, j.effectState())
			}
		})
	}
}
