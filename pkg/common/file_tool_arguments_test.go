package common

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFileToolArgumentNormalization(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file", "read_file", "load_file"} {
		for _, tc := range []struct {
			name string
			args map[string]any
			want string
			bad  bool
		}{
			{"canonical", map[string]any{"path": " /tmp/a "}, " /tmp/a ", false},
			{"legacy", map[string]any{"file_path": "/tmp/a"}, "/tmp/a", false},
			{"matching", map[string]any{"path": "/tmp/a", "file_path": "/tmp/a"}, "/tmp/a", false},
			{"conflict", map[string]any{"path": "secret-a", "file_path": "secret-b"}, "", true},
			{"null", map[string]any{"path": nil, "file_path": "secret"}, "", true},
			{"wrong_type", map[string]any{"path": 42}, "", true},
			{"legacy_type", map[string]any{"path": "secret", "file_path": 42}, "", true},
			{"missing", nil, "", true},
			{"empty", map[string]any{"path": ""}, "", false},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				before, _ := json.Marshal(tc.args)
				got, err := NormalizeFileToolArguments(name, tc.args)
				if tc.bad {
					if err == nil || strings.Contains(err.Error(), "secret") {
						t.Fatalf("err=%v", err)
					}
				} else {
					if err != nil || got["path"] != tc.want {
						t.Fatalf("got=%v err=%v", got, err)
					}
					if _, exists := got["file_path"]; exists {
						t.Fatal("alias retained")
					}
				}
				if path := FileToolPath(tc.args); path != tc.want {
					t.Fatalf("path=%q", path)
				}
				after, _ := json.Marshal(tc.args)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("mutated arguments")
				}
			})
		}
	}
	args := map[string]any{"file_path": "anything"}
	got, err := NormalizeFileToolArguments("other_tool", args)
	if err != nil || !reflect.DeepEqual(got, args) {
		t.Fatal("changed unrelated tool")
	}
}
