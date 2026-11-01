package common

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/adk/tool"
)

// ValidateToolArguments checks only top-level required argument presence and
// explicit nullability. It deliberately leaves optional fields, unknown keys,
// non-null values, and nested schemas to the tool's existing decoder/validator.
// Tools without an input declaration retain their existing behavior.
func ValidateToolArguments(t tool.Tool, args map[string]any) error {
	data := ExtractToolInputSchema(t)
	if len(data) == 0 {
		// Some declarations use the legacy typed Parameters field instead of
		// ParametersJsonSchema. Keep the shared extractor's behavior unchanged.
		if declared, ok := t.(ToolWithDeclaration); ok {
			if decl := declared.Declaration(); decl != nil && decl.Parameters != nil {
				var err error
				data, err = json.Marshal(decl.Parameters)
				if err != nil {
					return fmt.Errorf("tool %q: cannot inspect input schema", t.Name())
				}
			}
		}
	}
	if len(data) == 0 {
		return nil
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return fmt.Errorf("tool %q: cannot inspect input schema", t.Name())
	}
	if _, canonicalPath := schema.Properties["path"]; canonicalPath {
		var err error
		args, err = NormalizeFileToolArguments(t.Name(), args)
		if err != nil {
			return err
		}
	}
	// Sort our decoded copy, not the declaration, for stable, value-free errors.
	sort.Strings(schema.Required)
	for _, name := range schema.Required {
		value, present := args[name]
		if !present {
			return fmt.Errorf("tool %q: required argument %q is missing", t.Name(), name)
		}
		if value == nil && !toolArgumentAllowsNull(schema.Properties[name]) {
			return fmt.Errorf("tool %q: required argument %q must not be null", t.Name(), name)
		}
	}
	return nil
}

// toolArgumentAllowsNull recognizes explicit nullable forms used by ADK and
// JSON schemas. It is not a general JSON-schema evaluator.
func toolArgumentAllowsNull(raw json.RawMessage) bool {
	var schema struct {
		Nullable bool              `json:"nullable"`
		Type     json.RawMessage   `json:"type"`
		AnyOf    []json.RawMessage `json:"anyOf"`
		OneOf    []json.RawMessage `json:"oneOf"`
	}
	if json.Unmarshal(raw, &schema) != nil {
		return false
	}
	if schema.Nullable {
		return true
	}
	var typeName string
	if json.Unmarshal(schema.Type, &typeName) == nil && strings.EqualFold(typeName, "null") {
		return true
	}
	var typeNames []string
	if json.Unmarshal(schema.Type, &typeNames) == nil {
		for _, name := range typeNames {
			if strings.EqualFold(name, "null") {
				return true
			}
		}
	}
	for _, variants := range [][]json.RawMessage{schema.AnyOf, schema.OneOf} {
		for _, variant := range variants {
			if toolArgumentAllowsNull(variant) {
				return true
			}
		}
	}
	return false
}
