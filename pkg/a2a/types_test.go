package a2a

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestMessageUnmarshalKind verifies that parts are decoded using the A2A v1.0
// "kind" discriminator field.
func TestMessageUnmarshalKind(t *testing.T) {
	input := `{
		"role": "user",
		"parts": [
			{"kind": "text", "text": "hello"},
			{"kind": "file", "name": "a.txt", "mimeType": "text/plain", "uri": "file:///a.txt"},
			{"kind": "data", "mimeType": "application/json", "data": {"x": 1}}
		]
	}`

	var m Message
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(m.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(m.Parts))
	}

	tp, ok := m.Parts[0].(TextPart)
	if !ok {
		t.Fatalf("part[0]: expected TextPart, got %T", m.Parts[0])
	}
	if tp.Text != "hello" {
		t.Errorf("part[0].Text = %q, want %q", tp.Text, "hello")
	}

	fp, ok := m.Parts[1].(FilePart)
	if !ok {
		t.Fatalf("part[1]: expected FilePart, got %T", m.Parts[1])
	}
	if fp.Name != "a.txt" || fp.MimeType != "text/plain" || fp.URI != "file:///a.txt" {
		t.Errorf("part[1] = %+v, want name=a.txt mimeType=text/plain uri=file:///a.txt", fp)
	}

	dp, ok := m.Parts[2].(DataPart)
	if !ok {
		t.Fatalf("part[2]: expected DataPart, got %T", m.Parts[2])
	}
	if dp.MimeType != "application/json" {
		t.Errorf("part[2].MimeType = %q, want application/json", dp.MimeType)
	}
	if v, ok := dp.Data["x"]; !ok || v != float64(1) {
		t.Errorf("part[2].Data = %+v, want map[x:1]", dp.Data)
	}
}

// TestMessageUnmarshalTypeBackwardCompat verifies that the legacy "type"
// discriminator field is still decoded for backward compatibility.
func TestMessageUnmarshalTypeBackwardCompat(t *testing.T) {
	input := `{
		"role": "user",
		"parts": [
			{"type": "text", "text": "legacy"},
			{"type": "file", "name": "b.txt"},
			{"type": "data", "data": {"y": 2}}
		]
	}`

	var m Message
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(m.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(m.Parts))
	}

	tp, ok := m.Parts[0].(TextPart)
	if !ok {
		t.Fatalf("part[0]: expected TextPart, got %T", m.Parts[0])
	}
	if tp.Text != "legacy" {
		t.Errorf("part[0].Text = %q, want %q", tp.Text, "legacy")
	}

	if _, ok := m.Parts[1].(FilePart); !ok {
		t.Fatalf("part[1]: expected FilePart, got %T", m.Parts[1])
	}
	if _, ok := m.Parts[2].(DataPart); !ok {
		t.Fatalf("part[2]: expected DataPart, got %T", m.Parts[2])
	}
}

// TestMessageMarshalEmitsKind verifies that marshaling a Message emits the
// A2A v1.0 "kind" discriminator field (and not the legacy "type").
func TestMessageMarshalEmitsKind(t *testing.T) {
	m := Message{
		Role: "agent",
		Parts: []Part{
			TextPart{Text: "hi"},
			FilePart{Name: "c.txt", MimeType: "text/plain", URI: "file:///c.txt"},
			DataPart{MimeType: "application/json", Data: map[string]any{"z": 3}},
		},
	}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	out := string(raw)

	if !strings.Contains(out, `"kind":"text"`) {
		t.Errorf("expected marshaled output to contain kind:text, got %s", out)
	}
	if !strings.Contains(out, `"kind":"file"`) {
		t.Errorf("expected marshaled output to contain kind:file, got %s", out)
	}
	if !strings.Contains(out, `"kind":"data"`) {
		t.Errorf("expected marshaled output to contain kind:data, got %s", out)
	}
	if strings.Contains(out, `"type":`) {
		t.Errorf("expected marshaled output NOT to contain legacy type field, got %s", out)
	}
}

// TestMessageMarshalRoundTrip verifies that a Message survives a
// marshal/unmarshal cycle through the kind-based encoding.
func TestMessageMarshalRoundTrip(t *testing.T) {
	orig := Message{
		Role: "user",
		Parts: []Part{
			TextPart{Text: "round"},
			FilePart{Name: "d.txt", MimeType: "text/plain", URI: "file:///d.txt"},
			DataPart{MimeType: "application/json", Data: map[string]any{"n": float64(42)}},
		},
		Metadata: map[string]any{"trace": "abc"},
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var got Message
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round trip mismatch:\n orig = %+v\n  got = %+v", orig, got)
	}
}
