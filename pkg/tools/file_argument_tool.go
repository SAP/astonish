package tools

import (
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// fileArgumentTool keeps compatibility decoding outside the advertised schema.
// It is installed before sandbox/approval wrappers, so neither is bypassed.
type fileArgumentTool struct{ tool.Tool }

func (t *fileArgumentTool) Declaration() *genai.FunctionDeclaration {
	return t.Tool.(interface {
		Declaration() *genai.FunctionDeclaration
	}).Declaration()
}

func (t *fileArgumentTool) NormalizeArguments(args map[string]any) (map[string]any, error) {
	return common.NormalizeFileToolArguments(t.Name(), args)
}

func (t *fileArgumentTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if err := t.Tool.(interface {
		ProcessRequest(tool.Context, *model.LLMRequest) error
	}).ProcessRequest(ctx, req); err != nil {
		return err
	}
	// The underlying function packs its schema and itself. Dispatch must retain
	// this wrapper so native calls get the same compatibility handling as node.
	req.Tools[t.Name()] = t
	return nil
}

func (t *fileArgumentTool) Run(ctx tool.Context, raw any) (map[string]any, error) {
	args, ok := raw.(map[string]any)
	if !ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &args); err != nil {
			return nil, fmt.Errorf("invalid file arguments: %w", err)
		}
	}
	args, err := t.NormalizeArguments(args)
	if err != nil {
		return nil, err
	}
	return t.Tool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	}).Run(ctx, args)
}
