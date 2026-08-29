package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

var setLikeSchemaArrays = map[string]bool{
	"enum":     true,
	"required": true,
	"type":     true,
}

// ExtractToolInputSchema extracts the input schema from a tool that implements Declaration().
// Returns nil if the tool doesn't have a declaration or input schema.
func ExtractToolInputSchema(t interface{ Name() string }) json.RawMessage {
	dt, ok := t.(ToolWithDeclaration)
	if !ok {
		return nil
	}
	decl := dt.Declaration()
	if decl == nil || decl.ParametersJsonSchema == nil {
		return nil
	}
	data, err := json.Marshal(decl.ParametersJsonSchema)
	if err != nil {
		return nil
	}
	if string(data) == "null" {
		return nil
	}
	return data
}

// CanonicalizeJSON recursively converts a JSON-compatible value to stable
// maps and slices. Arrays whose JSON Schema semantics are set-like are sorted
// and deduplicated; other arrays retain their declared order.
func CanonicalizeJSON(value any) (any, error) {
	if isNil(value) {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON value: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode JSON value: %w", err)
	}
	return canonicalizeJSONValue(decoded, ""), nil
}

func canonicalizeJSONValue(value any, key string) any {
	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			value[childKey] = canonicalizeJSONValue(child, childKey)
		}
		if schemaType, ok := value["type"].(string); ok && strings.EqualFold(schemaType, "object") {
			if _, ok := value["properties"]; !ok {
				value["properties"] = map[string]any{}
			}
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = canonicalizeJSONValue(child, "")
		}
		if setLikeSchemaArrays[key] {
			return sortAndDeduplicateJSON(value)
		}
		return value
	default:
		return value
	}
}

func sortAndDeduplicateJSON(values []any) []any {
	type encodedValue struct {
		value any
		json  string
	}
	encoded := make([]encodedValue, 0, len(values))
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return values
		}
		encoded = append(encoded, encodedValue{value: value, json: string(data)})
	}
	sort.SliceStable(encoded, func(i, j int) bool { return encoded[i].json < encoded[j].json })

	result := make([]any, 0, len(encoded))
	for i, item := range encoded {
		if i == 0 || item.json != encoded[i-1].json {
			result = append(result, item.value)
		}
	}
	return result
}

// CanonicalizeToolSchema normalizes a function parameter schema. Missing and
// empty root schemas become the explicit no-parameter object schema.
func CanonicalizeToolSchema(schema any) (any, error) {
	if isNil(schema) {
		return map[string]any{"properties": map[string]any{}, "type": "object"}, nil
	}
	canonical, err := CanonicalizeJSON(schema)
	if err != nil {
		return nil, err
	}
	if object, ok := canonical.(map[string]any); ok && len(object) == 0 {
		return map[string]any{"properties": map[string]any{}, "type": "object"}, nil
	}
	return canonical, nil
}

// CanonicalizeRequestTools returns a shallow request copy with canonical tools.
func CanonicalizeRequestTools(req *model.LLMRequest) (*model.LLMRequest, error) {
	if req == nil || req.Config == nil {
		return req, nil
	}
	result := *req
	config := *req.Config
	var err error
	config.Tools, err = CanonicalizeTools(req.Config.Tools)
	if err != nil {
		return nil, err
	}
	if config.ToolConfig != nil {
		toolConfig := *config.ToolConfig
		if toolConfig.FunctionCallingConfig != nil {
			functionConfig := *toolConfig.FunctionCallingConfig
			functionConfig.AllowedFunctionNames = sortedUniqueStrings(functionConfig.AllowedFunctionNames)
			toolConfig.FunctionCallingConfig = &functionConfig
		}
		config.ToolConfig = &toolConfig
	}
	result.Config = &config
	return &result, nil
}

// CanonicalizeTools returns a copy in which all function declarations occupy
// one deterministic tool while non-function tool semantics remain intact.
func CanonicalizeTools(tools []*genai.Tool) ([]*genai.Tool, error) {
	var declarations []*genai.FunctionDeclaration
	result := make([]*genai.Tool, 0, len(tools)+1)
	for _, source := range tools {
		if source == nil {
			continue
		}
		clone := *source
		clone.FunctionDeclarations = nil
		if hasNonFunctionSemantics(&clone) {
			result = append(result, &clone)
		}
		for _, declaration := range source.FunctionDeclarations {
			canonical, err := canonicalizeDeclaration(declaration)
			if err != nil {
				return nil, err
			}
			if canonical != nil {
				declarations = append(declarations, canonical)
			}
		}
	}

	sort.Slice(declarations, func(i, j int) bool {
		return compareDeclarations(declarations[i], declarations[j]) < 0
	})
	if len(declarations) > 0 {
		result = append(result, &genai.Tool{FunctionDeclarations: declarations})
	}

	type encodedTool struct {
		tool *genai.Tool
		json []byte
	}
	encoded := make([]encodedTool, len(result))
	for i, tool := range result {
		data, err := json.Marshal(tool)
		if err != nil {
			return nil, fmt.Errorf("marshal tool %d: %w", i, err)
		}
		encoded[i] = encodedTool{tool: tool, json: data}
	}
	sort.Slice(encoded, func(i, j int) bool { return bytes.Compare(encoded[i].json, encoded[j].json) < 0 })
	for i := range encoded {
		result[i] = encoded[i].tool
	}
	return result, nil
}

func canonicalizeDeclaration(declaration *genai.FunctionDeclaration) (*genai.FunctionDeclaration, error) {
	if declaration == nil {
		return nil, nil
	}
	result := *declaration
	parameterSchema := result.ParametersJsonSchema
	if isNil(parameterSchema) {
		parameterSchema = result.Parameters
	}
	var err error
	result.ParametersJsonSchema, err = CanonicalizeToolSchema(parameterSchema)
	if err != nil {
		return nil, fmt.Errorf("canonicalize parameters for function %q: %w", result.Name, err)
	}
	result.Parameters = nil

	responseSchema := result.ResponseJsonSchema
	if isNil(responseSchema) {
		responseSchema = result.Response
	}
	result.ResponseJsonSchema, err = CanonicalizeJSON(responseSchema)
	if err != nil {
		return nil, fmt.Errorf("canonicalize response for function %q: %w", result.Name, err)
	}
	result.Response = nil
	return &result, nil
}

func hasNonFunctionSemantics(tool *genai.Tool) bool {
	copy := *tool
	copy.FunctionDeclarations = nil
	return !reflect.ValueOf(copy).IsZero()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func sortedUniqueStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write == 0 || value != result[write-1] {
			result[write] = value
			write++
		}
	}
	return result[:write]
}

func compareDeclarations(left, right *genai.FunctionDeclaration) int {
	if left.Name < right.Name {
		return -1
	}
	if left.Name > right.Name {
		return 1
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return 0
	}
	return bytes.Compare(leftJSON, rightJSON)
}
