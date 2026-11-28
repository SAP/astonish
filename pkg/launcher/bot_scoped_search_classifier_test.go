package launcher

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

type scopedSearchFallback struct{}

func (scopedSearchFallback) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown}, nil
}

var _ agent.BotActionClassifier = scopedSearchFallback{}

type scopedSearchRejectFallback struct{}

func (scopedSearchRejectFallback) DescribeAction(context.Context, string, json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{}, store.ErrBotForbidden
}

func TestBotScopedSearchClassifierMarksBoundSearchReadOnly(t *testing.T) {
	f := newBotFactoryFixture(t)
	knowledgeSearchSettings(f)
	cfg, err := BotScopedChatConfig(f.ctx, knowledgeScope(f))
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"query":"Tesla pricing news","max_results":5}`)
	classifier := botScopedSearchClassifier{services: f.services, config: cfg, fallback: scopedSearchFallback{}}
	for _, name := range []string{"perplexity_web_search", "configured_search/perplexity_web_search"} {
		action, err := classifier.DescribeAction(f.ctx, name, raw)
		qualified := name == "configured_search/perplexity_web_search"
		wantEffect := execution.EffectUnknown
		if qualified {
			wantEffect = execution.EffectRead
		}
		if err != nil || action.Tool != name || action.Effect != wantEffect || action.ConstraintsVerified != qualified || action.TargetFingerprint == "" {
			t.Fatalf("configured search distinction lost: %+v err=%v", action, err)
		}
	}
	rejecting := botScopedSearchClassifier{services: f.services, config: cfg, fallback: scopedSearchRejectFallback{}}
	qualified := "configured_search/perplexity_web_search"
	if action, err := rejecting.DescribeAction(f.ctx, qualified, raw); err != nil || action.Effect != execution.EffectRead || !action.ConstraintsVerified {
		t.Fatalf("qualified search inherited fallback rejection: %+v err=%v", action, err)
	}
	if _, err := rejecting.DescribeAction(f.ctx, "perplexity_web_search", raw); err == nil {
		t.Fatal("native search bypassed fallback rejection")
	}
	other, err := classifier.DescribeAction(f.ctx, "shell_command", json.RawMessage(`{"command":"true"}`))
	if err != nil || other.Effect != execution.EffectUnknown || other.ConstraintsVerified {
		t.Fatalf("unrelated tool authority expanded: %+v err=%v", other, err)
	}
}
