package agent

import (
	"context"

	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/tool"
)

// PreserveBotInvocationHandoff carries only an existing private dispatch handoff
// across trusted owner rebinding. It does not authorize, consume, copy, or reset
// the token: the terminal boundary still checks its identity, policy, arguments,
// classified target and shared one-use state. Invalid tokens must survive too so
// rebinding cannot turn their rejection into a new authorization attempt.
func PreserveBotInvocationHandoff(bound, invocation context.Context) context.Context {
	if handoff, ok := invocation.Value(botLazyHandoffKey{}).(*botLazyHandoff); ok {
		return context.WithValue(bound, botLazyHandoffKey{}, handoff)
	}
	return bound
}

// WithoutBotInvocationHandoff starts an independent nested invocation. It removes
// only the parent's one-shot handoff, never its policy or live authorization
// requirements. Use at trusted sub-dispatch boundaries, not terminal adapters.
func WithoutBotInvocationHandoff(ctx context.Context) context.Context {
	return context.WithValue(ctx, botLazyHandoffKey{}, struct{}{})
}

// AuthorizeBotBoundTool is the terminal boundary for an owner-bound tool. The
// wrapper must check its live isolation/lease and budget independently. Only an
// invocation-local handoff created by a trusted dispatcher can avoid consuming
// the same approval twice; direct calls still require full live authorization.
// The original invocation is separate because owner rebinding intentionally
// discards ambient context values, including the private dispatch handoff.
func AuthorizeBotBoundTool(bound context.Context, invocation tool.Context, identity string, args any, targetFingerprint ...string) error {
	checkTarget := func(action execution.ActionDescriptor) error {
		if len(targetFingerprint) > 1 || (len(targetFingerprint) == 1 && action.TargetFingerprint != targetFingerprint[0]) {
			return botPolicyDenied("terminal execution target differs from approved target")
		}
		return nil
	}
	if handoff, ok := invocation.Value(botLazyHandoffKey{}).(*botLazyHandoff); ok {
		id, bot := execution.BotRunIdentityFromContext(bound)
		digest, err := botInvocationDigest(identity, args)
		if !bot || id != handoff.identity || execution.RunPolicyFromContext(bound) != handoff.policy || err != nil || digest != handoff.digest || !handoff.used.CompareAndSwap(false, true) {
			return botPolicyDenied("invalid or reused bound execution handoff")
		}
		if err := handoff.validateTarget(bound); err != nil {
			return err
		}
		return checkTarget(handoff.action)
	}
	var action execution.ActionDescriptor
	if err := authorizeBotTool(bound, identity, args, &action); err != nil {
		botRejectedCause(invocation, identity, args, err)
		return err
	}
	return checkTarget(action)
}
