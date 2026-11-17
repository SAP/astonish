package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

type expiredAnswerBackend struct {
	explicitContinueBackend
	answers int
}

func (b *expiredAnswerBackend) ApproveBotAndContinue(context.Context, store.BotApproval, *store.BotInteractionResponse) (*store.BotApproveContinueResult, error) {
	b.answers++
	return nil, fmt.Errorf("original authority expired; explicit continuation required")
}
func TestBotExpiredAnswerRequiresExplicitContinue(t *testing.T) {
	b := &expiredAnswerBackend{}
	b.selected = &store.Bot{ID: uuid.New(), Revision: 3}
	b.run = store.BotRun{ID: uuid.New(), BotID: b.selected.ID, ExecutionMode: execution.BotExecutionModeChatInteractive, Status: store.RunInterrupted}
	a := store.BotApproval{ID: uuid.New(), BotID: b.selected.ID, RunID: b.run.ID, ActionDigest: "saved", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"yesno","prompt":"Proceed?"}`)}}
	m := newModel(context.Background(), Config{Backend: b})
	m.bots = botsState{open: true, answering: true, decisions: []store.BotApproval{a}}
	next, cmd := m.handleBotAnswerKey("enter")
	m = next.(model)
	if cmd != nil {
		t.Fatal("review submitted")
	}
	_, cmd = m.handleBotAnswerKey("enter")
	if cmd == nil {
		t.Fatal("no answer command")
	}
	result := cmd().(botDecisionMsg)
	if result.err == nil || b.answers != 1 || b.calls != 0 {
		t.Fatalf("answer renewed authority: %+v calls=%d", result, b.calls)
	}
	next, cmd = m.botCommand("/bots continue")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	next, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || b.calls != 0 {
		t.Fatal("review renewed authority")
	}
	_, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no explicit continuation")
	}
	cmd()
	if b.calls != 1 {
		t.Fatalf("explicit continue calls=%d", b.calls)
	}
}

func titleAnswerApproval(t *testing.T, count int) store.BotApproval {
	t.Helper()
	options := []botAnswerOption{{ID: "forecast", Label: "Compare Boston forecasts"}, {ID: "climate", Label: "Explain Boston seasonal weather"}, {ID: "outing", Label: "Plan a Boston outing"}, {ID: "packing", Label: "Suggest what to pack for Boston"}}
	payload, err := json.Marshal(struct {
		QuestionID string `json:"questionId"`
		botQuestionCard
	}{QuestionID: "title-question", botQuestionCard: botQuestionCard{Kind: "select", Prompt: "What should I do about Boston weather?", Options: options[:count], AllowCustomAnswer: true}})
	if err != nil {
		t.Fatal(err)
	}
	a := store.BotApproval{ID: uuid.New(), BotID: uuid.New(), RunID: uuid.New(), ActionDigest: "title-question-digest", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "title-question", Payload: payload}}
	if err := a.Interaction.Validate(); err != nil {
		t.Fatalf("invalid title question fixture: %v", err)
	}
	return a
}

func TestBotInitialTitleAnswerOptionsCustomAndDuplicate(t *testing.T) {
	for _, count := range []int{3, 4} {
		for _, custom := range []bool{false, true} {
			t.Run(fmt.Sprintf("options=%d/custom=%t", count, custom), func(t *testing.T) {
				a := titleAnswerApproval(t, count)
				b := &gapBotBackend{}
				m := newModel(context.Background(), Config{Backend: b})
				m.bots = botsState{open: true, answering: true, decisions: []store.BotApproval{a}}
				press := func(key tea.KeyPressMsg) tea.Cmd {
					next, cmd := m.handleBotsKey(key)
					m = next.(model)
					return cmd
				}
				want := "climate"
				steps := 1
				if custom {
					steps = count
					want = "Weekend rain 🙂"
				}
				for i := 0; i < steps; i++ {
					if press(tea.KeyPressMsg{Code: tea.KeyDown}) != nil {
						t.Fatal("navigation submitted")
					}
				}
				if custom {
					if press(tea.KeyPressMsg{Code: tea.KeyEnter}) != nil || m.bots.response != nil {
						t.Fatal("blank custom answer accepted")
					}
					for _, r := range want {
						press(tea.KeyPressMsg{Code: r, Text: string(r)})
					}
				}
				if press(tea.KeyPressMsg{Code: tea.KeyEnter}) != nil || b.response != nil {
					t.Fatal("review submitted")
				}
				if m.bots.response == nil || m.bots.response.Answer != want || m.bots.response.QuestionID != "title-question" {
					t.Fatalf("unbound reviewed answer: %+v", m.bots.response)
				}
				cmd := press(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd == nil || !m.bots.loading {
					t.Fatal("missing reviewed submission")
				}
				if duplicate := press(tea.KeyPressMsg{Code: tea.KeyEnter}); duplicate != nil {
					t.Fatal("duplicate submission while loading")
				}
				result := cmd().(botDecisionMsg)
				if result.err != nil || b.response == nil || b.response.Answer != want || b.decision.ID != a.ID || b.decision.RunID != a.RunID || b.decision.ActionDigest != a.ActionDigest {
					t.Fatalf("answer lost exact binding: %+v %+v", result, b.response)
				}
			})
		}
	}
}

func TestBotInitialTitleQuestionEscapeAndRestore(t *testing.T) {
	for _, reviewed := range []bool{false, true} {
		t.Run(fmt.Sprintf("reviewed=%t", reviewed), func(t *testing.T) {
			a := titleAnswerApproval(t, 4)
			b := &gapBotBackend{}
			m := newModel(context.Background(), Config{Backend: b})
			m.bots = botsState{open: true, answering: true, decisions: []store.BotApproval{a}}
			if reviewed {
				next, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd != nil {
					t.Fatal("review submitted")
				}
				m = next.(model)
			}
			next, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
			if cmd != nil || next.(model).bots.open || b.response != nil || b.decision.ID != uuid.Nil {
				t.Fatal("Escape decided or canceled the durable question")
			}
			// A fresh client receives the same persisted review, not a generated question.
			raw, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			var restored store.BotApproval
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.ID != a.ID || restored.Interaction.QuestionID != a.Interaction.QuestionID || restored.Status != store.ApprovalPending || len(botQuestion(restored).Options) != 4 || !botQuestion(restored).AllowCustomAnswer {
				t.Fatalf("restored question changed: %+v", restored)
			}
		})
	}
}

func TestBotInitialTitleAnswerRejectsExpiredOrDecidedQuestion(t *testing.T) {
	for _, status := range []store.ApprovalStatus{store.ApprovalPending, store.ApprovalConsumed, store.ApprovalDenied} {
		t.Run(string(status), func(t *testing.T) {
			a := titleAnswerApproval(t, 3)
			a.Status = status
			if status == store.ApprovalPending {
				a.ExpiresAt = time.Now().Add(-time.Hour)
			}
			m := newModel(context.Background(), Config{Backend: &gapBotBackend{}})
			m.bots = botsState{open: true, answering: true, decisions: []store.BotApproval{a}}
			next, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd != nil || next.(model).bots.response != nil || next.(model).bots.err == "" {
				t.Fatal("expired or decided question accepted")
			}
		})
	}
}
