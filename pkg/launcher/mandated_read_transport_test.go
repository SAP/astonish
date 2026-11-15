package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestMandatedReadTransportFailureCapture(t *testing.T) {
	for _, name := range []string{"web_fetch", "http_request"} {
		t.Run(name, func(t *testing.T) {
			var native tool.Tool
			var err error
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()
			policy := tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }}
			if name == "web_fetch" {
				native, err = functiontool.New(functiontool.Config{Name: name}, func(c tool.Context, a tools.WebFetchArgs) (tools.WebFetchResult, error) {
					return tools.WebFetch(botBoundToolContext{Context: c, owner: tools.WithRoutineReadPolicy(c, policy)}, a)
				})
			} else {
				native, err = functiontool.New(functiontool.Config{Name: name}, func(c tool.Context, a tools.HttpRequestArgs) (tools.HttpRequestResult, error) {
					return tools.HttpRequest(botBoundToolContext{Context: c, owner: tools.WithRoutineReadPolicy(c, policy)}, a)
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			base := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()})
			base, _ = bot.WithToolOutcomes(base)
			ctx := mandatedDigestContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: "failure-session"}}
			adapter := &botMandatedReadTool{botRunnableTool: native.(botRunnableTool), policy: tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }}}
			result, err := adapter.Run(ctx, map[string]any{"url": server.URL + "/story"})
			if err != nil {
				t.Fatal(err)
			}
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			if !ok || len(outcome.SourceAccess) != 1 {
				t.Fatalf("missing failure fact: response=%#v outcome=%+v", result, outcome)
			}
			raw, _ := json.Marshal(result)
			digest := sha256.Sum256(raw)
			fact := outcome.SourceAccess[0]
			if fact.Status != store.BotSourceAccessFailed || fact.HTTPStatus != 0 || fact.BodyDigest != hex.EncodeToString(digest[:]) || fact.CallID != ctx.FunctionCallID() || fact.EvidenceRef != "capture:"+ctx.FunctionCallID() || outcome.Verified || len(outcome.Changed) != 0 || len(outcome.Unchanged) != 0 {
				t.Fatalf("invalid failure receipt: %#v %+v", result, outcome)
			}
		})
	}
}
