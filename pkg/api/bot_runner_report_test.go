package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type botReportTestClassifier struct {
	effect   execution.ActionEffect
	verified bool
	mutate   bool
}

func (c botReportTestClassifier) DescribeAction(_ context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return execution.ActionDescriptor{}, err
	}
	a := execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: c.effect, ConstraintsVerified: c.verified}
	// A read-looking tool can write depending on its actual arguments.
	if args["operation"] == "send" {
		a.Effect = execution.EffectSend
	}
	if c.mutate {
		a.Arguments = json.RawMessage(`{}`)
	}
	return a, nil
}

func TestBotAgentSourceFailureIsInformationNotReportVeto(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx, _ = bot.WithToolOutcomes(ctx)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}})
	journal := &botAgentReadJournal{}
	call := &genai.FunctionCall{ID: "limited", Name: "web_fetch", Args: map[string]any{"url": "https://example.test"}}
	journal.capture(ctx, call)
	ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
	response := map[string]any{"error": "rate limited", "status_code": 429}
	raw, _ := json.Marshal(response)
	bot.RecordToolOutcome(ctx, call.ID, bot.ToolOutcome{Verified: false, Unverified: []string{"source unavailable"}, SourceAccess: []store.BotSourceAccessFact{{CallID: call.ID, EvidenceRef: "capture:" + call.ID, OriginalURL: "https://example.test", Status: store.BotSourceAccessFailed, HTTPStatus: 429, ObservedAt: time.Now().UTC(), BodyDigest: botAgentDigest(raw)}}})
	cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
	defer cr.cancel()
	cr.ctx = ctx
	cr.observeBotToolOutcome("tool", &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: response})
	if len(cr.completion.Report.Unverified) != 0 || len(cr.completion.Report.Changed) != 0 || len(cr.completion.reportBodies) != 0 {
		t.Fatalf("source promoted to veto/effect or body duplicated: %+v", cr.completion)
	}
}

func TestBotAgentReadOutcome(t *testing.T) {
	for _, tc := range []struct {
		name                string
		classifier          agent.BotActionClassifier
		send, failedReceipt bool
		want                store.RunStatus
	}{
		{"ordinary-read", botReportTestClassifier{effect: execution.EffectRead, verified: true}, false, false, store.RunSucceeded},
		{"spoofed-send", botReportTestClassifier{effect: execution.EffectRead, verified: true}, true, false, store.RunPartial},
		{"unknown", botReportTestClassifier{effect: execution.EffectUnknown}, false, false, store.RunPartial},
		{"unconstrained-read", botReportTestClassifier{effect: execution.EffectRead}, false, false, store.RunPartial},
		{"missing-classifier", nil, false, false, store.RunPartial},
		{"changed-arguments", botReportTestClassifier{effect: execution.EffectRead, verified: true, mutate: true}, false, false, store.RunPartial},
		{"failed-receipt", botReportTestClassifier{effect: execution.EffectRead, verified: true}, false, true, store.RunPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			req.Work = &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1}
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = execution.WithRunPolicy(ctx, req.Policy)
			ctx = context.WithValue(ctx, botChatBudgetKey{}, req.Budget)
			ctx, _ = bot.WithToolOutcomes(ctx)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: tc.classifier, Guard: botChatTestGuard{}})
			var callID string
			action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled fixture"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
				if tc.name == "ordinary-read" {
					if err := agent.AuthorizeBotTool(c, "read_file", args); err != nil {
						return nil, err
					}
				}
				callID = c.FunctionCallID()
				if tc.failedReceipt {
					bot.RecordToolOutcome(c, callID, bot.ToolOutcome{Verified: false, Unverified: []string{"receipt failed"}})
				}
				return map[string]any{"content": "actual source text", bot.ToolOutcomeKey: map[string]any{"verified": true, "effect": "read", "constraints_verified": true}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"path": "fixture"}
			if tc.send {
				args["operation"] = "send"
			}
			if tc.name != "ordinary-read" && tc.name != "failed-receipt" {
				// Exercise response reporting independently of authorization: the
				// real boundary correctly refuses these unconstrained actions.
				journal := &botAgentReadJournal{}
				call := &genai.FunctionCall{ID: "call-1", Name: "read_file", Args: args}
				journal.capture(ctx, call)
				cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
				cr.cancel()
				cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
				cr.observeBotToolOutcome("tool", &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{bot.ToolOutcomeKey: map[string]any{"verified": true, "effect": "read", "constraints_verified": true}}})
				if len(cr.completion.Report.Unverified) == 0 || len(cr.completion.Report.Changed) != 0 || cr.completion.toolResponses != 1 {
					t.Fatalf("untrusted result accepted: %+v", cr.completion)
				}
				return
			}
			llm := NewMockLLM(ToolCallTurn("read_file", args), TextTurn("Actual source text observed."))
			var cleaned atomic.Int64
			resources, err := botChatTestFactory(t, ss, llm, []tool.Tool{action}, &cleaned)(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			existing, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
			if err != nil {
				t.Fatal(err)
			}
			var sequence int64
			var completion ChatCompletion
			result, err := runBotChatAttempt(ctx, req, resources, func(context.Context, bot.RuntimeEvent) error { return nil }, nil, existing, nil, false, &sequence, &completion)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.want {
				t.Fatalf("status=%s want=%s report=%+v", result.Status, tc.want, result.Report)
			}
			if completion.toolResponses != 1 || completion.Usage.ToolCalls != 1 || callID == "" {
				t.Fatalf("source/accounting lost: %+v", completion)
			}
			persisted, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for event := range persisted.Session.Events().All() {
				if event.Content == nil {
					continue
				}
				for _, part := range event.Content.Parts {
					if part.FunctionResponse != nil && part.FunctionResponse.ID == callID {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("source missing from durable transcript")
			}
			if len(result.Report.Changed) != 0 || len(result.Report.Artifacts) != 0 {
				t.Fatalf("response minted effect evidence: %+v", result.Report)
			}
			if resources.Agent.LLM != llm {
				t.Fatal("attempt wrapper escaped its request")
			}
		})
	}
}

func TestBotAgentReadOutcomeInvocationBinding(t *testing.T) {
	for _, scenario := range []string{"matching", "wrong-id", "wrong-name", "reused-id", "consumed", "model-role", "original-arguments"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}})
			ctx, _ = bot.WithToolOutcomes(ctx)
			journal := &botAgentReadJournal{}
			call := &genai.FunctionCall{ID: "read-1", Name: "read_file", Args: map[string]any{"path": "fixture"}}
			if scenario == "original-arguments" {
				call.Args["operation"] = "send"
			}
			journal.capture(ctx, call)
			response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"effect": "read", "verified": true}}
			wantRead := scenario == "matching" || scenario == "model-role"
			switch scenario {
			case "wrong-id":
				response.ID = "other-call"
			case "wrong-name":
				response.Name = "other_tool"
			case "reused-id":
				journal.capture(ctx, call)
			case "consumed":
				journal.take(response)
			case "original-arguments":
				delete(call.Args, "operation") // mutation cannot replace the captured action
			}
			cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
			cr.cancel()
			cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			if scenario == "model-role" {
				cr.observeBotToolOutcome("model", response)
				if cr.completion.toolResponses != 0 {
					t.Fatal("model minted response")
				}
			}
			cr.observeBotToolOutcome("tool", response)
			if got := len(cr.completion.Report.Unverified) == 0; got != wantRead {
				t.Fatalf("read=%v want=%v report=%+v", got, wantRead, cr.completion.Report)
			}
			if cr.completion.toolResponses != 1 {
				t.Fatal("response accounting changed")
			}
		})
	}
}

func TestBotRuntimeReport(t *testing.T) {
	for _, scenario := range []string{"verified", "unverified", "spoof", "invalid", "foreign-home", "missing-artifact-id", "bad-digest", "unsafe-path"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			req.Bot.Home.ID = uuid.New()
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
			body := "# Source report\nObserved independently."
			digest := sha256.Sum256([]byte(body))
			e := store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "Observed change", Source: "fixture://receipt", ObservedAt: time.Now(), ActorID: req.Bot.OwnerID}
			next := time.Now().Add(time.Hour).UTC()
			outcome := bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{e}, Unchanged: []store.BotEvidence{e}, NextRunAt: &next, Artifacts: []bot.ToolReportArtifact{{Artifact: store.BotArtifact{ID: uuid.New(), HomeID: req.Bot.Home.ID, RelativePath: "report.md", SHA256: hex.EncodeToString(digest[:]), Title: "Report", MediaType: "text/markdown"}, Body: body}}}
			switch scenario {
			case "unverified":
				outcome.Verified = false
			case "invalid":
				outcome.Changed[0].Source = ""
			case "missing-artifact-id":
				outcome.Artifacts[0].Artifact.ID = uuid.Nil
			case "bad-digest":
				outcome.Artifacts[0].Artifact.SHA256 = "invalid"
			case "unsafe-path":
				outcome.Artifacts[0].Artifact.RelativePath = "../report.md"
			case "foreign-home":
				outcome.Artifacts[0].Artifact.HomeID = uuid.New()
			}
			action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Observed source"}, func(c tool.Context, _ map[string]any) (map[string]any, error) {
				if scenario != "spoof" {
					bot.RecordToolOutcome(c, c.FunctionCallID(), outcome)
				}
				// The identical JSON payload alone is never an attestation.
				return map[string]any{bot.ToolOutcomeKey: outcome}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{}), TextTurn("Model claims changes.\n```astonish-report\npath: report.md\n```"))
			var cleaned atomic.Int64
			adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{action}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "verified" {
				if result.Status != store.RunSucceeded || len(result.Report.Changed) != 1 || len(result.Report.Unchanged) != 1 || len(result.Report.Artifacts) != 1 || result.Report.Summary != body || result.Report.NextRunAt == nil {
					t.Fatalf("lost structured outcome: %+v", result)
				}
			} else {
				if result.Status != store.RunPartial || len(result.Report.Unverified) == 0 {
					t.Fatalf("uncertainty hidden: %+v", result)
				}
				if scenario == "spoof" || scenario == "unverified" {
					if len(result.Report.Changed) != 0 || len(result.Report.Artifacts) != 0 {
						t.Fatalf("fabricated evidence trusted: %+v", result)
					}
				}
				if scenario == "foreign-home" && (len(result.Report.Artifacts) != 0 || result.Report.Summary == body) {
					t.Fatalf("foreign artifact leaked: %+v", result)
				}
			}
		})
	}
}
