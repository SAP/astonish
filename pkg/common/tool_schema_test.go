package common

import (
	"encoding/json"
	"math/rand"
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
			"choice": map[string]any{"enum": []string{"beta", "alpha", "beta"}},
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

	got, err := CanonicalizeToolSchema(input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"properties":{"choice":{"enum":["alpha","beta"]},"nested":{"properties":{},"type":"object"},"steps":{"items":[{"const":"second"},{"const":"first"}],"type":"array"}},"required":["alpha","zeta"],"type":"object"}`
	if string(data) != want {
		t.Fatalf("canonical schema mismatch\n got: %s\nwant: %s", data, want)
	}
}

func TestCanonicalizeToolSchemaNormalizesEmptyAndTypedNil(t *testing.T) {
	var typedNil *genai.Schema
	for _, input := range []any{nil, typedNil, map[string]any{}} {
		got, err := CanonicalizeToolSchema(input)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"properties":{},"type":"object"}` {
			t.Fatalf("got %s", data)
		}
	}
}

func TestCanonicalizeRequestToolsGlobalOrderAndDoesNotMutate(t *testing.T) {
	jsonParameters := map[string]any{"type": "object", "required": []string{"json"}}
	jsonResponse := map[string]any{"type": "object", "required": []string{"json_response"}}
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{
		Tools: []*genai.Tool{
			{
				GoogleSearch: &genai.GoogleSearch{},
				FunctionDeclarations: []*genai.FunctionDeclaration{
					{Name: "zeta", ParametersJsonSchema: map[string]any{}},
					{Name: "alpha", Parameters: &genai.Schema{Type: genai.TypeObject, Required: []string{"z", "a"}}},
				},
			},
			{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name:                 "middle",
				Parameters:           &genai.Schema{Type: genai.TypeString},
				ParametersJsonSchema: jsonParameters,
				Response:             &genai.Schema{Type: genai.TypeString},
				ResponseJsonSchema:   jsonResponse,
			}}},
		},
		ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
			Mode:                 genai.FunctionCallingConfigModeAny,
			AllowedFunctionNames: []string{"zeta", "alpha", "zeta"},
		}},
	}}

	got, err := CanonicalizeRequestTools(req)
	if err != nil {
		t.Fatal(err)
	}
	if got == req || got.Config == req.Config {
		t.Fatal("expected request and config copies")
	}
	if req.Config.Tools[0].FunctionDeclarations[0].Name != "zeta" {
		t.Fatal("input request was mutated")
	}
	if !reflect.DeepEqual(got.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames, []string{"alpha", "zeta"}) {
		t.Fatalf("allowed names = %v", got.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames)
	}

	var declarations []*genai.FunctionDeclaration
	var preservedSearch bool
	for _, tool := range got.Config.Tools {
		declarations = append(declarations, tool.FunctionDeclarations...)
		preservedSearch = preservedSearch || tool.GoogleSearch != nil
	}
	if !preservedSearch {
		t.Fatal("non-function tool semantics were lost")
	}
	if len(declarations) != 3 {
		t.Fatalf("declaration count = %d", len(declarations))
	}
	if names := []string{declarations[0].Name, declarations[1].Name, declarations[2].Name}; !reflect.DeepEqual(names, []string{"alpha", "middle", "zeta"}) {
		t.Fatalf("declaration order = %v", names)
	}
	middle := declarations[1]
	if middle.Parameters != nil || middle.Response != nil {
		t.Fatal("typed schemas were not cleared")
	}
	parameters, _ := json.Marshal(middle.ParametersJsonSchema)
	response, _ := json.Marshal(middle.ResponseJsonSchema)
	if string(parameters) != `{"properties":{},"required":["json"],"type":"object"}` {
		t.Fatalf("dual parameter schema = %s", parameters)
	}
	if string(response) != `{"properties":{},"required":["json_response"],"type":"object"}` {
		t.Fatalf("dual response schema = %s", response)
	}
}

func TestCanonicalizeRequestToolsPermutationByteEquality(t *testing.T) {
	seed := rand.New(rand.NewSource(42))
	declarations := []*genai.FunctionDeclaration{
		{Name: "charlie", ParametersJsonSchema: randomizedSchema(seed)},
		{Name: "alpha", ParametersJsonSchema: randomizedSchema(seed)},
		{Name: "bravo", ParametersJsonSchema: randomizedSchema(seed)},
	}
	var want []byte
	for iteration := 0; iteration < 100; iteration++ {
		permutation := seed.Perm(len(declarations))
		tools := make([]*genai.Tool, 0, len(declarations))
		for _, index := range permutation {
			declaration := *declarations[index]
			declaration.ParametersJsonSchema = randomizedSchema(seed)
			tools = append(tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{&declaration}})
		}
		req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: tools}}
		got, err := CanonicalizeRequestTools(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(got.Config.Tools)
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 0 {
			want = data
		} else if string(data) != string(want) {
			t.Fatalf("iteration %d differs\n got: %s\nwant: %s", iteration, data, want)
		}
	}
}

func TestCanonicalizeRequestToolsFailsOnUnsupportedSchema(t *testing.T) {
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "invalid", ParametersJsonSchema: map[string]any{"bad": make(chan int)},
		}},
	}}}}
	if _, err := CanonicalizeRequestTools(req); err == nil {
		t.Fatal("expected canonicalization error")
	}
}

func randomizedSchema(rng *rand.Rand) map[string]any {
	entries := []struct {
		key   string
		value any
	}{
		{"type", "object"},
		{"required", []string{"z", "a", "z"}},
		{"properties", map[string]any{
			"sequence": map[string]any{"type": "array", "items": []any{
				map[string]any{"const": "second"}, map[string]any{"const": "first"},
			}},
			"choice": map[string]any{"enum": []string{"z", "a"}},
		}},
	}
	result := make(map[string]any, len(entries))
	for _, index := range rng.Perm(len(entries)) {
		result[entries[index].key] = entries[index].value
	}
	return result
}
