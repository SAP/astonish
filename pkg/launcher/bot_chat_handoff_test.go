package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
)

type botSingleGrantGuard struct {
	calls int
	grant execution.ScopedGrant
}

func (g *botSingleGrantGuard) Authorize(_ context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	g.calls++
	if g.calls != 1 {
		return execution.PolicyDecision{}, errors.New("grant already consumed")
	}
	digest, _ := a.Digest()
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest, GrantID: g.grant.ID}, nil
}

func TestBotChatFactoryExactGrantDispatchOnce(t *testing.T) {
	f := newBotFactoryFixture(t)
	a := execution.ActionDescriptor{Tool: "approved_probe", Effect: execution.EffectUnknown, Arguments: []byte(`{}`)}
	digest, _ := a.Digest()
	grant := execution.ScopedGrant{ID: uuid.New(), Identity: f.req.Policy.Identity(), ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)}
	var err error
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"approved_probe"}})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Policy, err = f.req.Policy.WithGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	guard := &botSingleGrantGuard{grant: grant}
	effects := 0
	target, err := functiontool.New(functiontool.Config{Name: "approved_probe", Description: "An exact-approved read"}, func(tool.Context, map[string]any) (map[string]any, error) {
		effects++
		return map[string]any{"result": "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := agent.BotPolicyRuntime{Guard: guard, Classifier: BotCatalogueClassifier{}}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err != nil {
			return nil, err
		}
		safe.PolicyRuntime = runtime
		safe.Catalog.Groups = map[string]*agent.ToolGroup{"approved_fixture": {Name: "approved_fixture", Tools: []tool.Tool{target}}}
		return safe, nil
	}
	resources, err := f.constructWorker(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	ctx := execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity())
	ctx = execution.WithRunPolicy(ctx, f.req.Policy)
	ctx = store.WithSessionID(ctx, f.req.Run.SessionID.String())
	ctx = agent.WithBotPolicyRuntime(ctx, runtime)
	var runnable botRunnableTool
	for _, candidate := range resources.Agent.Tools {
		if candidate.Name() == "execute_tool" {
			runnable = candidate.(botRunnableTool)
			break
		}
	}
	if runnable == nil {
		t.Fatal("shared Chat execute_tool missing")
	}
	tc := botFactoryHandoffContext{Context: nil, base: ctx, session: uuid.NewSHA1(f.req.Run.ID, []byte("private-fixture-worker")).String(), functionCallID: "handoff-approved-probe"}
	args := map[string]any{"name": "approved_probe", "arguments": map[string]any{}}
	if _, err := runnable.Run(tc, args); err != nil {
		t.Fatalf("first approved dispatch: effects=%d authorizations=%d err=%v", effects, guard.calls, err)
	}
	if effects != 1 || guard.calls != 1 {
		t.Fatalf("effects=%d authorizations=%d", effects, guard.calls)
	}
	if _, err := runnable.Run(tc, args); err == nil {
		t.Fatal("replayed exact grant")
	}
	if effects != 1 {
		t.Fatalf("replay executed: %d", effects)
	}
}

type botFactoryHandoffContext struct {
	tool.Context
	base           context.Context
	session        string
	functionCallID string
}

func (c botFactoryHandoffContext) FunctionCallID() string { return c.functionCallID }

func TestBotFactoryHandoffContextFunctionCallID(t *testing.T) {
	var ctx tool.Context = botFactoryHandoffContext{base: context.Background(), functionCallID: "handoff-call"}
	for range 2 {
		if got := ctx.FunctionCallID(); got != "handoff-call" {
			t.Fatalf("FunctionCallID() = %q, want handoff-call", got)
		}
	}
}

func (c botFactoryHandoffContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c botFactoryHandoffContext) Value(k any) any                                      { return c.base.Value(k) }
func (c botFactoryHandoffContext) Deadline() (time.Time, bool)                          { return c.base.Deadline() }
func (c botFactoryHandoffContext) Done() <-chan struct{}                                { return c.base.Done() }
func (c botFactoryHandoffContext) Err() error                                           { return c.base.Err() }
func (c botFactoryHandoffContext) SessionID() string                                    { return c.session }
