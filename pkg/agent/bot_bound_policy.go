package agent

import (
	"context"

	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/tool"
)

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
		return err
	}
	return checkTarget(action)
}
