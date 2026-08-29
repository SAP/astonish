package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// SanitizedToolset wraps a toolset and sanitizes tool schemas to fix
// common issues that cause provider rejections (e.g., object types
// without properties). This is needed in chat mode where all MCP tools
// are loaded, unlike flow mode which only uses specific tools.
type SanitizedToolset struct {
	underlying tool.Toolset
	debugMode  bool
}

// NewSanitizedToolset creates a toolset wrapper that fixes invalid schemas.
func NewSanitizedToolset(ts tool.Toolset, debugMode bool) *SanitizedToolset {
	return &SanitizedToolset{underlying: ts, debugMode: debugMode}
}

func (s *SanitizedToolset) Name() string {
	return s.underlying.Name()
}

func (s *SanitizedToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	underlyingTools, err := s.underlying.Tools(ctx)
	if err != nil {
		return nil, err
	}

	sanitized := make([]tool.Tool, 0, len(underlyingTools))
	for _, t := range underlyingTools {
		st := &sanitizedTool{Tool: t, debugMode: s.debugMode}
		if st.isValid() {
			sanitized = append(sanitized, st)
		} else if s.debugMode {
			slog.Debug("skipping tool with invalid schema", "component", "chat", "tool", t.Name())
		}
	}
	return sanitized, nil
}

// sanitizedTool wraps an individual tool and fixes its schema.
type sanitizedTool struct {
	tool.Tool
	debugMode bool
}

// Declaration returns a sanitized function declaration.
func (t *sanitizedTool) Declaration() *genai.FunctionDeclaration {
	dt, ok := t.Tool.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok {
		return nil
	}
	decl := dt.Declaration()
	if decl == nil {
		return nil
	}

	// Fix ParametersJsonSchema if present (raw JSON schema from MCP)
	if decl.ParametersJsonSchema != nil {
		decl.ParametersJsonSchema = sanitizeJSONSchema(decl.ParametersJsonSchema, t.debugMode, decl.Name)
	}

	// Fix Parameters if present (genai.Schema)
	if decl.Parameters != nil {
		sanitizeGenaiSchema(decl.Parameters)
	}

	return decl
}

// ProcessRequest packs the sanitized tool declaration into the LLM request.
// This replaces the underlying tool's ProcessRequest to ensure the sanitized
// Declaration() is used instead of the original (potentially broken) one.
// This replicates the logic from ADK's internal toolutils.PackTool.
func (t *sanitizedTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if req.Tools == nil {
		req.Tools = make(map[string]any)
	}

	name := t.Name()
	if _, ok := req.Tools[name]; ok {
		return fmt.Errorf("duplicate tool: %q", name)
	}
	req.Tools[name] = t

	decl := t.Declaration()
	if decl == nil {
		return nil
	}

	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}

	// Find an existing genai.Tool with FunctionDeclarations
	var funcTool *genai.Tool
	for _, gt := range req.Config.Tools {
		if gt != nil && gt.FunctionDeclarations != nil {
			funcTool = gt
			break
		}
	}
	if funcTool == nil {
		req.Config.Tools = append(req.Config.Tools, &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{decl},
		})
	} else {
		funcTool.FunctionDeclarations = append(funcTool.FunctionDeclarations, decl)
	}

	return nil
}

// isValid checks if the tool can be sent to a provider without errors.
func (t *sanitizedTool) isValid() bool {
	// All tools are valid after sanitization -- we fix rather than reject.
	// If we ever encounter unfixable schemas, we can reject here.
	return true
}

// Run delegates to the underlying tool's Run method.
// This is required for ADK's FunctionTool interface so the tool can be executed.
func (t *sanitizedTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	runner, ok := t.Tool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		return nil, fmt.Errorf("tool '%s' does not implement Run", t.Name())
	}
	return runner.Run(ctx, args)
}

// sanitizeJSONSchema fixes common issues in raw JSON schemas from MCP servers.
// Common issues:
//   - type "object" without a "properties" field
//   - properties missing a "type" field (breaks strict LLM templates like Nemotron)
func sanitizeJSONSchema(schema any, debugMode bool, toolName string) any {
	switch s := schema.(type) {
	case map[string]any:
		// Fix: object type without properties
		if typ, ok := s["type"]; ok && typ == "object" {
			if _, hasProp := s["properties"]; !hasProp {
				s["properties"] = map[string]any{}
				if debugMode {
					slog.Debug("fixed schema: added empty properties to object type", "component", "chat", "tool", toolName)
				}
			}
		}

		// Recursively fix nested schemas
		if props, ok := s["properties"].(map[string]any); ok {
			for key, val := range props {
				props[key] = sanitizeJSONSchema(val, debugMode, toolName+"."+key)
			}
		}
		if items, ok := s["items"]; ok {
			s["items"] = sanitizeJSONSchema(items, debugMode, toolName+".items")
		}
		if addl, ok := s["additionalProperties"]; ok {
			if addlMap, isMap := addl.(map[string]any); isMap {
				s["additionalProperties"] = sanitizeJSONSchema(addlMap, debugMode, toolName+".additionalProperties")
			}
		}

		// Fix: property missing "type" — strict LLM templates (e.g. Nemotron)
		// require every property to have an explicit type. Skip schemas that
		// express their type via composition keywords (anyOf, oneOf, allOf).
		if _, hasType := s["type"]; !hasType {
			if _, hasAnyOf := s["anyOf"]; !hasAnyOf {
				if _, hasOneOf := s["oneOf"]; !hasOneOf {
					if _, hasAllOf := s["allOf"]; !hasAllOf {
						// Only add default type if the schema has at least one
						// key (a truly empty schema {} means "any value" and
						// is typically the root, not a property).
						if len(s) > 0 {
							s["type"] = "string"
							if debugMode {
								slog.Debug("fixed schema: added default type string to typeless property", "component", "chat", "tool", toolName)
							}
						}
					}
				}
			}
		}

		return s

	case *map[string]any:
		if s == nil {
			return schema
		}
		result := sanitizeJSONSchema(*s, debugMode, toolName)
		*s = result.(map[string]any)
		return s

	default:
		// Try to round-trip through JSON to get a map we can inspect.
		// This handles cases where the schema is a struct or *jsonschema.Schema.
		data, err := json.Marshal(schema)
		if err != nil {
			return schema
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return schema
		}
		fixed := sanitizeJSONSchema(m, debugMode, toolName)
		return fixed
	}
}

// sanitizeGenaiSchema fixes common issues in genai.Schema objects.
func sanitizeGenaiSchema(schema *genai.Schema) {
	if schema == nil {
		return
	}
	if schema.Type == genai.TypeObject && schema.Properties == nil {
		schema.Properties = map[string]*genai.Schema{}
	}
	// Recurse into properties
	for _, prop := range schema.Properties {
		sanitizeGenaiSchema(prop)
	}
	if schema.Items != nil {
		sanitizeGenaiSchema(schema.Items)
	}
}
