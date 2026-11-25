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
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
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

func TestBotAgentNativeDiscoverySettlesWithoutBecomingSource(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx, _ = bot.WithToolOutcomes(ctx)
	journal := &botAgentReadJournal{}
	cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
	defer cr.cancel()
	cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
	for _, name := range []string{common.SearchToolsName, common.DescribeToolsName} {
		call := &genai.FunctionCall{ID: name + "-call", Name: name, Args: map[string]any{"query": "web research"}}
		journal.capture(ctx, call)
		response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"tools": []any{"web_fetch"}}}
		observation := cr.observeBotToolOutcome("tool", response)
		if observation.SourceEligible {
			t.Fatalf("native discovery became factual source: name=%s observation=%+v", name, observation)
		}
	}
	if journal.effectState() != store.BotReportEffectsReadOnly || len(cr.completion.Report.Unverified) != 0 {
		t.Fatalf("native discovery poisoned read-only work: state=%s report=%+v", journal.effectState(), cr.completion.Report)
	}
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

func TestBotAgentNonexecutionOutcomeBinding(t *testing.T) {
	for _, mode := range []string{"matching", "missing-response", "wrong-id", "wrong-name", "receipt-id", "receipt-name", "duplicate-call", "duplicate-response", "duplicate-ledger", "unverified", "bare-verified", "invalid-observation", "spoofed-json", "send", "write", "unchanged", "diagnostic", "source", "artifact", "schedule", "routine", "next-run"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx, _ = bot.WithToolOutcomes(ctx)
			journal := &botAgentReadJournal{}
			// No autonomous classifier is bound into this interactive context.
			call := &genai.FunctionCall{ID: "optional", Name: "bot_work_update", Args: map[string]any{"kind": "bad"}}
			journal.capture(ctx, call)
			outcome := bot.NewToolNonexecutionOutcome(ctx, call.ID, call.Name, call.Args, bot.ToolFeedbackCorrectCall)
			response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"status": "invalid_request"}}
			switch mode {
			case "wrong-id":
				response.ID = "other"
			case "wrong-name":
				response.Name = "other"
			case "receipt-id":
				outcome.Nonexecution.CallID = "other"
			case "receipt-name":
				outcome.Nonexecution.ToolName = "other"
			case "duplicate-call":
				journal.capture(ctx, call)
			case "unverified":
				outcome.Verified = false
			case "bare-verified":
				outcome.Nonexecution = nil
			case "invalid-observation":
				outcome.Nonexecution = nil
				outcome.Unchanged = []store.BotEvidence{{Summary: "not an independent observation"}}
			case "spoofed-json":
				response.Response[bot.ToolOutcomeKey] = map[string]any{"verified": true, "nonexecution": map[string]any{"CallID": call.ID, "ToolName": call.Name}}
			case "send", "write":
				outcome.Changed = []store.BotEvidence{{Summary: "uncertain " + mode}}
			case "unchanged":
				outcome.Unchanged = []store.BotEvidence{{Summary: "mixed receipt"}}
			case "diagnostic":
				outcome.Unverified = []string{"external effect receipt missing"}
			case "source":
				outcome.SourceAccess = []store.BotSourceAccessFact{{CallID: call.ID}}
			case "artifact":
				outcome.Artifacts = []bot.ToolReportArtifact{{}}
			case "schedule":
				outcome.ScheduleCapture = &bot.ScheduleCapture{}
			case "routine":
				outcome.RoutineCapture = &bot.RoutineCapture{}
			case "next-run":
				now := time.Now()
				outcome.NextRunAt = &now
			}
			if mode != "spoofed-json" {
				bot.RecordToolOutcome(ctx, call.ID, outcome)
			}
			if mode == "duplicate-ledger" {
				bot.RecordToolOutcome(ctx, call.ID, outcome)
			}
			cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			if mode != "missing-response" {
				cr.observeBotToolOutcome("tool", response)
			}
			if mode == "duplicate-response" {
				cr.observeBotToolOutcome("tool", response)
			}
			want := store.BotReportEffectsUncertain
			if mode == "matching" {
				want = store.BotReportEffectsReadOnly
				if len(cr.completion.Report.Unverified) != 0 || len(cr.completion.Report.Changed) != 0 {
					t.Fatalf("rejection minted a content veto/effect: %+v", cr.completion.Report)
				}
			}
			if got := journal.effectState(); got != want {
				t.Fatalf("state=%q want=%q report=%+v", got, want, cr.completion.Report)
			}
		})
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

type botBridgeReportTestClassifier struct{ botReportTestClassifier }

func (c botBridgeReportTestClassifier) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	if name == "execute_tool" {
		return execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectUnknown}, nil
	}
	return c.botReportTestClassifier.DescribeAction(ctx, name, raw)
}

func TestBotAgentBridgeReadOutcome(t *testing.T) {
	for _, scenario := range []string{"read", "write", "unknown", "unconstrained", "mutated-classification", "malformed", "missing-target", "nested-bridge", "denied", "unrecognized", "missing-response", "wrong-id", "wrong-name", "duplicate-call", "duplicate-response", "model-role", "failed-receipt", "spoofed-json"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx, _ = bot.WithToolOutcomes(ctx)
			classifier := botReportTestClassifier{effect: execution.EffectRead, verified: true}
			if scenario == "unknown" {
				classifier.effect = execution.EffectUnknown
			}
			if scenario == "unconstrained" {
				classifier.verified = false
			}
			if scenario == "mutated-classification" {
				classifier.mutate = true
			}
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botBridgeReportTestClassifier{classifier}})
			journal := &botAgentReadJournal{}
			args := map[string]any{"path": "fixture"}
			if scenario == "write" {
				args["operation"] = "send"
			}
			call := &genai.FunctionCall{ID: "bridge-1", Name: "execute_tool", Args: map[string]any{"name": "read_file", "arguments": args}}
			if scenario == "malformed" {
				call.Args["arguments"] = "{"
			}
			if scenario == "missing-target" {
				delete(call.Args, "name")
			}
			if scenario == "nested-bridge" {
				call.Args["name"] = "execute_tool"
			}
			journal.capture(ctx, call)
			if scenario == "duplicate-call" {
				journal.capture(ctx, call)
			}
			response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"content": "actual source"}}
			switch scenario {
			case "denied":
				response.Response = map[string]any{"status": "denied"}
			case "unrecognized":
				response.Response = map[string]any{"error": "tool not found in available catalog"}
			case "missing-response":
				response.Response = nil
			case "wrong-id":
				response.ID = "other"
			case "wrong-name":
				response.Name = "read_file"
			case "failed-receipt":
				bot.RecordToolOutcome(ctx, call.ID, bot.ToolOutcome{Unverified: []string{"receipt failed"}})
			case "spoofed-json":
				response.Response = map[string]any{"error": "denied", bot.ToolOutcomeKey: map[string]any{"verified": true, "effect": "read"}}
			}
			cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			if scenario == "model-role" {
				cr.observeBotToolOutcome("model", response)
				if cr.completion.toolResponses != 0 || journal.effectState() != store.BotReportEffectsUncertain {
					t.Fatal("model response completed a pending bridge invocation")
				}
				return
			}
			cr.observeBotToolOutcome("tool", response)
			if scenario == "duplicate-response" {
				cr.observeBotToolOutcome("tool", response)
			}
			wantRead := scenario == "read"
			if got := len(cr.completion.Report.Unverified) == 0; got != wantRead {
				t.Fatalf("read=%v want=%v report=%+v", got, wantRead, cr.completion.Report)
			}
			if wantRead && journal.effectState() != store.BotReportEffectsReadOnly {
				t.Fatalf("read state=%s", journal.effectState())
			}
			if len(cr.completion.Report.Changed) != 0 || len(cr.completion.Report.Artifacts) != 0 {
				t.Fatal("bridge minted effect evidence")
			}
		})
	}
}

type botBridgeReportContext struct {
	tool.Context
	base context.Context
	id   string
}

func (c botBridgeReportContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c botBridgeReportContext) Value(key any) any                                    { return c.base.Value(key) }
func (c botBridgeReportContext) FunctionCallID() string                               { return c.id }

type botBridgeReportAuthorizer struct{ deny bool }

func (a botBridgeReportAuthorizer) Authorize(context.Context, execution.Capability) error { return nil }
func (a botBridgeReportAuthorizer) AuthorizeTool(context.Context, string) error {
	if a.deny {
		return store.ErrBotForbidden
	}
	return nil
}

func TestBotAgentBridgeActualDispatch(t *testing.T) {
	for _, scenario := range []string{"read", "write", "denied", "unknown-target", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = execution.WithBotTurnAuthorizer(ctx, botBridgeReportAuthorizer{deny: scenario == "denied"})
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botBridgeReportTestClassifier{botReportTestClassifier{effect: execution.EffectRead, verified: true}}})
			ctx, _ = bot.WithToolOutcomes(ctx)
			calls := 0
			target, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "dispatch fixture"}, func(tool.Context, map[string]any) (map[string]any, error) {
				calls++
				return map[string]any{"content": "actual dispatch", bot.ToolOutcomeKey: map[string]any{"verified": true}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			idx := agent.NewLexicalToolIndex()
			if err := idx.PrimeTools(ctx, []tool.Tool{target}, nil); err != nil {
				t.Fatal(err)
			}
			bridge, err := agent.NewProgressiveToolBridge(idx)
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"path": "fixture"}
			if scenario == "write" {
				args["operation"] = "send"
			}
			call := &genai.FunctionCall{ID: "dispatch-1", Name: "execute_tool", Args: map[string]any{"name": "read_file", "arguments": args}}
			if scenario == "unknown-target" {
				call.Args["name"] = "unknown"
			}
			if scenario == "malformed" {
				call.Args["arguments"] = "{"
			}
			journal := &botAgentReadJournal{}
			journal.capture(ctx, call)
			out, err := bridge[1].(interface {
				Run(tool.Context, any) (map[string]any, error)
			}).Run(botBridgeReportContext{base: ctx, id: call.ID}, call.Args)
			if err != nil {
				out = map[string]any{"error": err.Error()}
			}
			cr := newChatRunner(req.Run.SessionID.String(), req.Bot.OwnerID.String(), studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			observation := cr.observeBotToolOutcome("tool", &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: out})
			knownRejected := scenario == "denied" || scenario == "unknown-target"
			if knownRejected && (!observation.KnownNonexecution || observation.SourceEligible || cr.completion.Report.EffectState != store.BotReportEffectsReadOnly) {
				t.Fatalf("pre-dispatch rejection lost bound proof or became source evidence: %+v report=%+v", observation, cr.completion.Report)
			}
			if (len(cr.completion.Report.Unverified) == 0) != (scenario == "read" || knownRejected) {
				t.Fatalf("report=%+v output=%+v error=%v", cr.completion.Report, out, err)
			}
			wantCalls := 0
			if scenario == "read" || scenario == "write" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("executions=%d want=%d", calls, wantCalls)
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
