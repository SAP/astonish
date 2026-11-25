package launcher

import (
	"testing"

	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotSharedDispatchPreservesRuntimeNext(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			if interactive {
				setBotSharedInteractive(f)
			}
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			binding := botFactoryModelForTest(t, got.Agent.LLM).binding
			entry := got.Agent.ToolIndex.GetToolEntry("read_file").Tool.(*chatRuntimeTool)
			invocation := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}}
			bound := botBoundToolContext{Context: invocation, owner: binding.bind(invocation)}
			calls := 0
			result, err := (botSharedDispatch{binding}).Dispatch(bound, entry.chatRuntimeRunnable, map[string]any{"path": "outputs/test"}, func(tool.Context, any) (map[string]any, error) {
				calls++
				return map[string]any{"content": "guarded next"}, nil
			})
			if err != nil || calls != 1 || result["content"] != "guarded next" {
				t.Fatalf("shared continuation bypassed: calls=%d result=%v err=%v", calls, result, err)
			}
			binding.researchOnly = true
			denied := got.Agent.ToolIndex.GetToolEntry("write_file").Tool.(*chatRuntimeTool)
			_, err = (botSharedDispatch{binding}).Dispatch(bound, denied.chatRuntimeRunnable, map[string]any{}, func(tool.Context, any) (map[string]any, error) { calls++; return nil, nil })
			if err != store.ErrBotForbidden || calls != 1 {
				t.Fatalf("research child widened authority: calls=%d err=%v", calls, err)
			}
		})
	}
}
