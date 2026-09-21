package themes_test

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// TestTemplateImportStateRoundTrip asserts that all new import lifecycle fields
// survive a JSON marshal → unmarshal cycle without data loss.
func TestTemplateImportStateRoundTrip(t *testing.T) {
	orig := themes.Template{
		Schema:           2,
		Name:             "brand",
		Label:            "Brand",
		ImportState:      "validated",
		ImportIterations: 3,
		ImportWarnings:   []string{"table on slide 4 not supported", "chart on slide 7 not supported"},
		SourcePPTXBase64: "dGVzdA==", // "test" in base64
		Tokens:           map[string]string{"surface": "#FFFFFF", "ink": "#000000"},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got themes.Template
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ImportState != orig.ImportState {
		t.Errorf("ImportState: got %q, want %q", got.ImportState, orig.ImportState)
	}
	if got.ImportIterations != orig.ImportIterations {
		t.Errorf("ImportIterations: got %d, want %d", got.ImportIterations, orig.ImportIterations)
	}
	if len(got.ImportWarnings) != len(orig.ImportWarnings) {
		t.Errorf("ImportWarnings len: got %d, want %d", len(got.ImportWarnings), len(orig.ImportWarnings))
	} else {
		for i, w := range orig.ImportWarnings {
			if got.ImportWarnings[i] != w {
				t.Errorf("ImportWarnings[%d]: got %q, want %q", i, got.ImportWarnings[i], w)
			}
		}
	}
	if got.SourcePPTXBase64 != orig.SourcePPTXBase64 {
		t.Errorf("SourcePPTXBase64: got %q, want %q", got.SourcePPTXBase64, orig.SourcePPTXBase64)
	}
}

// TestWithoutSourceZeroesSourceField asserts that WithoutSource zeroes
// SourcePPTXBase64 while preserving all other fields.
func TestWithoutSourceZeroesSourceField(t *testing.T) {
	orig := themes.Template{
		Schema:           2,
		Name:             "brand",
		Label:            "Brand",
		ImportState:      "validated",
		ImportIterations: 1,
		ImportWarnings:   []string{"chart on slide 2 not supported"},
		SourcePPTXBase64: "dGVzdA==",
		Tokens:           map[string]string{"surface": "#FFFFFF"},
	}

	clean := orig.WithoutSource()

	if clean.SourcePPTXBase64 != "" {
		t.Errorf("WithoutSource: SourcePPTXBase64 should be empty, got %q", clean.SourcePPTXBase64)
	}
	// All other fields must be preserved.
	if clean.Name != orig.Name {
		t.Errorf("Name changed: got %q, want %q", clean.Name, orig.Name)
	}
	if clean.ImportState != orig.ImportState {
		t.Errorf("ImportState changed: got %q, want %q", clean.ImportState, orig.ImportState)
	}
	if clean.ImportIterations != orig.ImportIterations {
		t.Errorf("ImportIterations changed: got %d, want %d", clean.ImportIterations, orig.ImportIterations)
	}
	// Original must be unmodified.
	if orig.SourcePPTXBase64 != "dGVzdA==" {
		t.Errorf("WithoutSource must not mutate the receiver; orig.SourcePPTXBase64 = %q", orig.SourcePPTXBase64)
	}
}

// TestTemplateZeroImportState asserts that a Template with a zero ImportState
// (empty string) serializes and deserializes cleanly — the field is omitted
// from JSON (omitempty) and the deserialized value is the zero string.
func TestTemplateZeroImportState(t *testing.T) {
	orig := themes.Template{
		Schema: 2,
		Name:   "classic",
		Tokens: map[string]string{"surface": "#FFFFFF"},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Field must be omitted (omitempty).
	if string(data) == "" {
		t.Fatal("marshal returned empty bytes")
	}
	// The JSON must NOT contain importState when the zero value is used.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, present := raw["importState"]; present {
		t.Error("importState should be omitted from JSON when zero, but it was present")
	}

	var got themes.Template
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ImportState != "" {
		t.Errorf("ImportState: got %q, want empty string", got.ImportState)
	}
}
