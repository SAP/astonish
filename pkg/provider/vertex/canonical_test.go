package vertex

import (
	"encoding/json"
	"math/rand"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestConvertRequestToolSerializationIsCanonical(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	names := []string{"charlie", "alpha", "bravo"}
	var want []byte
	for iteration := 0; iteration < 50; iteration++ {
		var tools []*genai.Tool
		for _, index := range rng.Perm(len(names)) {
			properties := make(map[string]any)
			keys := []string{"zeta", "alpha"}
			for _, propertyIndex := range rng.Perm(len(keys)) {
				properties[keys[propertyIndex]] = map[string]any{"type": "string"}
			}
			tools = append(tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: names[index],
				ParametersJsonSchema: map[string]any{
					"type":       "object",
					"required":   []string{"zeta", "alpha"},
					"properties": properties,
				},
			}}})
		}
		converted, err := ConvertRequest(&model.LLMRequest{Config: &genai.GenerateContentConfig{
			Tools: tools,
			ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode:                 genai.FunctionCallingConfigModeAny,
				AllowedFunctionNames: []string{"charlie", "alpha", "bravo"},
			}},
		}}, 100)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(converted)
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 0 {
			want = data
		} else if string(data) != string(want) {
			t.Fatalf("converted request %d differs\n got: %s\nwant: %s", iteration, data, want)
		}
	}
}
