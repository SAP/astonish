package agent

import (
	"fmt"
	"net/url"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"

	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// botDiscoveryTool is a sealed metadata capability, not an exception for a tool
// name. Only trusted composition wraps its local, already scoped search/describe
// instances. Execution remains on the ordinary exact-action policy path.
type botDiscoveryTool struct {
	proposalCaptureRunnable
	authorize func(tool.Context) (tool.Context, error)
}

// NewBotDiscoveryTool binds a metadata-only helper to live runtime checks and an
// owner-scoped context. Never pass an external or request-injected implementation.
func NewBotDiscoveryTool(t tool.Tool, authorize func(tool.Context) (tool.Context, error)) (tool.Tool, error) {
	r, ok := t.(proposalCaptureRunnable)
	if !ok || authorize == nil || (t.Name() != "search_tools" && t.Name() != "describe_tools") {
		return nil, fmt.Errorf("invalid Bot discovery capability")
	}
	return &botDiscoveryTool{proposalCaptureRunnable: r, authorize: authorize}, nil
}

func (t *botDiscoveryTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	bound, err := t.authorize(ctx)
	if err != nil {
		return nil, err
	}
	result, err := t.proposalCaptureRunnable.Run(bound, args)
	// Only this sealed local implementation can attest to metadata lookup. This
	// is not evidence that a discovered action ran, nor that returned text is true.
	// Keep the caller's exact ID: native dispatch invokes this wrapper once, and
	// execute_tool continues to own only its resolved target's ledger entry.
	if common.ToolResultSucceeded(result, err) {
		if identity, ok := execution.BotRunIdentityFromContext(bound); ok {
			if actor, parseErr := uuid.Parse(identity.ActorID); parseErr == nil {
				bot.RecordToolOutcome(bound, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{
					Kind:       store.BotEvidenceObservation,
					Summary:    "Scoped tool catalog metadata lookup completed; no external action performed",
					Source:     "bot-discovery://" + identity.RunID.String() + "/" + t.Name() + "/" + url.PathEscape(ctx.FunctionCallID()),
					ObservedAt: time.Now().UTC(), ActorID: actor,
				}}})
			}
		}
	}
	return result, err
}

func (t *botDiscoveryTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	// Declaring local metadata does not exercise a capability. In particular a
	// chat-only caller must still be able to converse without invoking discovery.
	if err := t.proposalCaptureRunnable.ProcessRequest(ctx, req); err != nil {
		return err
	}
	if req != nil && req.Tools != nil {
		req.Tools[t.Name()] = t
	}
	return nil
}
