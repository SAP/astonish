package agent

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestFileAliasArtifacts(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file", "load_file"} {
		candidate, err := functiontool.New(functiontool.Config{Name: name}, func(tool.Context, struct{}) (map[string]any, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		chat := &ChatAgent{}
		chat.captureToolArtifacts(candidate, map[string]any{"file_path": "/tmp/alias.txt"}, map[string]any{"success": true}, nil)
		if got := chat.DrainFiles(); len(got) != 1 || got[0].Path != "/tmp/alias.txt" {
			t.Fatalf("%s: %+v", name, got)
		}
	}
}

func TestWriteFileArtifactPaths(t *testing.T) {
	const path = "/tmp/write-artifact.md"
	candidate, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "artifact fixture"},
		func(tool.Context, struct{}) (map[string]any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := maps.Clone(tc.args)
			chat := &ChatAgent{}
			output := map[string]any{"status": "success"}
			var want []FileArtifact
			if tc.valid {
				want = []FileArtifact{{Path: path, ToolName: "write_file"}}
			}
			chat.captureToolArtifacts(candidate, tc.args, output, nil)
			if got := chat.DrainFiles(); !reflect.DeepEqual(got, want) {
				t.Errorf("native capture = %+v, want %+v", got, want)
			}
			chat.finalizeProgressiveToolOutput(candidate, tc.args, output, nil)
			if got := chat.DrainFiles(); !reflect.DeepEqual(got, want) {
				t.Errorf("progressive capture = %+v, want %+v", got, want)
			}
			if !reflect.DeepEqual(tc.args, original) {
				t.Errorf("capture mutated arguments: %+v", tc.args)
			}
		})
	}
}

func TestWriteFileContinuationPath(t *testing.T) {
	for _, key := range []string{"path", "file_path"} {
		t.Run(key, func(t *testing.T) {
			const path = "/tmp/oversized-report.md"
			executions := 0
			candidate, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "continuation fixture"},
				func(tool.Context, struct {
					Path     string `json:"path,omitempty"`
					FilePath string `json:"file_path,omitempty"`
					Content  string `json:"content"`
				}) (map[string]any, error) {
					executions++
					return map[string]any{"status": "success"}, nil
				})
			if err != nil {
				t.Fatal(err)
			}
			llm := &memoryCaptureModel{respond: func(_ context.Context, i int, _ *model.LLMRequest) (*model.LLMResponse, error) {
				switch i {
				case 0:
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
						ID: "write", Name: "write_file", Args: map[string]any{key: path, "content": strings.Repeat("x", directWriteMaxBytes+1)},
					}}}}, TurnComplete: true}, nil
				case 1:
					return &model.LLMResponse{Content: genai.NewContentFromText("done", genai.RoleModel), TurnComplete: true}, nil
				default:
					return nil, fmt.Errorf("unexpected model round %d", i)
				}
			}}
			service := session.InMemoryService()
			chat := NewChatAgent(llm, []tool.Tool{candidate}, nil, service, &SystemPromptBuilder{CustomPrompt: "CORE"}, false, false)
			runner := memoryTestRunner(t, chat, service)
			found := false
			for event, runErr := range runner.Run(context.Background(), "u", "artifact", genai.NewContentFromText("write a report", genai.RoleUser), adkagent.RunConfig{}) {
				if runErr != nil {
					t.Fatal(runErr)
				}
				if event == nil || event.Content == nil {
					continue
				}
				for _, part := range event.Content.Parts {
					if part.FunctionResponse == nil {
						continue
					}
					response := part.FunctionResponse.Response
					if response["status"] == "continuation_marker_required" {
						found = true
						if response["path"] != path {
							t.Errorf("continuation path = %v, want %s", response["path"], path)
						}
					}
				}
			}
			if !found {
				t.Error("missing continuation-marker response")
			}
			if executions != 0 {
				t.Errorf("oversized write ran %d times", executions)
			}
			if got := chat.DrainFiles(); len(got) != 0 {
				t.Errorf("rejected write published artifacts: %+v", got)
			}
		})
	}
}
