package api

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"
)

// Both restored artifact projections and offline content recovery must accept
// current calls and historical transcripts without changing their arguments.
func TestFilePathAliasPublication(t *testing.T) {
	const path = "/tmp/alias.txt"
	for _, name := range []string{"write_file", "edit_file", "load_file"} {
		for _, key := range []string{"path", "file_path"} {
			events := testEvents{
				artifactToolCallEvent("inv", "call", name, map[string]any{key: path}),
				toolResponseEvent("inv", "call", name, map[string]any{"success": true}),
			}
			if got := collectArtifacts(events); len(got) != 1 || got[0].Path != path {
				t.Errorf("%s/%s artifacts=%+v", name, key, got)
			}
		}
	}
	events := testEvents{
		artifactToolCallEvent("inv", "write", "write_file", map[string]any{"path": path, "content": "hello"}),
		toolResponseEvent("inv", "write", "write_file", map[string]any{"success": true}),
		artifactToolCallEvent("inv", "edit", "edit_file", map[string]any{"file_path": path, "old_string": "hello", "new_string": "hello world!"}),
		toolResponseEvent("inv", "edit", "edit_file", map[string]any{"success": true}),
	}
	if content, known := readArtifactContentFromEvents(events, path); !known || content != "hello world!" {
		t.Fatalf("content=%q known=%v", content, known)
	}
}

func TestWriteFilePathConsumers(t *testing.T) {
	const path = "/tmp/path-consumer-report.md"
	for _, tc := range []struct {
		name  string
		args  map[string]any
		valid bool
	}{
		{"canonical", map[string]any{"path": path}, true},
		{"historical", map[string]any{"file_path": path}, true},
		{"matching", map[string]any{"path": path, "file_path": path}, true},
		{"conflicting", map[string]any{"path": "/tmp/other.md", "file_path": path}, false},
		{"non-string canonical", map[string]any{"path": 123, "file_path": path}, false},
		{"non-string legacy", map[string]any{"path": path, "file_path": 123}, false},
		{"empty canonical", map[string]any{"path": "", "file_path": path}, false},
		{"missing", map[string]any{}, false},
	} {
		for _, envelope := range []string{"native", "object", "string"} {
			t.Run(tc.name+"/"+envelope, func(t *testing.T) {
				args := maps.Clone(tc.args)
				args["content"] = `{"stdout":"# Recovered report","exit_code":0}`
				original := maps.Clone(args)
				callName := "write_file"
				callArgs := args
				if envelope != "native" {
					callName = "execute_tool"
					var inner any = args
					if envelope == "string" {
						encoded, err := json.Marshal(args)
						if err != nil {
							t.Fatal(err)
						}
						inner = string(encoded)
					}
					callArgs = map[string]any{"name": "write_file", "arguments": inner}
				}
				events := testEvents{
					artifactToolCallEvent("inv", "call", callName, callArgs),
					toolResponseEvent("inv", "call", callName, map[string]any{"status": "success"}),
				}
				want := 0
				if tc.valid {
					want = 1
				}
				artifacts := collectArtifacts(events)
				if len(artifacts) != want {
					t.Errorf("Files artifacts = %+v, want %d", artifacts, want)
				} else if want == 1 && (artifacts[0].Path != path || artifacts[0].ToolName != "write_file") {
					t.Errorf("Files artifact identity = %+v", artifacts[0])
				}
				count := 0
				for _, message := range eventsToMessages(events, nil) {
					if message.Type == "artifact" {
						count++
						if message.Content != path || message.ToolName != "write_file" {
							t.Errorf("restored artifact = %+v", message)
						}
					}
				}
				if count != want {
					t.Errorf("restored artifact count = %d, want %d", count, want)
				}
				content, known := readArtifactContentFromEvents(events, path)
				if known != tc.valid || (known && content != "# Recovered report") {
					t.Errorf("content recovery = %q, %v", content, known)
				}
				if !reflect.DeepEqual(args, original) {
					t.Errorf("consumer mutated arguments: %+v", args)
				}
			})
		}
	}
}
