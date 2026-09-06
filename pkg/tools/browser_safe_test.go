package tools

import (
	"context"
	"fmt"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// toolContextStub implements tool.Context (= agent.ToolContext) and context.Context.
// Embedding context.Context satisfies the context.Context portion of ReadonlyContext
// and makes the type assertion `any(ctx).(context.Context)` in
// ensureBrowserSessionAndContext succeed.
type toolContextStub struct {
	context.Context
	sessionID string
}

// --- ReadonlyContext methods ---

func (s *toolContextStub) UserContent() *genai.Content                    { return nil }
func (s *toolContextStub) InvocationID() string                           { return "" }
func (s *toolContextStub) AgentName() string                              { return "" }
func (s *toolContextStub) ReadonlyState() session.ReadonlyState           { return stubReadonlyState{} }
func (s *toolContextStub) UserID() string                                 { return "" }
func (s *toolContextStub) AppName() string                                { return "" }
func (s *toolContextStub) SessionID() string                              { return s.sessionID }
func (s *toolContextStub) Branch() string                                 { return "" }

// --- CallbackContext methods ---

func (s *toolContextStub) Artifacts() agent.Artifacts  { return nil }
func (s *toolContextStub) State() session.State        { return stubState{} }

// --- ToolContext methods ---

func (s *toolContextStub) FunctionCallID() string                                                  { return "" }
func (s *toolContextStub) Actions() *session.EventActions                                          { return nil }
func (s *toolContextStub) SearchMemory(_ context.Context, _ string) (*memory.SearchResponse, error) { return nil, nil }
func (s *toolContextStub) ToolConfirmation() *toolconfirmation.ToolConfirmation                    { return nil }
func (s *toolContextStub) RequestConfirmation(_ string, _ any) error                               { return nil }

// stubReadonlyState satisfies session.ReadonlyState.
type stubReadonlyState struct{}

func (stubReadonlyState) Get(string) (any, error)    { return nil, nil }
func (stubReadonlyState) All() iter.Seq2[string, any] { return func(yield func(string, any) bool) {} }

// stubState satisfies session.State.
type stubState struct{ stubReadonlyState }

func (stubState) Set(string, any) error { return nil }

// ---------------------------------------------------------------------------

func TestEnsureBrowserSessionAndContext_SetsSessionAndContext(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())

	chain := []string{"@base", "abc123"}
	ctx := store.WithSandboxLayerChain(context.Background(), chain)
	stub := &toolContextStub{
		Context:   ctx,
		sessionID: "sess-42",
	}

	ensureBrowserSessionAndContext(mgr, stub)

	// Verify that SetRequestContext was called by checking the context
	// is propagated to ContainerEnsureReadyFunc via GetOrLaunch.
	var capturedCtx context.Context
	mgr.SandboxEnabled = true
	mgr.ContainerEnsureReadyFunc = func(gotCtx context.Context, _ string) error {
		capturedCtx = gotCtx
		return nil
	}
	mgr.ContainerResolveFunc = func(_ string) (string, string, error) {
		return "", "", fmt.Errorf("stop")
	}
	_, _ = mgr.GetOrLaunch()

	gotChain := store.SandboxLayerChainFromContext(capturedCtx)
	if len(gotChain) != 2 || gotChain[0] != "@base" || gotChain[1] != "abc123" {
		t.Errorf("request context chain = %v, want [@base abc123]", gotChain)
	}
}

func TestEnsureBrowserSessionAndContext_NilSafe(t *testing.T) {
	// Should not panic with nil mgr or nil ctx.
	ensureBrowserSessionAndContext(nil, nil)

	mgr := browser.NewManager(browser.DefaultConfig())
	ensureBrowserSessionAndContext(mgr, nil)
}
