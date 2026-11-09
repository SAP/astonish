package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/tool"
)

// BotActionClassifier is installed by a trusted runtime, never by tool arguments
// or MCP annotations. Implementations must mechanically verify effect constraints;
// names, descriptions, and model claims are not evidence of read-only behavior.
// Arguments contain credential references, not resolved secrets.
type BotActionClassifier interface {
	DescribeAction(context.Context, string, json.RawMessage) (execution.ActionDescriptor, error)
}

// BotPolicyRuntime supplies fresh fencing/budget checks and atomic approval
// consumption. Guard.Authorize must return PolicyAllow with the exact digest
// (and the consumed GrantID for PolicyConsumeGrant) ONLY after those checks.
// This struct is copied into trusted context; it is not a public tool schema.
type BotPolicyRuntime struct {
	Mandate interface {
		Check(context.Context) error
		AuthorizeAction(context.Context, execution.ActionDescriptor) (execution.ScopedGrant, error)
	}
	Guard      execution.PolicyGuard
	Classifier BotActionClassifier
	Approvals  execution.ApprovalRequester
}
type botPolicyRuntimeKey struct{}

func BotPolicyRuntimeFromContext(ctx context.Context) BotPolicyRuntime {
	runtime, _ := ctx.Value(botPolicyRuntimeKey{}).(BotPolicyRuntime)
	return runtime
}

func WithBotPolicyRuntime(ctx context.Context, runtime BotPolicyRuntime) context.Context {
	return context.WithValue(ctx, botPolicyRuntimeKey{}, runtime)
}

// BotPolicyError is a blocked invocation, not a successful tool result.
// ApprovalRequired is separate from Chat's interactive/AutoApprove preference.
type BotPolicyError struct{ Decision execution.PolicyDecision }

func (e *BotPolicyError) Error() string { return "bot policy: " + e.Decision.Reason }
func (e *BotPolicyError) ApprovalRequired() bool {
	return e.Decision.Kind == execution.PolicyRequestApproval
}

func botPolicyDenied(reason string) error {
	return &BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyDeny, Reason: reason}}
}

// BotToolIdentity uses resolved executable identity, never the requested alias.
// MCP/A2A and request-injected tools cannot borrow first-party tool authority.
func BotToolIdentity(group, name string) string {
	if strings.HasPrefix(group, "mcp:") || group == "a2a" || group == "request" {
		return group + "/" + name
	}
	return name
}

// AuthorizeBotTool is shared with future execution-boundary adapters. Ordinary
// Chat is unchanged. Bot callers must invoke this before resolving secrets and
// before any tool/startup side effect. No cached or model-supplied approval counts.
func AuthorizeBotTool(ctx context.Context, identity string, args any) error {
	return authorizeBotTool(ctx, identity, args, nil)
}

func authorizeBotTool(ctx context.Context, identity string, args any, capture *execution.ActionDescriptor) error {
	_, bot := execution.BotRunIdentityFromContext(ctx)
	auth, interactive := execution.BotTurnAuthorizerFromContext(ctx)
	if interactive {
		if !bot || execution.RunPolicyFromContext(ctx) != nil {
			return botPolicyDenied("invalid interactive execution binding")
		}
		if err := auth.AuthorizeTool(ctx, identity); err != nil {
			return err
		}
		if args == nil {
			args = map[string]any{}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return botPolicyDenied("invalid action arguments")
		}
		action := execution.ActionDescriptor{Tool: identity, Arguments: raw, Effect: execution.EffectUnknown}
		if classifier := BotPolicyRuntimeFromContext(ctx).Classifier; classifier != nil {
			classified, err := classifier.DescribeAction(ctx, identity, bytes.Clone(raw))
			if err != nil {
				return botPolicyDenied("action classification failed")
			}
			candidate := action
			candidate.Tool, candidate.Arguments = classified.Tool, classified.Arguments
			want, e1 := action.Digest()
			got, e2 := candidate.Digest()
			if e1 != nil || e2 != nil || want != got {
				return botPolicyDenied("classifier changed executable identity or arguments")
			}
			action = classified
		}
		if capture != nil {
			*capture = action
		}
		return nil
	}
	if !bot && execution.RunPolicyFromContext(ctx) == nil {
		return nil
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return botPolicyDenied("invalid action arguments")
	}
	action := execution.ActionDescriptor{Tool: identity, Effect: execution.EffectUnknown, Arguments: raw}
	// Check identity/principal/tool ceilings before calling any runtime adapter.
	initialAction := action
	initialAction.Effect = execution.EffectRead // unverified: ceiling check only, never authority
	initial := execution.EvaluateRunPolicy(ctx, initialAction, time.Now())
	if initial.Kind == execution.PolicyDeny {
		return &BotPolicyError{Decision: initial}
	}
	runtime, _ := ctx.Value(botPolicyRuntimeKey{}).(BotPolicyRuntime)
	if runtime.Classifier != nil {
		classified, err := runtime.Classifier.DescribeAction(ctx, identity, bytes.Clone(raw))
		if err != nil {
			return botPolicyDenied("action classification failed")
		}
		// A classifier may add verified effect metadata, not replace the invocation.
		original := action
		candidate := action
		candidate.Tool, candidate.Arguments = classified.Tool, classified.Arguments
		want, err := original.Digest()
		got, candidateErr := candidate.Digest()
		if err != nil || candidateErr != nil || want != got {
			return botPolicyDenied("classifier changed executable identity or arguments")
		}
		action = classified
	}
	decision := execution.EvaluateRunPolicy(ctx, action, time.Now())
	if runtime.Mandate != nil {
		if err := runtime.Mandate.Check(ctx); err != nil {
			return botPolicyDenied("mandate no longer valid")
		}
		if decision.Kind == execution.PolicyRequestApproval {
			grant, err := runtime.Mandate.AuthorizeAction(ctx, action)
			if err == nil {
				policy, err := execution.RunPolicyFromContext(ctx).WithGrant(grant)
				if err == nil {
					ctx = execution.WithRunPolicy(ctx, policy)
					decision = execution.EvaluateRunPolicy(ctx, action, time.Now())
				}
			}
		}
	}
	switch decision.Kind {
	case execution.PolicyRequestApproval:
		if runtime.Approvals != nil {
			id, _ := execution.BotRunIdentityFromContext(ctx)
			if _, err := runtime.Approvals.RequestApproval(ctx, id, action, decision.ActionDigest); err != nil {
				return botPolicyDenied("approval request failed")
			}
		}
		return &BotPolicyError{Decision: decision}
	case execution.PolicyAllow, execution.PolicyConsumeGrant:
		if runtime.Guard == nil {
			return botPolicyDenied("missing live execution guard")
		}
		// Authorize is a live operation, not EvaluateRunPolicy: it must fence the
		// lease, check revocation/budget and atomically consume a matching grant.
		// Keep evidence independent of any adapter retaining/mutating its byte slice.
		evidence := action
		evidence.Arguments = bytes.Clone(action.Arguments)
		authorized, err := runtime.Guard.Authorize(ctx, action)
		if err != nil {
			return botPolicyDenied("live execution authorization failed")
		}
		if authorized.Kind != execution.PolicyAllow || authorized.ActionDigest != decision.ActionDigest ||
			(decision.Kind == execution.PolicyConsumeGrant && authorized.GrantID != decision.GrantID) {
			return botPolicyDenied("execution guard did not authorize the exact action")
		}
		if capture != nil {
			*capture = evidence
		}
		return nil
	default:
		return &BotPolicyError{Decision: decision}
	}
}

// An invocation-local, one-use handoff prevents progressive -> lazy MCP from
// consuming the same approval twice. It cannot be supplied in public arguments,
// reused on a second call, or applied to different effective arguments. It is
// accepted ONLY by the lazy boundary, never by a general/child dispatcher.
type botLazyHandoff struct {
	interactive  bool
	identity     execution.BotRunIdentity
	policy       *execution.RunPolicy
	digest       string                     // effective (post-substitution) invocation
	action       execution.ActionDescriptor // approved references and classified target
	actionDigest string
	used         atomic.Bool
}
type botLazyHandoffKey struct{}
type botPolicyToolContext struct {
	tool.Context
	context context.Context
}

func (c *botPolicyToolContext) Value(key any) any { return c.context.Value(key) }

func botInvocationDigest(identity string, args any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return (execution.ActionDescriptor{Tool: identity, Effect: execution.EffectUnknown, Arguments: raw}).Digest()
}

func authorizeBotInvocation(ctx tool.Context, identity string, args any) (tool.Context, error) {
	var action execution.ActionDescriptor
	if err := authorizeBotTool(ctx, identity, args, &action); err != nil {
		return ctx, err
	}
	return botAuthorizedInvocationContext(ctx, identity, args, action)
}

func botAuthorizedInvocationContext(ctx tool.Context, identity string, args any, action execution.ActionDescriptor) (tool.Context, error) {
	id, bot := execution.BotRunIdentityFromContext(ctx)
	if !bot {
		return ctx, nil
	}
	digest, err := botInvocationDigest(identity, args)
	if err != nil {
		return ctx, botPolicyDenied("invalid action arguments")
	}
	action.Arguments = bytes.Clone(action.Arguments)
	actionDigest, err := action.Digest()
	if err != nil || action.Tool != identity {
		return ctx, botPolicyDenied("missing authorized action evidence")
	}
	_, interactive := execution.BotTurnAuthorizerFromContext(ctx)
	handoff := &botLazyHandoff{interactive: interactive, identity: id, policy: execution.RunPolicyFromContext(ctx), digest: digest, action: action, actionDigest: actionDigest}
	return &botPolicyToolContext{Context: ctx, context: context.WithValue(ctx, botLazyHandoffKey{}, handoff)}, nil
}

func authorizeBotLazyInvocation(ctx tool.Context, identity string, args any) error {
	if handoff, ok := ctx.Value(botLazyHandoffKey{}).(*botLazyHandoff); ok {
		id, bot := execution.BotRunIdentityFromContext(ctx)
		digest, err := botInvocationDigest(identity, args)
		if !bot || id != handoff.identity || execution.RunPolicyFromContext(ctx) != handoff.policy || err != nil || digest != handoff.digest || !handoff.used.CompareAndSwap(false, true) {
			return botPolicyDenied("invalid or reused lazy execution handoff")
		}
		return handoff.validateTarget(ctx)
	}
	return AuthorizeBotTool(ctx, identity, args)
}

// Reclassify the approved references, not resolved secrets. This checks every
// classified field without consuming the durable approval a second time.
func (h *botLazyHandoff) validateTarget(ctx context.Context) error {
	if mandate := BotPolicyRuntimeFromContext(ctx).Mandate; mandate != nil {
		if err := mandate.Check(ctx); err != nil {
			return err
		}
	}
	auth, interactive := execution.BotTurnAuthorizerFromContext(ctx)
	if interactive != h.interactive {
		return botPolicyDenied("interactive authorization binding changed")
	}
	if interactive {
		if err := auth.AuthorizeTool(ctx, h.action.Tool); err != nil {
			return err
		}
	}
	action := execution.ActionDescriptor{Tool: h.action.Tool, Arguments: bytes.Clone(h.action.Arguments), Effect: execution.EffectUnknown}
	if classifier := BotPolicyRuntimeFromContext(ctx).Classifier; classifier != nil {
		var err error
		action, err = classifier.DescribeAction(ctx, action.Tool, action.Arguments)
		if err != nil {
			return botPolicyDenied("execution target classification failed")
		}
	}
	digest, err := action.Digest()
	if err != nil || digest != h.actionDigest || action.ConstraintsVerified != h.action.ConstraintsVerified {
		return botPolicyDenied("classified execution target changed")
	}
	return nil
}

func botPolicyProgressiveResult(name string, err error) ProgressiveToolResult {
	state := ProgressiveStateAuthorizationDenied
	if policyErr, ok := err.(*BotPolicyError); ok && policyErr.ApprovalRequired() {
		state = ProgressiveStateApprovalRequired
	}
	return ProgressiveToolResult{State: state, Tool: name, Error: fmt.Sprint(err)}
}
