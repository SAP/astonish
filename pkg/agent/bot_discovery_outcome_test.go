package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type discoveryOutcomeContext struct {
	tool.Context
	base context.Context
	id   string
}

func (c discoveryOutcomeContext) Value(key any) any      { return c.base.Value(key) }
func (c discoveryOutcomeContext) FunctionCallID() string { return c.id }

func TestBotDiscoveryOutcomeExactInvocation(t *testing.T) {
	for _, name := range []string{"search_tools", "describe_tools"} {
		for _, path := range []string{"native", "direct"} {
			for _, scenario := range []string{"success", "empty", "lookup-miss", "error", "structured-error", "nil", "revoked", "raw-same-name", "missing-id"} {
				t.Run(name+"/"+path+"/"+scenario, func(t *testing.T) {
					base, _ := bot.WithToolOutcomes(botPolicyContext(t, botBoundaryContext(t), nil, nil))
					identity, _ := execution.BotRunIdentityFromContext(base)
					identity.ActorID = uuid.NewString()
					base = execution.WithBotRunIdentity(base, identity)
					ctx := discoveryOutcomeContext{Context: &contextToolContext{minimalReadonlyContext{Context: base}}, base: base, id: "exact-call"}
					if scenario == "missing-id" {
						ctx.id = ""
					}
					calls := 0
					raw, err := functiontool.New(functiontool.Config{Name: name, Description: "local metadata fixture"}, func(tool.Context, map[string]any) (map[string]any, error) {
						calls++
						switch scenario {
						case "error":
							return nil, errors.New("lookup failed")
						case "structured-error":
							return map[string]any{"error": "lookup failed"}, nil
						case "nil":
							return nil, nil
						case "empty":
							return map[string]any{"tools": []any{}}, nil
						case "lookup-miss":
							return map[string]any{"tools": []any{}, "errors": []string{"excluded target not found"}}, nil
						}
						return map[string]any{bot.ToolOutcomeKey: bot.ToolOutcome{Verified: true}, "call_id": "forged-id"}, nil
					})
					if err != nil {
						t.Fatal(err)
					}
					sealed, err := NewBotDiscoveryTool(raw, func(c tool.Context) (tool.Context, error) {
						if scenario == "revoked" {
							return nil, errors.New("revoked")
						}
						return c, nil
					})
					if err != nil {
						t.Fatal(err)
					}
					candidate := sealed
					if scenario == "raw-same-name" {
						candidate = raw
					}
					if path == "native" {
						_, _ = botNativeCallbacks(nil, nil)[0](ctx, candidate, map[string]any{})
					} else {
						_, _ = candidate.(proposalCaptureRunnable).Run(ctx, map[string]any{})
					}
					want := scenario == "success" || scenario == "empty" || scenario == "lookup-miss"
					outcome, ok := bot.TakeToolOutcome(base, "exact-call")
					if ok != want {
						t.Fatalf("ledger present=%v want=%v: %+v", ok, want, outcome)
					}
					if want {
						if calls != 1 || !outcome.Verified || len(outcome.Unchanged) != 1 || !bot.ValidObservation(outcome.Unchanged[0]) || !strings.Contains(outcome.Unchanged[0].Source, name+"/exact-call") || len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || outcome.NextRunAt != nil || outcome.ScheduleCapture != nil {
							t.Fatalf("not metadata-only: calls=%d outcome=%+v", calls, outcome)
						}
					}
					for _, id := range []string{"exact-call", "forged-id", "other-call", ""} {
						if _, ok := bot.TakeToolOutcome(base, id); ok {
							t.Fatalf("duplicate or forged ledger entry %q", id)
						}
					}
				})
			}
		}
	}
}
