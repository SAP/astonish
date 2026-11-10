package launcher

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
)

func TestBotWebFetchUnavailableThenAlternateSource(t *testing.T) {
	f := newBotFactoryFixture(t)
	base := execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity())
	base, _ = bot.WithToolOutcomes(base)
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: f.req.Run.SessionID.String()}
	for _, status := range []float64{429, 401, 429, 200} {
		result := map[string]any{"status": status, "final_url": "https://example.test/news", "content": "response body"}
		recordBuiltinToolObservation(ctx, ctx.SessionID(), "web_fetch", nil, result)
		outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
		if !ok || !outcome.Verified || len(outcome.Unverified) != 0 || len(outcome.Changed) != 0 || len(outcome.Unchanged) != 1 {
			t.Fatalf("known HTTP response treated as uncertain effect: status=%v outcome=%+v present=%v", status, outcome, ok)
		}
		if status != 200 && !strings.Contains(outcome.Unchanged[0].Summary, "unavailable") {
			t.Fatalf("failed read lost source limitation: %+v", outcome)
		}
	}
}

func TestBotWebFetchUnknownResponseRemainsUnverified(t *testing.T) {
	f := newBotFactoryFixture(t)
	base := execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity())
	base, _ = bot.WithToolOutcomes(base)
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: f.req.Run.SessionID.String()}
	for _, result := range []map[string]any{
		{"status": float64(0), "final_url": "https://example.test", "content": ""},
		{"status": float64(429), "content": "unavailable"},
		{"status": float64(429), "final_url": "https://example.test"},
		{"status": float64(429.5), "final_url": "https://example.test", "content": "unavailable"},
		{"status": float64(429), "final_url": "https://example.test", "content": "unavailable", "error": "transport failure"},
	} {
		recordBuiltinToolObservation(ctx, ctx.SessionID(), "web_fetch", nil, result)
		if outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); ok {
			t.Fatalf("unknown response minted observation: %+v => %+v", result, outcome)
		}
	}
}
