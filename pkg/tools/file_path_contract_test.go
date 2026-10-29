package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

func TestFileToolsCanonicalPathSequence(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, key := range []string{"path", "file_path"} {
			t.Run(key+map[bool]string{true: "/native", false: "/node"}[native], func(t *testing.T) {
				resetTestCache(t)
				old := currentCaller
				t.Cleanup(func() { currentCaller = old })
				registered, err := GetInternalTools()
				if err != nil {
					t.Fatal(err)
				}
				run := func(name string, args map[string]any) any {
					t.Helper()
					if native {
						for _, candidate := range registered {
							if candidate.Name() == name {
								result, err := candidate.(interface {
									Run(tool.Context, any) (map[string]any, error)
								}).Run(&toolContextStub{Context: context.Background()}, args)
								if err != nil {
									t.Fatal(err)
								}
								return result
							}
						}
						t.Fatalf("missing %s", name)
					}
					result, err := ExecuteTool(context.Background(), name, args, "chat")
					if err != nil {
						t.Fatal(err)
					}
					return result
				}
				path := filepath.Join(t.TempDir(), "hello.txt")
				run("write_file", map[string]any{key: path, "content": "hello"})
				run("edit_file", map[string]any{key: path, "old_string": "hello", "new_string": "hello world!"})
				if got := readTestFile(t, path); got != "hello world!" {
					t.Fatal(got)
				}
				run("read_file", map[string]any{key: path, "force": true})
				result := run("load_file", map[string]any{key: path})
				data, _ := json.Marshal(result)
				var metadata map[string]any
				_ = json.Unmarshal(data, &metadata)
				if metadata["size_bytes"] != float64(12) || metadata["content"] != nil {
					t.Fatalf("metadata=%s", data)
				}
			})
		}
	}
}

func TestFileToolsRejectConflictingPaths(t *testing.T) {
	resetTestCache(t)
	registered, err := GetInternalTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range registered {
		switch candidate.Name() {
		case "write_file", "edit_file", "read_file", "load_file":
		default:
			continue
		}
		args := map[string]any{"path": filepath.Join(t.TempDir(), "a"), "file_path": filepath.Join(t.TempDir(), "b"), "content": "hello", "old_string": "hello", "new_string": "world"}
		if err := common.ValidateToolArguments(candidate, args); err == nil {
			t.Errorf("%s pre-dispatch validation accepted conflict", candidate.Name())
		}
		if _, err := candidate.(interface {
			Run(tool.Context, any) (map[string]any, error)
		}).Run(&toolContextStub{Context: context.Background()}, args); err == nil {
			t.Errorf("%s native accepted conflict", candidate.Name())
		}
		if _, err := ExecuteTool(context.Background(), candidate.Name(), args, "chat"); err == nil {
			t.Errorf("%s node accepted conflict", candidate.Name())
		}
	}
}

func TestFileToolsAdvertiseCanonicalPath(t *testing.T) {
	ctx := &toolContextStub{Context: context.Background()}
	registered, err := GetInternalTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range registered {
		switch candidate.Name() {
		case "read_file", "write_file", "edit_file", "load_file":
		default:
			continue
		}
		req := &model.LLMRequest{}
		if err := candidate.(interface {
			ProcessRequest(tool.Context, *model.LLMRequest) error
		}).ProcessRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if req.Tools[candidate.Name()] != candidate {
			t.Fatal("native registration lost compatibility wrapper")
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(common.ExtractToolInputSchema(candidate), &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Properties["path"] == nil || schema.Properties["file_path"] != nil {
			t.Errorf("%s schema=%+v", candidate.Name(), schema)
		}
		required := false
		for _, key := range schema.Required {
			if key == "path" {
				required = true
			}
		}
		if !required {
			t.Errorf("%s path not required", candidate.Name())
		}
	}
}
