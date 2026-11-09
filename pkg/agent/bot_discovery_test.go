package agent

import (
	"errors"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestBotDiscoveryIdentityNotName(t *testing.T) {
	base := botPolicyContext(t, botBoundaryContext(t), nil, nil)
	ctx := &contextToolContext{minimalReadonlyContext{Context: base}}
	for _, name := range []string{"search_tools", "describe_tools"} {
		t.Run(name, func(t *testing.T) {
			calls, checks := 0, 0
			raw, err := functiontool.New(functiontool.Config{Name: name, Description: "metadata fixture"}, func(tool.Context, map[string]any) (map[string]any, error) {
				calls++
				return map[string]any{"ok": true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cb := botNativeCallbacks(nil, nil)[0]
			if _, err := cb(ctx, raw, map[string]any{}); err == nil || calls != 0 {
				t.Fatal("name bypassed policy")
			}
			revoked := false
			sealed, err := NewBotDiscoveryTool(raw, func(c tool.Context) (tool.Context, error) {
				checks++
				if revoked {
					return nil, errors.New("revoked")
				}
				return c, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			req := &model.LLMRequest{}
			if err := sealed.(proposalCaptureRunnable).ProcessRequest(ctx, req); err != nil {
				t.Fatal(err)
			}
			if req.Tools[name] != sealed {
				t.Fatal("raw implementation registered")
			}
			if _, err := cb(ctx, sealed, map[string]any{}); err != nil || calls != 1 {
				t.Fatalf("sealed call: %v count=%d", err, calls)
			}
			revoked = true
			if _, err := cb(ctx, sealed, map[string]any{}); err == nil || calls != 1 {
				t.Fatal("revoked discovery executed")
			}
			if _, err := sealed.(proposalCaptureRunnable).Run(ctx, map[string]any{}); err == nil || calls != 1 {
				t.Fatal("direct call skipped check")
			}
			if checks != 3 {
				t.Fatalf("checks=%d", checks)
			}
			if err := AuthorizeBotTool(base, name, nil); err == nil {
				t.Fatal("global policy was widened")
			}
			if _, err := NewBotDiscoveryTool(raw, nil); err == nil {
				t.Fatal("missing live guard accepted")
			}
		})
	}
	raw, err := functiontool.New(functiontool.Config{Name: "execute_tool", Description: "dispatcher"}, func(tool.Context, map[string]any) (map[string]any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewBotDiscoveryTool(raw, func(c tool.Context) (tool.Context, error) { return c, nil }); err == nil {
		t.Fatal("execution sealed as metadata")
	}
}
