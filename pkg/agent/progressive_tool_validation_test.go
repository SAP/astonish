package agent

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Use an ADK-generated required-path declaration, but spy at Run entry rather
// than in its callback: ADK's own decoder must not mask a missing guard.
type progressiveValidationSpy struct {
	tool.Tool
	calls  int
	got    map[string]any
	output map[string]any
	err    error
}

func (s *progressiveValidationSpy) Declaration() *genai.FunctionDeclaration {
	return s.Tool.(interface {
		Declaration() *genai.FunctionDeclaration
	}).Declaration()
}
func (s *progressiveValidationSpy) Run(_ tool.Context, raw any) (map[string]any, error) {
	s.calls++
	s.got = raw.(map[string]any)
	return s.output, s.err
}
func newProgressiveValidationSpy(t *testing.T, name string) *progressiveValidationSpy {
	t.Helper()
	// Importing pkg/tools here would cycle back through pkg/agent. A typed
	// callback generates a genuine required-path schema without that dependency.
	type pathArgs struct {
		Path string `json:"path"`
	}
	base, err := functiontool.New(functiontool.Config{Name: name}, func(_ tool.Context, _ pathArgs) (map[string]any, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &progressiveValidationSpy{Tool: base, output: map[string]any{"success": true}}
}

func TestProgressiveValidationRejectsMissingPathBeforeRunner(t *testing.T) {
	for _, name := range []string{"read_file", "load_file"} {
		for _, surface := range []string{"bridge", "executor"} {
			t.Run(name+"/"+surface, func(t *testing.T) {
				spy := newProgressiveValidationSpy(t, name)
				idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{spy}})
				bridge, err := NewProgressiveToolBridge(idx)
				if err != nil {
					t.Fatal(err)
				}
				executor := NewProgressiveToolExecutor(&ChatAgent{ToolIndex: idx})
				ctx := &contextToolContext{minimalReadonlyContext{Context: context.Background()}}
				for _, args := range []map[string]any{{}, {"unknown_path": "must-not-be-echoed"}, {"path": nil}} {
					var message string
					if surface == "bridge" {
						_, err := bridge[1].(runnableDeferredTool).Run(ctx, map[string]any{"name": name, "arguments": args})
						if err != nil {
							message = err.Error()
						}
					} else {
						result := executor.Execute(ctx, name, args)
						if result.State != ProgressiveStateExecutionError {
							t.Fatalf("invalid call state=%s", result.State)
						}
						message = result.Error
					}
					if !strings.Contains(message, `required argument "path"`) || strings.Contains(message, "must-not-be-echoed") {
						t.Fatalf("unsafe or missing validation error: %q", message)
					}
					if spy.calls != 0 {
						t.Fatalf("invalid call reached runner %d times", spy.calls)
					}
				}
				valid := map[string]any{"path": "/tmp/report"}
				if surface == "bridge" {
					if _, err := bridge[1].(runnableDeferredTool).Run(ctx, map[string]any{"name": name, "arguments": valid}); err != nil {
						t.Fatal(err)
					}
				} else if result := executor.Execute(ctx, name, valid); result.State != ProgressiveStateReady {
					t.Fatalf("valid call: %+v", result)
				}
				if spy.calls != 1 || !reflect.DeepEqual(spy.got, valid) {
					t.Fatalf("valid call did not reach runner intact: calls=%d args=%v", spy.calls, spy.got)
				}
			})
		}
	}
}

func TestDecodeExecuteToolArgsTypedWrappers(t *testing.T) {
	args := map[string]any{"path": "/tmp/report"}
	direct := ExecuteToolArgs{Name: "load_file", Arguments: args}
	wrapped := ExecuteToolArgs{Name: executeToolName, Arguments: map[string]any{"name": "load_file", "arguments": args}}
	for _, raw := range []any{direct, &direct, wrapped, &wrapped} {
		decoded, err := decodeExecuteToolArgs(raw)
		if err != nil || decoded.Name != "load_file" || !reflect.DeepEqual(decoded.Arguments, args) {
			t.Fatalf("typed %T: %+v %v", raw, decoded, err)
		}
		decoded.Arguments["path"] = "/tmp/changed"
		if args["path"] != "/tmp/changed" {
			t.Fatal("typed decoder copied arguments")
		}
		args["path"] = "/tmp/report"
	}
	var nilArgs *ExecuteToolArgs
	if _, err := decodeExecuteToolArgs(nilArgs); err == nil {
		t.Fatal("nil typed envelope accepted")
	}
}

func TestChildEffectiveToolCallSelfWrapperMatchesExecutionAndDisabledScope(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disabled], func(t *testing.T) {
			spy := newProgressiveValidationSpy(t, "load_file")
			scope := &ChildToolScope{entries: []childToolEntry{{tool: spy, group: "core"}}}
			bridge, err := NewScopedProgressiveToolBridge(scope)
			if err != nil {
				t.Fatal(err)
			}
			base := context.Background()
			if disabled {
				base = store.WithDisabledTools(base, []string{"load_file"})
			}
			ctx := &contextToolContext{minimalReadonlyContext{Context: base}}
			args := map[string]any{"path": "/tmp/report"}
			raw := map[string]any{"name": executeToolName, "arguments": map[string]any{"name": "core/load_file", "arguments": args}}
			logical, effective, resolveErr := childEffectiveToolCall(ctx, scope, bridge[1], raw)
			_, runErr := bridge[1].(runnableDeferredTool).Run(ctx, raw)
			if disabled {
				if resolveErr == nil || runErr == nil || !strings.Contains(resolveErr.Error(), "disabled") || !strings.Contains(runErr.Error(), "disabled") || spy.calls != 0 {
					t.Fatalf("disabled resolution=%v execution=%v calls=%d", resolveErr, runErr, spy.calls)
				}
				return
			}
			if resolveErr != nil || runErr != nil || logical != spy || spy.calls != 1 || !reflect.DeepEqual(effective, spy.got) {
				t.Fatalf("logical/execution mismatch: logical=%v args=%v got=%v resolve=%v run=%v", logical, effective, spy.got, resolveErr, runErr)
			}
			effective["path"] = "/tmp/substituted"
			if args["path"] != "/tmp/substituted" || spy.got["path"] != "/tmp/substituted" {
				t.Fatal("authorization and execution do not share argument identity")
			}
		})
	}
}

func TestProgressiveExecutorAuthorizationAndDisabledPreventRunner(t *testing.T) {
	for _, mode := range []string{"disabled", "scope_denied", "scope_allowed"} {
		t.Run(mode, func(t *testing.T) {
			spy := newProgressiveValidationSpy(t, "load_file")
			idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{spy}})
			principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceMCP, Subject: "user", OrgSlug: "org", TeamSlug: "team", Authenticated: true}
			if mode != "scope_denied" {
				principal.Scopes = []string{string(execution.CapabilityToolExecute)}
			}
			ctx, err := execution.WithPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "disabled" {
				ctx = store.WithDisabledTools(ctx, []string{"load_file"})
			}
			result := NewProgressiveToolExecutor(&ChatAgent{ToolIndex: idx}).Execute(&contextToolContext{minimalReadonlyContext{Context: ctx}}, "load_file", map[string]any{"path": "/tmp/report"})
			switch mode {
			case "disabled":
				if result.State != ProgressiveStateExecutionError || !strings.Contains(result.Error, "disabled") {
					t.Fatalf("result=%+v", result)
				}
			case "scope_denied":
				if result.State != ProgressiveStateAuthorizationDenied || !strings.Contains(result.Error, "tool:execute") {
					t.Fatalf("result=%+v", result)
				}
			case "scope_allowed":
				if result.State != ProgressiveStateReady || spy.calls != 1 {
					t.Fatalf("result=%+v calls=%d", result, spy.calls)
				}
				return
			}
			if spy.calls != 0 {
				t.Fatalf("denied call reached runner %d times", spy.calls)
			}
		})
	}
}

func TestProgressiveExecutorOnlySuccessfulLoadFilePublishes(t *testing.T) {
	cases := []struct {
		name    string
		output  map[string]any
		err     error
		publish bool
	}{
		{"success", map[string]any{"success": true}, nil, true},
		{"legacy_success", map[string]any{"path": "ignored-response-path"}, nil, true},
		{"failed", map[string]any{"success": false}, nil, false},
		{"error_field", map[string]any{"error": "failed"}, nil, false},
		{"go_error", map[string]any{"success": true}, errors.New("failed"), false},
		{"nil_result", nil, nil, false},
	}
	for _, status := range []string{"pending_approval", "approval_required", "pending_authorization", "failed", "error", "denied"} {
		cases = append(cases, struct {
			name    string
			output  map[string]any
			err     error
			publish bool
		}{status, map[string]any{"status": status}, nil, false})
	}
	for _, name := range []string{"load_file", "read_file"} {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				spy := newProgressiveValidationSpy(t, name)
				spy.output, spy.err = tc.output, tc.err
				idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: []tool.Tool{spy}})
				chat := &ChatAgent{ToolIndex: idx}
				path := filepath.Join(t.TempDir(), "report.txt")
				result := NewProgressiveToolExecutor(chat).Execute(&contextToolContext{minimalReadonlyContext{Context: context.Background()}}, name, map[string]any{"path": path})
				if spy.calls != 1 {
					t.Fatalf("runner calls=%d result=%+v", spy.calls, result)
				}
				files := chat.DrainFiles()
				if tc.publish && name == "load_file" {
					if len(files) != 1 || files[0].Path != path || files[0].ToolName != "load_file" {
						t.Fatalf("files=%+v", files)
					}
				} else if len(files) != 0 {
					t.Fatalf("non-publication result leaked artifact: %+v", files)
				}
			})
		}
	}
}

// Exercise the actual child BeforeToolCallback via the existing fake-LLM seam:
// a self-wrapped mutation must authorize the inner tool, never the dispatcher.
func TestChildSelfWrappedToolAuthorization(t *testing.T) {
	for _, grant := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "granted"}[grant], func(t *testing.T) {
			var calls, gates, turns atomic.Int32
			inner, err := functiontool.New(functiontool.Config{Name: "write_file"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
				calls.Add(1)
				if args["file_path"] != "/tmp/project/result.txt" {
					t.Errorf("wrong executed args: %v", args)
				}
				return map[string]any{"success": true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			mgr := NewSubAgentManager(SubAgentConfig{MaxRetries: 1})
			mgr.AppName, mgr.UserID = "test", "user"
			mgr.SessionService = adksession.InMemoryService()
			mgr.ToolGroups = map[string]*ToolGroup{"core": {Name: "core", Tools: []tool.Tool{inner}}}
			policy := NewSessionAuthPolicy("/tmp/project")
			mgr.GetAuthPolicy = func(string) *SessionAuthPolicy { return policy }
			mgr.AuthorizationGate = func(req SubAgentAuthRequest) SubAgentAuthResponse {
				gates.Add(1)
				if req.ToolName != "write_file" || req.Args["file_path"] != "/tmp/project/result.txt" {
					t.Errorf("wrong logical authorization: %+v", req)
				}
				return SubAgentAuthResponse{Granted: grant, Choice: "Allow"}
			}
			mgr.LLM = livenessTestLLM{generate: func(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					part := &genai.Part{Text: "done"}
					if turns.Add(1) == 1 {
						part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "wrapped-write", Name: executeToolName, Args: map[string]any{"name": executeToolName, "arguments": map[string]any{"name": "write_file", "arguments": map[string]any{"file_path": "/tmp/project/result.txt", "content": "hello"}}}}}
					}
					yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, nil)
				}
			}}
			result := mgr.RunTask(context.Background(), SubAgentTask{Name: "writer", Description: "write", ParentID: "parent", ToolFilter: []string{"write_file"}})
			wantCalls := int32(0)
			if grant {
				wantCalls = 1
			}
			if result.Status != "success" || gates.Load() != 1 || calls.Load() != wantCalls {
				t.Fatalf("result=%+v gates=%d calls=%d", result, gates.Load(), calls.Load())
			}
		})
	}
}
