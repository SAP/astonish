package api

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type deliveryContextModel struct {
	t     *testing.T
	home  string
	bot   string
	calls int
}

func (*deliveryContextModel) Name() string { return "delivery-context" }
func (m *deliveryContextModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		var input map[string]json.RawMessage
		if err := json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &input); err != nil {
			m.t.Fatal(err)
		}
		if len(r.Tools) != 0 || len(r.Config.Tools) != 0 {
			m.t.Error("assessment has tools")
		}
		var delivery struct {
			BotID         string `json:"bot_id"`
			HomeSessionID string `json:"home_session_id"`
			Stage         string `json:"stage"`
			Mechanism     string `json:"mechanism"`
		}
		if err := json.Unmarshal(input["delivery_context"], &delivery); err != nil {
			m.t.Fatal("missing runtime delivery context", err)
		}
		if delivery.BotID != m.bot || delivery.HomeSessionID != m.home || delivery.Stage != "prepublication" {
			m.t.Errorf("wrong trusted context: %+v", delivery)
		}
		if m.home != "" && delivery.Mechanism != "runtime_after_commit" {
			m.t.Error("missing publisher lifecycle")
		}
		if m.home == "" && delivery.Mechanism != "" {
			m.t.Error("missing Home granted publication")
		}
		if string(input["fixture"]) != "true" {
			m.t.Error("flattened data lost")
		}
		if !strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "publication readiness") {
			m.t.Error("missing lifecycle guidance")
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{}`, "model")}, nil)
	}
}
func TestBotWorkDeliveryContextNeverOverridesFailure(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Bot.HomeSessionID = uuid.New()
	req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	ws := &workMemoryStore{state: req.Work.Clone()}
	mdl := &workAssessmentModel{t: t, fail: true}
	calls := 0
	result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, mdl, false, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		calls++
		c.FinalEventID = "candidate-event"
		c.reportBodies = map[string]string{"tool-response:source": "actual source: correct"}
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "correct"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status == store.RunSucceeded || ws.state.Phase == store.BotWorkAccepted {
		t.Fatal("runtime context overrode failing semantic review")
	}
	if calls != 3 || ws.state.Repairs != 2 {
		t.Fatalf("repair bound changed: calls=%d repairs=%d", calls, ws.state.Repairs)
	}
	if len(result.Report.Changed) != 0 {
		t.Fatal("readiness fabricated effect evidence")
	}
}

func TestBotWorkDeliveryContext(t *testing.T) {
	for _, home := range []uuid.UUID{uuid.New(), uuid.Nil} {
		for _, prompt := range []string{botWorkPreparePrompt, botWorkReviewPrompt, botWorkPendingReviewPrompt} {
			ctx, req, _ := botChatTestRequest(t)
			req.Bot.HomeSessionID = home
			homeText := ""
			if home != uuid.Nil {
				homeText = home.String()
			}
			mdl := &deliveryContextModel{t: t, home: homeText, bot: req.Bot.ID.String()}
			input := map[string]any{"fixture": true, "delivery_context": map[string]any{"home_session_id": "foreign", "stage": "delivered", "mechanism": "producer_claim"}}
			before, _ := json.Marshal(input)
			var out map[string]any
			var usage store.BotUsage
			if err := botWorkJSON(ctx, req, mdl, prompt, input, &out, &usage); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(input)
			if string(before) != string(after) || mdl.calls != 1 || usage.ToolCalls != 0 {
				t.Fatal("mutated evidence or extra calls")
			}
		}
	}
}
