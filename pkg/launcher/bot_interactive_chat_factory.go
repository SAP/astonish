package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// Interactive policy dispatch is used by the shared Chat runtime.
type interactiveBotTool struct {
	next func(tool.Context, any) (map[string]any, error)
	botRunnableTool
	binding *botFactoryBinding
}

func (t *interactiveBotTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	t.binding.declarations.close()
	if ctx == nil || ctx.SessionID() == "" {
		return nil, store.ErrBotForbidden
	}
	bound := botBoundToolContext{Context: ctx, owner: t.binding.bind(ctx)}
	if err := t.binding.check(bound); err != nil {
		return nil, err
	}
	if t.binding.synthesisReserve.synthesis() {
		return botSynthesisStoppedResult(bound, t.Name(), args), nil
	}
	if err := t.binding.checkResearchDetail(bound, ctx.SessionID(), t.Name(), args); err != nil {
		return nil, err
	}
	if err := t.binding.req.Authorization.AuthorizeTool(bound, t.Name()); err != nil {
		return nil, err
	}
	for _, disabled := range store.DisabledToolsFromContext(bound) {
		if disabled == t.Name() {
			return nil, store.ErrBotForbidden
		}
	}
	if err := t.binding.req.Budget.ReserveTool(bound, t.Name()); err != nil {
		return nil, err
	}
	return t.binding.progressTool(bound, t.botRunnableTool, args, func(c tool.Context, a any) (map[string]any, error) {
		result, err := t.binding.runSynthesisTool(c, botDispatchTarget(t.botRunnableTool, t.next), a)
		if t.binding.tasks != nil && substantiveTaskTool(t.botRunnableTool) {
			if captureErr := t.binding.tasks.observe(bound, t.Name(), args, result, err); captureErr != nil {
				return result, captureErr
			}
		}
		// Only the shared built-in node transport is eligible; an MCP tool with
		// the same name or a forged result cannot attest to effects.
		if _, builtin := t.botRunnableTool.(*sandbox.NodeTool); builtin {
			raw, marshalErr := json.Marshal(args)
			var values map[string]any
			if marshalErr == nil && json.Unmarshal(raw, &values) == nil {
				if values, normalizeErr := common.NormalizeFileToolArguments(t.Name(), values); normalizeErr == nil {
					observation := result
					if err != nil {
						observation = map[string]any{"error": err.Error()}
					}
					recordBuiltinToolObservation(bound, t.binding.req.Run.SessionID.String(), t.Name(), values, observation)
				}
			}
		}
		return result, err
	})
}
func (t *interactiveBotTool) ProcessRequest(ctx tool.Context, r *model.LLMRequest) (err error) {
	defer func() {
		if err != nil {
			t.binding.declarations.close()
		}
	}()
	if ctx == nil {
		return store.ErrBotForbidden
	}
	binding := t.binding.declarations.bind(t.binding, r)
	bound := botBoundToolContext{Context: ctx, owner: binding.bind(ctx)}
	if err := binding.check(bound); err != nil {
		return err
	}
	if err := t.botRunnableTool.ProcessRequest(bound, r); err != nil {
		return err
	}
	// Declarations may register their raw executable. Preserve the same bound
	// dispatch used by progressive discovery, lazy toolsets and children.
	if r != nil && r.Tools != nil {
		if _, registered := r.Tools[t.Name()]; registered {
			r.Tools[t.Name()] = t
		}
	}
	return nil
}
