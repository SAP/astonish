package openai

// normalizeGeminiNullableTypes adapts nullable JSON Schema type arrays to the
// scalar type + nullable representation accepted by Gemini proxy translators.
// The caller owns the canonical request copy. Traverse schema positions only:
// defaults/examples and property names such as "type" are application data.
func normalizeGeminiNullableTypes(schema any) {
	m, ok := schema.(map[string]any)
	if !ok {
		return
	}
	if types, ok := m["type"].([]any); ok {
		nonNull := ""
		nullable, ambiguous := false, false
		for _, v := range types {
			s, ok := v.(string)
			if !ok {
				ambiguous = true
				continue
			}
			if s == "null" {
				nullable = true
				continue
			}
			switch s {
			case "array", "object", "string", "number", "integer", "boolean":
			default:
				ambiguous = true
			}
			if nonNull != "" && nonNull != s {
				ambiguous = true
			}
			nonNull = s
		}
		if nullable && nonNull != "" && !ambiguous {
			m["type"] = nonNull
			m["nullable"] = true
		}
	}
	for _, key := range []string{"properties", "$defs", "definitions", "patternProperties", "dependentSchemas"} {
		if entries, ok := m[key].(map[string]any); ok {
			for _, child := range entries {
				normalizeGeminiNullableTypes(child)
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "contains", "not", "if", "then", "else", "propertyNames", "unevaluatedProperties"} {
		switch child := m[key].(type) {
		case map[string]any:
			normalizeGeminiNullableTypes(child)
		case []any:
			for _, item := range child {
				normalizeGeminiNullableTypes(item)
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if children, ok := m[key].([]any); ok {
			for _, child := range children {
				normalizeGeminiNullableTypes(child)
			}
		}
	}
}
