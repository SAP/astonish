package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// TestCaptureToolArtifacts checks the shared capture policy and progressive
// finalizer together, including every existing artifact source.
func TestCaptureToolArtifacts(t *testing.T) {
	for _, name := range []string{"load_file", "write_file", "edit_file", "browser_stop_recording", "run_drill", "read_file"} {
		t.Run(name, func(t *testing.T) {
			candidate, err := functiontool.New(functiontool.Config{Name: name, Description: "artifact fixture"},
				func(tool.Context, struct{}) (map[string]any, error) { return nil, nil })
			if err != nil {
				t.Fatal(err)
			}
			input := map[string]any{"path": "input.txt"}
			path := "input.txt"
			if name == "write_file" {
				input = map[string]any{"file_path": "written.txt"}
				path = "written.txt"
			}
			if name == "browser_stop_recording" || name == "run_drill" {
				path = "output.mp4"
			}
			var want []FileArtifact
			if name != "read_file" {
				want = []FileArtifact{{Path: resolveAbsPath(path), ToolName: name}}
			}
			for _, state := range []struct {
				name    string
				fields  map[string]any
				err     error
				absent  bool
				success bool
			}{
				{name: "legacy success", success: true},
				{name: "explicit success", fields: map[string]any{"success": true, "status": "success"}, success: true},
				{name: "pending approval", fields: map[string]any{"status": "pending_approval"}},
				{name: "pending authorization", fields: map[string]any{"status": "pending_authorization"}},
				{name: "approval required", fields: map[string]any{"status": "approval_required"}},
				{name: "error field", fields: map[string]any{"error": "failed"}},
				{name: "false success", fields: map[string]any{"success": false}},
				{name: "failed status", fields: map[string]any{"status": "failed"}},
				{name: "Go error", err: errors.New("failed")},
				{name: "nil output", absent: true},
			} {
				t.Run(state.name, func(t *testing.T) {
					output := map[string]any{"path": "output.mp4", "artifact_paths": []any{"output.mp4", "output.mp4"}}
					for k, v := range state.fields {
						output[k] = v
					}
					if state.absent {
						output = nil
					}
					expected := want
					if !state.success {
						expected = nil
					}
					chat := &ChatAgent{}
					chat.captureToolArtifacts(candidate, input, output, state.err)
					if got := chat.DrainFiles(); !reflect.DeepEqual(got, expected) {
						t.Fatalf("capture = %+v, want %+v", got, expected)
					}
					if got := chat.finalizeProgressiveToolOutput(candidate, input, output, state.err); !reflect.DeepEqual(got, output) {
						t.Fatalf("finalizer changed output: %+v", got)
					}
					if got := chat.DrainFiles(); !reflect.DeepEqual(got, expected) {
						t.Fatalf("progressive = %+v, want %+v", got, expected)
					}
				})
			}
			chat := &ChatAgent{}
			chat.captureToolArtifacts(candidate, nil, map[string]any{}, nil)
			chat.captureToolArtifacts(candidate, map[string]any{"path": "", "file_path": 123}, map[string]any{"path": ""}, nil)
			if got := chat.DrainFiles(); len(got) != 0 {
				t.Fatalf("missing paths published: %+v", got)
			}
			chat.captureToolArtifacts(nil, input, map[string]any{}, nil)
			var absent *ChatAgent
			absent.captureToolArtifacts(candidate, input, map[string]any{}, nil)
		})
	}
}

// Exercise the actual ChatAgent.Run AfterToolCallback, not just the progressive
// finalizer: the native callback previously published every nil-error result.
func TestFileArtifactNativeCallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		toolName  string
		pathKey   string
		output    map[string]any
		err       error
		wantFiles int
	}{
		{name: "pending approval", toolName: "load_file", output: map[string]any{"status": "pending_approval"}},
		{name: "approval required", toolName: "load_file", output: map[string]any{"status": "approval_required"}},
		{name: "pending authorization", toolName: "load_file", output: map[string]any{"status": "pending_authorization"}},
		{name: "nil output", toolName: "load_file"},
		{name: "structured error", toolName: "load_file", output: map[string]any{"error": "missing file"}},
		{name: "failed status", toolName: "load_file", output: map[string]any{"status": "failed"}},
		{name: "false success", toolName: "load_file", output: map[string]any{"success": false}},
		{name: "execution error", toolName: "load_file", output: map[string]any{"status": "success"}, err: errors.New("execution failed")},
		{name: "successful load", toolName: "load_file", output: map[string]any{"size_bytes": 0, "total_lines": 0}, wantFiles: 1},
		{name: "successful canonical write", toolName: "write_file", pathKey: "path", output: map[string]any{"status": "success"}, wantFiles: 1},
		{name: "successful historical write", toolName: "write_file", pathKey: "file_path", output: map[string]any{"status": "success"}, wantFiles: 1},
		{name: "successful read", toolName: "read_file", output: map[string]any{"content": "inspection only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.txt")
			pathKey := tc.pathKey
			if pathKey == "" {
				pathKey = "path"
			}
			var executions atomic.Int32
			candidate, err := functiontool.New(functiontool.Config{Name: tc.toolName, Description: "file artifact fixture"},
				func(_ tool.Context, _ struct {
					Path     string `json:"path,omitempty"`
					FilePath string `json:"file_path,omitempty"`
				}) (map[string]any, error) {
					executions.Add(1)
					return tc.output, tc.err
				})
			if err != nil {
				t.Fatal(err)
			}
			llm := &memoryCaptureModel{respond: func(_ context.Context, i int, _ *model.LLMRequest) (*model.LLMResponse, error) {
				switch i {
				case 0:
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
						ID: "file-call", Name: tc.toolName, Args: map[string]any{pathKey: path},
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
			for _, runErr := range runner.Run(context.Background(), "u", "artifact", genai.NewContentFromText("inspect the file", genai.RoleUser), adkagent.RunConfig{}) {
				if runErr != nil && tc.err == nil {
					t.Fatalf("Run: %v", runErr)
				}
			}
			if executions.Load() != 1 {
				t.Fatalf("tool executions = %d, want 1", executions.Load())
			}
			files := chat.DrainFiles()
			if len(files) != tc.wantFiles {
				t.Fatalf("artifacts = %+v, want %d", files, tc.wantFiles)
			}
			if len(files) == 1 && (files[0].Path != path || files[0].ToolName != tc.toolName) {
				t.Fatalf("artifact = %+v", files[0])
			}
		})
	}
}
