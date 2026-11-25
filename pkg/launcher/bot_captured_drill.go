package launcher

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

type botCapturedDrillClassifier struct {
	owner    context.Context
	fallback agent.BotActionClassifier
}

func (c botCapturedDrillClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if name != "run_drill" {
		return c.fallback.DescribeAction(ctx, name, args)
	}
	bound := botBoundContext{Context: ctx, owner: c.owner}
	snapshot, err := tools.CaptureDrill(bound, store.TeamFlowStoreFromContext(bound), args, c.fallback.DescribeAction)
	if err != nil {
		return execution.ActionDescriptor{}, err
	}
	evidence, err := json.Marshal(snapshot)
	if err != nil {
		return execution.ActionDescriptor{}, err
	}
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown, TargetFingerprint: snapshot.Fingerprint(), TargetSnapshot: evidence}, nil
}
