package launcher

import (
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// accountingPrivateChildTool preserves the original private conversation through
// the trusted child harness while leaving target authorization/accounting intact.
type accountingPrivateChildTool struct {
	botRunnableTool
	binding *botFactoryBinding
}

func (t accountingPrivateChildTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	return t.botRunnableTool.Run(botWorkerExecutionContext{Context: ctx, sessionID: t.binding.req.Run.SessionID.String(), conversation: botWorkerConversation{binding: t.binding, id: t.binding.conversationID}}, args)
}

// Native declarations retain this trusted conversation adapter as the executable.
func (t accountingPrivateChildTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	bound := botWorkerExecutionContext{Context: ctx, sessionID: t.binding.req.Run.SessionID.String(), conversation: botWorkerConversation{binding: t.binding, id: t.binding.conversationID}}
	if err := t.botRunnableTool.ProcessRequest(bound, req); err != nil {
		return err
	}
	if req != nil && req.Tools != nil && req.Tools[t.Name()] != nil {
		req.Tools[t.Name()] = t
	}
	return nil
}
