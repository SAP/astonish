package agent

import (
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestFingerprintRequestStableAcrossRounds(t *testing.T) {
	request := func(system string, names ...string) *model.LLMRequest {
		declarations := make([]*genai.FunctionDeclaration, 0, len(names))
		tools := make(map[string]any, len(names))
		for _, name := range names {
			declarations = append(declarations, &genai.FunctionDeclaration{
				Name: name,
				ParametersJsonSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			})
			tools[name] = struct{}{}
		}
		return &model.LLMRequest{
			Config: &genai.GenerateContentConfig{
				SystemInstruction: genai.NewContentFromText(system, "system"),
				Tools:             []*genai.Tool{{FunctionDeclarations: declarations}},
			},
			Tools: tools,
		}
	}

	first := fingerprintRequest(request("stable prompt", "beta", "alpha"))
	intraTurn := fingerprintRequest(request("stable prompt", "alpha", "beta"))
	secondTurn := fingerprintRequest(request("stable prompt", "beta", "alpha"))
	if first != intraTurn || first != secondTurn {
		t.Fatalf("stable request fingerprints changed: first=%+v intra=%+v second=%+v", first, intraTurn, secondTurn)
	}

	changedPrompt := fingerprintRequest(request("changed prompt", "alpha", "beta"))
	if changedPrompt.system == first.system || changedPrompt.tools != first.tools {
		t.Fatalf("prompt-only change not isolated: first=%+v changed=%+v", first, changedPrompt)
	}
	changedTools := fingerprintRequest(request("stable prompt", "alpha", "gamma"))
	if changedTools.tools == first.tools || changedTools.system != first.system {
		t.Fatalf("tool-only change not isolated: first=%+v changed=%+v", first, changedTools)
	}
}
