package common

import (
	"encoding/json"
	"reflect"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestCanonicalizeToolSchemaRecursive(t *testing.T) {
	input := map[string]any{
		"type":     "object",
		"required": []string{"zeta", "alpha", "alpha"},
		"properties": map[string]any{
			"choice": map[string]any{
				"enum": []string{"beta", "alpha", "beta"},
			},
			"nested": map[string]any{"type": "object"},
			"steps": map[string]any{
				"type": "array",
				"items": []any{
					map[string]any{"const": "second"},
					map[string]any{"const": "first"},
				},
			},
		},
	}

	got := CanonicalizeToolSchema(input)
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"properties":{"choice":{"enum":["alpha","beta"]},"nested":{"properties":{},"type":"object"},"steps":{"items":[{"const":"second"},{"const":"first"}],"type":"array"}},"required":["alpha","zeta"],"type":"object"}`
	if string(data) != want {
		t.Fatalf("canonical schema mismatch\n got: %s\nwant: %s", data, want)
	}
}

func TestCanonicalizeToolSchemaNormalizesEmptyRoot(t *testing.T) {
	for _, input := range []any{nil, map[string]any{}} {
		got, err := json.Marshal(CanonicalizeToolSchema(input))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"properties":{},"type":"object"}` {
			t.Fatalf("got %s", got)
		}
	}
}

func TestCanonicalizeRequestToolsSortsAndDoesNotMutate(t *testing.T) {
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "zeta", ParametersJsonSchema: map[string]any{}}}},
		{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "alpha",
			Parameters: &genai.Schema{
				Type:     genai.TypeObject,
				Required: []string{"z", "a"},
			},
		}}},
	}}}

	got := CanonicalizeRequestTools(req)
	if got == req || got.Config == req.Config {
		t.Fatal("expected request and config copies")
	}
	if req.Config.Tools[0].FunctionDeclarations[0].Name != "zeta" {
		t.Fatal("input request was mutated")
	}
	var names []string
	for _, tool := range got.Config.Tools {
		for _, declaration := range tool.FunctionDeclarations {
			names = append(names, declaration.Name)
		}
	}
	if !reflect.DeepEqual(names, []string{"alpha", "zeta"}) {
		t.Fatalf("declaration order = %v", names)
	}
	alpha := got.Config.Tools[0].FunctionDeclarations[0]
	if alpha.Parameters != nil || alpha.ParametersJsonSchema == nil {
		t.Fatal("typed schema was not canonicalized to JSON schema")
	}
	data, err := json.Marshal(alpha.ParametersJsonSchema)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"properties":{},"required":["a","z"],"type":"OBJECT"}` {
		t.Fatalf("typed schema = %s", data)
	}
}
