package launcher

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"testing"
)

func TestBotMandatedReadClassifier(t *testing.T) {
	native, e := functiontool.New(functiontool.Config{Name: "http_request", Description: "test"}, func(tool.Context, tools.HttpRequestArgs) (tools.HttpRequestResult, error) {
		return tools.HttpRequestResult{}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	read := &botMandatedReadTool{botRunnableTool: native.(botRunnableTool), policy: tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }}}
	classifier := WithBotMandatedReadClassifier(BotCatalogueClassifier{}, []tool.Tool{read})
	for _, raw := range []string{`{"url":"https://example.org","method":"GET"}`, `{"url":"https://example.org","method":"HEAD"}`} {
		a, e := classifier.DescribeAction(context.Background(), "http_request", json.RawMessage(raw))
		if e != nil || a.Effect != execution.EffectRead || !a.ConstraintsVerified {
			t.Fatalf("%+v %v", a, e)
		}
	}
	for _, raw := range []string{`{"url":"https://example.org","method":"POST"}`, `{"url":"https://example.org","headers":{"Authorization":"secret"}}`, `{"url":"https://example.org","credential":"secret"}`} {
		if _, e := classifier.DescribeAction(context.Background(), "http_request", json.RawMessage(raw)); e == nil {
			t.Fatal(raw)
		}
	}
	for _, name := range []string{"shell_command", "browser_navigate", "web_fetch"} {
		a, e := classifier.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
		if e != nil || a.Effect != execution.EffectUnknown {
			t.Fatalf("unbound name classified read: %s %+v %v", name, a, e)
		}
	}
}
