package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"sync/atomic"
	"time"
)

type capturedDrillOccurrenceKey struct{}
type capturedDrillOccurrence struct {
	guard  *botResumeGuard
	action execution.ActionDescriptor
	policy *execution.RunPolicy
	turn   execution.BotTurnAuthorizer
	used   atomic.Bool
}

// PreserveCapturedDrillOccurrence copies only a private invocation marker across
// trusted scope rebinding. Neither model input nor ordinary exact resume can mint it.
func PreserveCapturedDrillOccurrence(bound, invocation context.Context) context.Context {
	if marker, ok := invocation.Value(capturedDrillOccurrenceKey{}).(*capturedDrillOccurrence); ok {
		return context.WithValue(bound, capturedDrillOccurrenceKey{}, marker)
	}
	return bound
}

func CapturedDrillForInvocation(ctx context.Context) (*tools.CapturedDrill, error) {
	g, ok := agent.BotPolicyRuntimeFromContext(ctx).Guard.(*botResumeGuard)
	if !ok || g.approval.Action.Tool != "run_drill" {
		return nil, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.attempted {
		return nil, nil
	}
	return tools.DecodeCapturedDrill(g.approval.Action)
}

// CapturedDrillChildContext is called solely by the shared drill executor. It
// claims the next exact occurrence; retries cannot reset this attempt cursor.
func CapturedDrillChildContext(ctx context.Context, name string, args map[string]any) (context.Context, error) {
	g, ok := agent.BotPolicyRuntimeFromContext(ctx).Guard.(*botResumeGuard)
	if !ok || g.approval.Action.Tool != "run_drill" {
		return ctx, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	snapshot, err := tools.DecodeCapturedDrill(g.approval.Action)
	if err != nil || !g.attempted || g.drillCursor >= len(snapshot.Children) {
		return nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expected := snapshot.Children[g.drillCursor]
	runtime := agent.BotPolicyRuntimeFromContext(ctx)
	if runtime.Classifier == nil {
		return nil, store.ErrBotUnsupported
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	action, err := runtime.Classifier.DescribeAction(ctx, name, raw)
	if err != nil {
		return nil, err
	}
	want, _ := expected.Digest()
	got, err := action.Digest()
	if err != nil || want != got {
		return nil, store.ErrBotForbidden
	}
	// Interactive execution retains the original caller gate, not an autonomy
	// policy. The parent approval admits only these exact verified read children.
	if g.req.Policy == nil && g.req.Run.IsInteractiveChat() && g.req.Mandate == nil && g.req.Authorization != nil {
		if execution.RunPolicyFromContext(ctx) != nil || action.Effect != execution.EffectRead || !action.ConstraintsVerified || action.SpendMinor != 0 {
			return nil, store.ErrBotForbidden
		}
		if err := g.req.Authorization.AuthorizeTool(ctx, name); err != nil {
			return nil, err
		}
		g.drillCursor++
		return context.WithValue(ctx, capturedDrillOccurrenceKey{}, &capturedDrillOccurrence{guard: g, action: action, turn: g.req.Authorization}), nil
	}
	// Captured approval never turns unknown shell/send/spend into child rights.
	p := execution.RunPolicyFromContext(ctx)
	if p == nil {
		return nil, store.ErrBotForbidden
	}
	child, err := p.Narrow([]string{name})
	if err != nil {
		return nil, err
	}
	ctx = execution.WithRunPolicy(ctx, child)
	decision := execution.EvaluateRunPolicy(ctx, action, time.Now())
	if decision.Kind != execution.PolicyAllow {
		return nil, store.ErrBotForbidden
	}
	g.drillCursor++
	return context.WithValue(ctx, capturedDrillOccurrenceKey{}, &capturedDrillOccurrence{guard: g, action: action, policy: child}), nil
}
