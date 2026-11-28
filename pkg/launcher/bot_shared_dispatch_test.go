package launcher

import (
	"context"
	"testing"

	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type sharedNextExactGuard struct{}

func (sharedNextExactGuard) Authorize(_ context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	digest, err := action.Digest()
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, nil
}

func TestBotSharedDispatchPreservesRuntimeNext(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			if interactive {
				setBotSharedInteractive(f)
			}
			got, err := f.constructWorker(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			binding := botFactoryModelForTest(t, got.Agent.LLM).binding
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: execution.BotRunIdentity{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Version.Version, ActorID: f.req.Bot.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
			if err != nil {
				t.Fatal(err)
			}
			if !interactive {
				binding.req.Policy = policy
			}
			binding.owner = execution.WithRunPolicy(binding.owner, policy)
			binding.owner = agent.WithBotPolicyRuntime(binding.owner, agent.BotPolicyRuntime{Guard: sharedNextExactGuard{}, Classifier: botFactoryTestClassifier{}})
			entry := got.Agent.ToolIndex.GetToolEntry("read_file").Tool.(*chatRuntimeTool)
			invocation := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: agent.WithBotPrivateWorkerExecution(execution.WithRunPolicy(binding.owner, policy))}, sid: uuid.NewSHA1(f.req.Run.ID, []byte("private-fixture-worker")).String()}}
			bound := botBoundToolContext{Context: invocation, owner: binding.bind(invocation)}
			calls := 0
			result, err := (botSharedDispatch{binding}).Dispatch(bound, entry.chatRuntimeRunnable, map[string]any{"path": "outputs/test"}, func(tool.Context, any) (map[string]any, error) {
				calls++
				return map[string]any{"content": "guarded next"}, nil
			})
			if err != nil || calls != 1 || result["content"] != "guarded next" {
				t.Fatalf("shared continuation bypassed: calls=%d result=%v err=%v", calls, result, err)
			}
			binding.researchOnly = true
			denied := got.Agent.ToolIndex.GetToolEntry("write_file").Tool.(*chatRuntimeTool)
			_, err = (botSharedDispatch{binding}).Dispatch(bound, denied.chatRuntimeRunnable, map[string]any{}, func(tool.Context, any) (map[string]any, error) { calls++; return nil, nil })
			if err != store.ErrBotForbidden || calls != 1 {
				t.Fatalf("research child widened authority: calls=%d err=%v", calls, err)
			}
		})
	}
}
