package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"google.golang.org/adk/tool"
)

type dispatchParityContext struct{ catalogueContext }

func (dispatchParityContext) SessionID() string { return "fixed-owner" }
func (dispatchParityContext) AgentName() string { return "fixed-owner" }

type dispatchParityBackend struct {
	sandbox.Backend
	requests []map[string]any
	sessions []string
	response string
	failure  error
}

func (*dispatchParityBackend) CreateSession(_ context.Context, spec sandbox.SessionSpec) (*sandbox.Session, error) {
	return &sandbox.Session{}, nil
}
func (*dispatchParityBackend) StartSession(context.Context, string) error { return nil }
func (*dispatchParityBackend) SessionState(context.Context, string) (sandbox.SessionState, error) {
	return sandbox.SessionStateRunning, nil
}
func (*dispatchParityBackend) WaitForSessionReady(context.Context, string) error { return nil }
func (b *dispatchParityBackend) Exec(_ context.Context, id string, spec sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	raw, err := io.ReadAll(spec.Stdin)
	if err != nil {
		return nil, err
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	b.requests = append(b.requests, req)
	b.sessions = append(b.sessions, id)
	if b.failure != nil {
		return nil, b.failure
	}
	return &sandbox.ExecResult{Stdout: []byte("{\"ready\":true}\n{\"id\":\"1\",\"result\":" + b.response + "}\n")}, nil
}

// Exercise the actual backend pool/client/NDJSON transport, not a replacement
// ToolNodeClient, for ordinary pooled Chat and the fixed-session Bot wrapper.
func TestBotNodeDispatchBackendParity(t *testing.T) {
	for _, response := range []string{`{"content":"same"}`, `[1,2]`, `null`} {
		t.Run(response, func(t *testing.T) {
			backend := &dispatchParityBackend{response: response}
			pool := sandbox.NewBackendPool(backend, sandbox.ResourceLimits{})
			entries, err := BotCatalogueTools(pool.GetOrCreate("fixed-owner"), "fixed-owner")
			if err != nil {
				t.Fatal(err)
			}
			var fixed *botSandboxTool
			for _, entry := range entries {
				if entry.Name() == "read_file" {
					fixed = entry.(*botSandboxTool)
				}
			}
			if fixed == nil {
				t.Fatal("missing read_file")
			}
			pooled := sandbox.NewNodeToolWithPool(fixed.Tool, pool)
			ctx := dispatchParityContext{catalogueContext{botFactoryToolContext{base: context.Background()}}}
			for _, args := range []any{map[string]any{"file_path": "/workspace/a", "_caller": "escape"}, struct {
				Path string `json:"path"`
			}{Path: "/workspace/a"}} {
				before, _ := json.Marshal(args)
				chatResult, chatErr := pooled.Run(ctx, args)
				botResult, botErr := fixed.Run(ctx, args)
				if chatErr != nil || botErr != nil || !reflect.DeepEqual(chatResult, botResult) {
					t.Fatalf("chat=%v/%v bot=%v/%v", chatResult, chatErr, botResult, botErr)
				}
				n := len(backend.requests)
				if n < 2 || !reflect.DeepEqual(backend.requests[n-2], backend.requests[n-1]) {
					t.Fatalf("payload mismatch: %+v", backend.requests)
				}
				values := backend.requests[n-1]["args"].(map[string]any)
				if values["path"] != "/workspace/a" || values["_caller"] != "fixed-owner" || values["file_path"] != nil {
					t.Fatalf("args=%v", values)
				}
				after, _ := json.Marshal(args)
				if string(before) != string(after) {
					t.Fatal("mutated caller arguments")
				}
			}
			for _, args := range []any{nil, map[string]any{}, map[string]any{"path": nil}, map[string]any{"path": "a", "file_path": "b"}, []string{"invalid"}} {
				n := len(backend.requests)
				_, a := pooled.Run(ctx, args)
				_, b := fixed.Run(ctx, args)
				if a == nil || b == nil || a.Error() != b.Error() || len(backend.requests) != n {
					t.Fatalf("invalid args: %v / %v", a, b)
				}
			}
			backend.failure = errors.New("transport failed")
			for _, runner := range []tool.Tool{pooled, fixed} {
				_, err := runner.(botRunnableTool).Run(ctx, map[string]any{"path": "/workspace/a"})
				if !errors.Is(err, backend.failure) {
					t.Fatalf("lost transport error: %v", err)
				}
			}
			for _, id := range backend.sessions {
				if id != "fixed-owner" {
					t.Fatalf("escaped route: %s", id)
				}
			}
		})
	}
}

func TestBotNodeDispatchMissingAuthorityAndCancellation(t *testing.T) {
	client := &catalogueClient{}
	entries, err := BotCatalogueTools(client, "owner")
	if err != nil {
		t.Fatal(err)
	}
	var fixed *botSandboxTool
	for _, entry := range entries {
		if entry.Name() == "read_file" {
			fixed = entry.(*botSandboxTool)
		}
	}
	ctx := catalogueContext{botFactoryToolContext{base: context.Background()}}
	for _, c := range []sandbox.ToolNodeClient{nil, (*catalogueClient)(nil)} {
		copy := *fixed
		copy.client = c
		if _, err := copy.Run(ctx, map[string]any{"path": "/a"}); err == nil {
			t.Fatal("missing client accepted")
		}
	}
	copy := *fixed
	copy.sessionID = ""
	if _, err := copy.Run(ctx, map[string]any{"path": "/a"}); err == nil {
		t.Fatal("missing session accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixed.Run(catalogueContext{botFactoryToolContext{base: canceled}}, map[string]any{"path": "/a"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatal("unauthorized dispatch")
	}
}
