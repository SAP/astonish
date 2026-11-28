package launcher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
)

func TestBotWorkflowQuestionOwner(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.ctx = bot.WithInteractionOwner(f.ctx)
	call, cleanup := workflowFactory(t, f, "ask_user")
	defer cleanup()
	result, err := call("ask_user", map[string]any{"kind": "yesno", "prompt": "Continue?"})
	if err != nil || result["questionId"] == "" {
		t.Fatalf("question=%v err=%v", result, err)
	}
	f.req.Bot.Status = store.BotPaused
	if _, err := call("ask_user", map[string]any{"kind": "yesno", "prompt": "Again?"}); err == nil {
		t.Fatal("revoked owner asked question")
	}
	action, err := (BotCatalogueClassifier{}).DescribeAction(context.Background(), "ask_user", json.RawMessage(`{}`))
	if err != nil || action.Effect != execution.EffectRead || !action.ConstraintsVerified {
		t.Fatal("question classified as executable action", err)
	}
}

func TestBotBrowserHumanHandoffTruthfulBlocked(t *testing.T) {
	reason := botBrowserUnsupported("browser_request_human")
	for _, want := range []string{"blocked awaiting an explicit owner-bound handoff capability", "no durable resumable browser continuation or takeover surface is available", "no host listener was opened", "no human wait was started"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("handoff limitation missing %q: %s", want, reason)
		}
	}
}
