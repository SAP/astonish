package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/a2a"
)

func TestDecodeA2AV1SendMessageParamsParts(t *testing.T) {
	tests := []struct {
		name    string
		part    string
		assert  func(*testing.T, a2a.Part)
		wantErr string
	}{
		{
			name: "empty text with kind",
			part: `{"kind":"text","text":""}`,
			assert: func(t *testing.T, part a2a.Part) {
				t.Helper()
				text, ok := part.(a2a.TextPart)
				if !ok || text.Text != "" {
					t.Fatalf("part = %#v, want empty TextPart", part)
				}
			},
		},
		{
			name: "legacy empty text without kind",
			part: `{"text":""}`,
			assert: func(t *testing.T, part a2a.Part) {
				t.Helper()
				if _, ok := part.(a2a.TextPart); !ok {
					t.Fatalf("part = %#v, want TextPart", part)
				}
			},
		},
		{
			name: "explicit kind wins over other fields",
			part: `{"kind":"data","text":"ignored","data":{"answer":42}}`,
			assert: func(t *testing.T, part a2a.Part) {
				t.Helper()
				data, ok := part.(a2a.DataPart)
				if !ok || data.Data["answer"] != float64(42) {
					t.Fatalf("part = %#v, want DataPart", part)
				}
			},
		},
		{
			name: "file",
			part: `{"kind":"file","file":{"name":"brief.txt","mimeType":"text/plain","uri":"https://example.test/brief.txt","bytes":"aGk="}}`,
			assert: func(t *testing.T, part a2a.Part) {
				t.Helper()
				file, ok := part.(a2a.FilePart)
				if !ok || file.Name != "brief.txt" || file.MimeType != "text/plain" || file.URI != "https://example.test/brief.txt" || string(file.Bytes) != "hi" {
					t.Fatalf("part = %#v, want decoded FilePart", part)
				}
			},
		},
		{
			name:    "ambiguous legacy part",
			part:    `{"text":"hello","data":{"answer":42}}`,
			wantErr: "exactly one of text, data, or file",
		},
		{
			name:    "unsupported explicit kind",
			part:    `{"kind":"audio","text":"ignored"}`,
			wantErr: `unsupported message part kind "audio"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := json.RawMessage(`{"message":{"role":"ROLE_USER","parts":[` + tt.part + `]}}`)
			decoded, err := decodeA2AV1SendMessageParams(params)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(decoded.Message.Parts) != 1 {
				t.Fatalf("parts = %d, want 1", len(decoded.Message.Parts))
			}
			tt.assert(t, decoded.Message.Parts[0])
		})
	}
}

func TestEncodeA2AV1MessageMetadata(t *testing.T) {
	withoutMetadata := encodeA2AV1Message(a2a.Message{Role: "agent"}, "message-1")
	if _, ok := withoutMetadata["metadata"]; ok {
		t.Fatalf("nil metadata was encoded: %#v", withoutMetadata)
	}

	metadata := map[string]any{"source": "test"}
	withMetadata := encodeA2AV1Message(a2a.Message{Role: "agent", Metadata: metadata}, "message-2")
	got, ok := withMetadata["metadata"].(map[string]any)
	if !ok || got["source"] != "test" {
		t.Fatalf("metadata = %#v, want preserved metadata", withMetadata["metadata"])
	}
}
