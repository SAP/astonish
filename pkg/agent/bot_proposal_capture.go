package agent

import (
	"fmt"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

const BotProposalCaptureName = "propose_bot_control"

type proposalCaptureRunnable interface {
	tool.Tool
	Run(tool.Context, any) (map[string]any, error)
	Declaration() *genai.FunctionDeclaration
	ProcessRequest(tool.Context, *model.LLMRequest) error
}

// botProposalCapture is a sealed concrete capability, not a tool-name exception.
// Only the trusted launcher wraps its fenced, budgeted proposal-only implementation.
// No credential substitution or external action authorization runs on this path.
type botProposalCapture struct{ proposalCaptureRunnable }

func NewBotProposalCaptureTool(t tool.Tool) (tool.Tool, error) {
	r, ok := t.(proposalCaptureRunnable)
	if !ok || t.Name() != BotProposalCaptureName {
		return nil, fmt.Errorf("invalid proposal capture capability")
	}
	return &botProposalCapture{r}, nil
}
func (t *botProposalCapture) ProcessRequest(ctx tool.Context, r *model.LLMRequest) error {
	if err := t.proposalCaptureRunnable.ProcessRequest(ctx, r); err != nil {
		return err
	}
	if r != nil && r.Tools != nil {
		r.Tools[t.Name()] = t
	}
	return nil
}
