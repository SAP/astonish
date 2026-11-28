package sandbox

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestDecodeBotHomeFileLine(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		want       error
		valid      bool
	}{
		{"missing", `{"error":{"code":"not_found"}}`, store.ErrBotNotFound, false},
		{"forbidden", `{"error":{"code":"forbidden"}}`, store.ErrBotForbidden, false},
		{"failed", `{"error":{"code":"failed"}}`, nil, false},
		{"unknown", `{"error":{"code":"ENOENT"}}`, nil, false},
		{"null error", `{"error":null}`, nil, false},
		{"empty error", `{"error":{}}`, nil, false},
		{"mixed", `{"error":{"code":"not_found"},"path":"x"}`, nil, false},
		{"malformed", `{"error":`, nil, false},
		{"null", `null`, nil, false},
		{"array", `[]`, nil, false},
		{"entry", `{"path":"SOUL.md","size_bytes":2}`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entry store.BotFileEntry
			err := DecodeBotHomeFileLine([]byte(tc.wire), &entry)
			if tc.valid {
				if err != nil || entry.Path != "SOUL.md" {
					t.Fatalf("entry=%+v err=%v", entry, err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid/error response accepted")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if tc.want == nil && errors.Is(err, store.ErrBotNotFound) {
				t.Fatal("non-missing error misclassified")
			}
		})
	}
}
