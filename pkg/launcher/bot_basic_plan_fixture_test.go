package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/tools"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// basicPlanNode is a transport fixture: the executable remains Chat's NodeTool.
type basicPlanNode struct {
	call func(string, map[string]any) (json.RawMessage, error)
}

func (*basicPlanNode) BindSession(string)       {}
func (*basicPlanNode) EnsureReady(string) error { return nil }
func (n *basicPlanNode) Call(_ string, name string, args map[string]any) (json.RawMessage, error) {
	return n.call(name, args)
}

// basicPlanWrite executes the production built-in against a disposable node
// filesystem. Only the transport path is translated back to the virtual path.
func basicPlanWrite(t *testing.T) func(string, map[string]any) (json.RawMessage, error) {
	t.Helper()
	root := t.TempDir()
	return func(name string, args map[string]any) (json.RawMessage, error) {
		if name != "write_file" || args["path"] != "/isolated/PLAN.md" {
			return nil, fmt.Errorf("unexpected plan node dispatch: %s %v", name, args)
		}
		content, ok := args["content"].(string)
		if !ok {
			return nil, fmt.Errorf("missing plan content")
		}
		result, err := tools.WriteFile(nil, tools.WriteFileArgs{FilePath: filepath.Join(root, "PLAN.md"), Content: content})
		if err != nil {
			return nil, err
		}
		result.Path = args["path"].(string)
		return json.Marshal(result)
	}
}

type basicPlanPool struct {
	catalogTestPool
	client sandbox.ToolNodeClient
}

func (*basicPlanPool) SetSessionScope(string, string, string)      {}
func (p *basicPlanPool) GetOrCreate(string) sandbox.ToolNodeClient { return p.client }
func (*basicPlanPool) ResolveOwner(id string) string               { return id }

type basicPlanGuard struct{ exact *planExactGrantGuard }

func (g basicPlanGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if a.Tool == "write_file" {
		return g.exact.Authorize(ctx, a)
	}
	return (botFactoryTestGuard{}).Authorize(ctx, a)
}

// Explicit test-owner approval of each exact mirror, independent of the outer
// announce/update approval. No wildcard policy, ordinary write substitution, or
// grant inheritance is involved; every grant is consumed by the real dispatch.
func basicApprovePlanMirrors(t *testing.T, p *scopedChatPlan, guard *planExactGrantGuard, initial *agent.PlanSnapshot, allowed []string) {
	t.Helper()
	commit := func(snapshot agent.PlanSnapshot) error {
		markdown := agent.NewPlanState(snapshot.Goal, snapshot.Document, snapshot.Steps).RenderMarkdown()
		raw, err := json.Marshal(map[string]any{"path": path.Join(p.workspace, "PLAN.md"), "content": markdown})
		if err != nil {
			return err
		}
		digest, err := (execution.ActionDescriptor{Tool: "write_file", Arguments: raw, Effect: execution.EffectUnknown}).Digest()
		if err != nil {
			return err
		}
		identity, _ := execution.BotRunIdentityFromContext(p.invocation)
		grant := execution.ScopedGrant{ID: uuid.New(), Identity: identity, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Minute)}
		policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised, AllowedTools: allowed, Grants: []execution.ScopedGrant{grant}})
		if err != nil {
			return err
		}
		guard.grants[digest] = grant
		invocation := p.invocation
		p.invocation = chatRuntimeToolContext{Context: invocation, bound: execution.WithRunPolicy(invocation, policy)}
		defer func() { p.invocation = invocation }()
		if err := p.commit(snapshot); err != nil {
			return err
		}
		if _, remains := guard.grants[digest]; remains {
			t.Fatal("plan mirror did not consume its independent grant")
		}
		return nil
	}
	options := agent.PlanRuntimeOptions{Workspace: p.workspace, Verify: p.verify, CommitSnapshot: commit}
	p.runtime = agent.NewPlanRuntime(nil, options)
	if initial != nil {
		p.runtime = agent.NewPlanRuntimeFromSnapshot(*initial, options)
	}
}
