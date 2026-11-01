package openai

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeGeminiNullableTypes(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"array", `{"type":["null","array"],"items":{"type":["object","null"],"properties":{"n":{"type":["integer","null"]}},"required":["n"]}}`, `{"type":"array","nullable":true,"items":{"type":"object","nullable":true,"properties":{"n":{"type":"integer","nullable":true}},"required":["n"]}}`},
		{"ambiguous", `{"type":["null","string","number"]}`, `{"type":["null","string","number"]}`},
		{"only null", `{"type":["null"]}`, `{"type":["null"]}`},
		{"invalid", `{"type":["null",3]}`, `{"type":["null",3]}`},
		{"unknown", `{"type":["null","custom"]}`, `{"type":["null","custom"]}`},
		{"non-null union", `{"type":["integer","number"]}`, `{"type":["integer","number"]}`},
		{"schema positions", `{"anyOf":[{"type":["string","null"]}],"additionalProperties":{"type":["boolean","null"]},"$defs":{"a":{"type":["number","null"]}}}`, `{"anyOf":[{"type":"string","nullable":true}],"additionalProperties":{"type":"boolean","nullable":true},"$defs":{"a":{"type":"number","nullable":true}}}`},
		{"application data", `{"type":"object","properties":{"type":{"type":["null","string"]}},"default":{"type":["null","array"]},"examples":[{"type":["null","array"]}],"enum":[{"type":["null","array"]}]}`, `{"type":"object","properties":{"type":{"type":"string","nullable":true}},"default":{"type":["null","array"]},"examples":[{"type":["null","array"]}],"enum":[{"type":["null","array"]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got, want any
			if err := json.Unmarshal([]byte(tc.input), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			normalizeGeminiNullableTypes(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
			normalizeGeminiNullableTypes(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatal("not idempotent")
			}
		})
	}
}
