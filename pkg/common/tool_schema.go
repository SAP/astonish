package common

import (
	"bytes"
	"encoding/json"
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
func CanonicalizeJSON(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return value
	}
	return canonicalizeJSONValue(decoded, "")
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
func CanonicalizeToolSchema(schema any) any {
	if schema == nil {
		return map[string]any{"properties": map[string]any{}, "type": "object"}
	}
	canonical := CanonicalizeJSON(schema)
	if object, ok := canonical.(map[string]any); ok && len(object) == 0 {
		return map[string]any{"properties": map[string]any{}, "type": "object"}
	}
	return canonical
}

// CanonicalizeRequestTools returns a shallow request copy with canonical tools.
func CanonicalizeRequestTools(req *model.LLMRequest) *model.LLMRequest {
	if req == nil || req.Config == nil || len(req.Config.Tools) == 0 {
		return req
	}
	result := *req
	config := *req.Config
	config.Tools = CanonicalizeTools(req.Config.Tools)
	result.Config = &config
	return &result
}

// CanonicalizeTools returns a copy of tools with function declarations sorted
// by name and schemas normalized for deterministic provider serialization.
func CanonicalizeTools(tools []*genai.Tool) []*genai.Tool {
	result := append([]*genai.Tool(nil), tools...)
	for toolIndex, source := range tools {
		if source == nil || source.FunctionDeclarations == nil {
			continue
		}
		clone := *source
		clone.FunctionDeclarations = make([]*genai.FunctionDeclaration, 0, len(source.FunctionDeclarations))
		for _, declaration := range source.FunctionDeclarations {
			if declaration == nil {
				clone.FunctionDeclarations = append(clone.FunctionDeclarations, nil)
				continue
			}
			copy := *declaration
			if copy.ParametersJsonSchema != nil {
				copy.ParametersJsonSchema = CanonicalizeToolSchema(copy.ParametersJsonSchema)
			} else if copy.Parameters != nil {
				copy.ParametersJsonSchema = CanonicalizeToolSchema(copy.Parameters)
				copy.Parameters = nil
			} else {
				copy.ParametersJsonSchema = CanonicalizeToolSchema(nil)
			}
			if copy.ResponseJsonSchema != nil {
				copy.ResponseJsonSchema = CanonicalizeJSON(copy.ResponseJsonSchema)
			} else if copy.Response != nil {
				copy.ResponseJsonSchema = CanonicalizeJSON(copy.Response)
				copy.Response = nil
			}
			clone.FunctionDeclarations = append(clone.FunctionDeclarations, &copy)
		}
		sort.SliceStable(clone.FunctionDeclarations, func(i, j int) bool {
			return compareDeclarations(clone.FunctionDeclarations[i], clone.FunctionDeclarations[j]) < 0
		})
		result[toolIndex] = &clone
	}

	sort.SliceStable(result, func(i, j int) bool {
		left, right := firstDeclaration(result[i]), firstDeclaration(result[j])
		if left == nil || right == nil {
			return left != nil
		}
		return compareDeclarations(left, right) < 0
	})
	return result
}

func firstDeclaration(tool *genai.Tool) *genai.FunctionDeclaration {
	if tool == nil || len(tool.FunctionDeclarations) == 0 {
		return nil
	}
	return tool.FunctionDeclarations[0]
}

func compareDeclarations(left, right *genai.FunctionDeclaration) int {
	if left == nil {
		if right == nil {
			return 0
		}
		return 1
	}
	if right == nil {
		return -1
	}
	if left.Name < right.Name {
		return -1
	}
	if left.Name > right.Name {
		return 1
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Compare(leftJSON, rightJSON)
}
