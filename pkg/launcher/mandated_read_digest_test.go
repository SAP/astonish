package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
)

type mandatedDigestContext struct{ proposalTestContext }

func (mandatedDigestContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

func TestMandatedReadSourceDigestMatchesCapturedResponse(t *testing.T) {
	for _, name := range []string{"http_request", "web_fetch"} {
		t.Run(name, func(t *testing.T) {
			var native tool.Tool
			var err error
			if name == "http_request" {
				native, err = functiontool.New(functiontool.Config{Name: name, Description: "local fixture"}, func(tool.Context, tools.HttpRequestArgs) (tools.HttpRequestResult, error) {
					return tools.HttpRequestResult{StatusCode: 200, Body: `{"weather":"clear"}`}, nil
				})
			} else {
				native, err = functiontool.New(functiontool.Config{Name: name, Description: "local fixture"}, func(tool.Context, tools.WebFetchArgs) (tools.WebFetchResult, error) {
					return tools.WebFetchResult{Status: 200, FinalURL: "https://example.test/weather", Content: "clear"}, nil
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			base := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()})
			base, _ = bot.WithToolOutcomes(base)
			ctx := mandatedDigestContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: "digest-session"}}
			adapter := &botMandatedReadTool{botRunnableTool: native.(botRunnableTool), policy: tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }}}
			result, err := adapter.Run(ctx, map[string]any{"url": "https://example.test/weather"})
			if err != nil {
				t.Fatal(err)
			}
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			if !ok || len(outcome.SourceAccess) != 1 {
				t.Fatalf("missing source fact: %+v", outcome)
			}
			// Session storage round-trips the ADK result through JSON. Evidence reads
			// hash that complete response, never only an unwrapped observation.
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var persisted map[string]any
			if err = json.Unmarshal(encoded, &persisted); err != nil {
				t.Fatal(err)
			}
			encoded, err = json.Marshal(persisted)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(encoded)
			fact := outcome.SourceAccess[0]
			if fact.BodyDigest != hex.EncodeToString(digest[:]) {
				t.Fatalf("source digest does not match complete persisted %s response: got %s want %x", name, fact.BodyDigest, digest)
			}
			if fact.CallID != ctx.FunctionCallID() || fact.EvidenceRef != "capture:"+ctx.FunctionCallID() || fact.HTTPStatus != 200 {
				t.Fatalf("invalid binding: %+v", fact)
			}
			work := store.NewBotAgentWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Read weather"})
			work.AgentWork.SourceAccess = []store.BotSourceAccessFact{fact}
			work.AgentWork.Evidence = []store.BotWorkEvidenceReference{{Ref: fact.EvidenceRef, RunID: work.Contract.RunID, EventID: "captured-event", CallID: fact.CallID, Digest: hex.EncodeToString(digest[:])}}
			if err := work.Validate(); err != nil {
				t.Fatalf("genuine captured response rejected by work validation: %v", err)
			}
			for _, tamper := range []string{"body", "call"} {
				invalid := work.Clone()
				if tamper == "body" {
					invalid.AgentWork.SourceAccess[0].BodyDigest = hex.EncodeToString(make([]byte, sha256.Size))
				} else {
					invalid.AgentWork.SourceAccess[0].CallID = "another-call"
				}
				if invalid.Validate() == nil {
					t.Fatalf("accepted tampered %s binding", tamper)
				}
			}
		})
	}
}
