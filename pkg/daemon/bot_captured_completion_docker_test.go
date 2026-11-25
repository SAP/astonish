package daemon

import (
	"context"
	"encoding/json"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"iter"
	"os"
	"strings"
	"testing"
)

type capturedDeliveryModel struct {
	t     *testing.T
	event session.Event
	title bool
}

func (*capturedDeliveryModel) Name() string { return "captured-provider-no-network" }
func (m *capturedDeliveryModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, part := range req.Config.SystemInstruction.Parts {
			if strings.HasPrefix(part.Text, "BOT_WORK_REVIEW_V1") {
				m.t.Log("CAPTURED_REVIEW_MODEL_REACHED: controlled fixture is not independent factual approval")
			}
		}
	}
	if reply, ok := workerWorkAssessment(req, nil); ok {
		return reply
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if !m.title {
			m.title = true
			raw, err := os.ReadFile(os.Getenv("ASTONISH_CAPTURED_TITLE_EVENT"))
			if err != nil {
				m.t.Fatal(err)
			}
			var e session.Event
			if err = json.Unmarshal(raw, &e); err != nil {
				m.t.Fatal(err)
			}
			yield(&e.LLMResponse, nil)
			return
		}
		m.t.Logf("CAPTURED_FINAL id=%s partial=%t complete=%t finish=%s", m.event.ID, m.event.Partial, m.event.TurnComplete, m.event.FinishReason)
		yield(&m.event.LLMResponse, nil)
	}
}
func TestBotCapturedCompletionDockerRecovery(t *testing.T) {
	if os.Getenv("ASTONISH_CAPTURED_FINAL_EVENT") == "" {
		t.Skip("captured event required")
	}
	raw, err := os.ReadFile(os.Getenv("ASTONISH_CAPTURED_FINAL_EVENT"))
	if err != nil {
		t.Fatal(err)
	}
	var e session.Event
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.Partial || !e.TurnComplete || e.ID == "" || e.ErrorCode != "" {
		t.Fatal("incomplete capture")
	}
	runBotExactDockerReproduction(t, &capturedDeliveryModel{t: t, event: e})
}
