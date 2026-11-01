package common

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type argumentValidationTool struct {
	tool.Tool
	decl *genai.FunctionDeclaration
}

func (t argumentValidationTool) Name() string                            { return "test_tool" }
func (t argumentValidationTool) Declaration() *genai.FunctionDeclaration { return t.decl }

type argumentValidationNoDeclaration struct{ tool.Tool }

func (argumentValidationNoDeclaration) Name() string { return "no_declaration" }

func TestValidateToolArguments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema any
		args   map[string]any
		want   string
	}{
		{"missing", map[string]any{"required": []string{"file_path"}}, map[string]any{"path": "secret-path", "content": "secret-content"}, `tool "test_tool": required argument "file_path" is missing`},
		{"nil_args", json.RawMessage(`{"required":["content"]}`), nil, `tool "test_tool": required argument "content" is missing`},
		{"required_null", json.RawMessage(`{"required":["content"],"properties":{"content":{"type":"string"}}}`), map[string]any{"content": nil}, `tool "test_tool": required argument "content" must not be null`},
		{"deterministic", map[string]any{"required": []any{"z", "a"}}, nil, `tool "test_tool": required argument "a" is missing`},
		{"empty_content", map[string]any{"required": []string{"content"}}, map[string]any{"content": ""}, ""},
		{"false_zero_optional_unknown", json.RawMessage(`{"required":["enabled","count"],"properties":{"optional":{"type":"string"}}}`), map[string]any{"enabled": false, "count": 0, "optional": nil, "_caller": "chat"}, ""},
		{"no_broad_type_validation", json.RawMessage(`{"required":["value"],"properties":{"value":{"type":"string"}}}`), map[string]any{"value": 7}, ""},
		{"nullable", json.RawMessage(`{"required":["value"],"properties":{"value":{"type":"string","nullable":true}}}`), map[string]any{"value": nil}, ""},
		{"null_type", json.RawMessage(`{"required":["value"],"properties":{"value":{"type":"null"}}}`), map[string]any{"value": nil}, ""},
		{"union", json.RawMessage(`{"required":["value"],"properties":{"value":{"type":["string","null"]}}}`), map[string]any{"value": nil}, ""},
		{"anyOf", json.RawMessage(`{"required":["value"],"properties":{"value":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`), map[string]any{"value": nil}, ""},
		{"oneOf", json.RawMessage(`{"required":["value"],"properties":{"value":{"oneOf":[{"type":"null"},{"type":"string"}]}}}`), map[string]any{"value": nil}, ""},
		{"nullable_still_required", json.RawMessage(`{"required":["value"],"properties":{"value":{"nullable":true}}}`), nil, `tool "test_tool": required argument "value" is missing`},
		{"nested_not_validated", json.RawMessage(`{"required":["value"],"properties":{"value":{"type":"object","required":["nested"]}}}`), map[string]any{"value": map[string]any{}}, ""},
		{"typed_schema", &genai.Schema{Type: genai.TypeObject, Required: []string{"content"}, Properties: map[string]*genai.Schema{"content": {Type: genai.TypeString}}}, nil, `tool "test_tool": required argument "content" is missing`},
		{"typed_nullable", &genai.Schema{Type: genai.TypeObject, Required: []string{"value"}, Properties: map[string]*genai.Schema{"value": {Type: genai.TypeString, Nullable: new(true)}}}, map[string]any{"value": nil}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.args)
			err := ValidateToolArguments(argumentValidationTool{decl: &genai.FunctionDeclaration{ParametersJsonSchema: tc.schema}}, tc.args)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
			after, _ := json.Marshal(tc.args)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("arguments mutated")
			}
		})
	}
}

func TestValidateToolArguments_DeclarationForms(t *testing.T) {
	for _, candidate := range []tool.Tool{
		argumentValidationNoDeclaration{},
		argumentValidationTool{},
		argumentValidationTool{decl: &genai.FunctionDeclaration{}},
		argumentValidationTool{decl: &genai.FunctionDeclaration{ParametersJsonSchema: (*genai.Schema)(nil)}},
	} {
		if err := ValidateToolArguments(candidate, nil); err != nil {
			t.Fatal(err)
		}
	}
	typed := argumentValidationTool{decl: &genai.FunctionDeclaration{Parameters: &genai.Schema{Type: genai.TypeObject, Required: []string{"value"}}}}
	if err := ValidateToolArguments(typed, nil); err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("typed Parameters error = %v", err)
	}
}

func TestValidateToolArguments_FunctionToolSchema(t *testing.T) {
	type args struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
		Optional *int   `json:"optional,omitempty"`
	}
	candidate, err := functiontool.New(functiontool.Config{Name: "write_file"}, func(_ tool.Context, _ args) (map[string]any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolArguments(candidate, map[string]any{"file_path": "ok"}); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("missing content error = %v", err)
	}
	if err := ValidateToolArguments(candidate, map[string]any{"file_path": "ok", "content": ""}); err != nil {
		t.Fatal(err)
	}
}
