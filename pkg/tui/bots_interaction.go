package tui

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/question"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"time"
)

type botQuestionKind = question.Kind

const botQuestionYesNo = question.YesNo

type botAnswerOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type botQuestionCard struct {
	AllowCustomAnswer bool              `json:"allow_custom_answer,omitempty"`
	Kind              botQuestionKind   `json:"kind"`
	Prompt            string            `json:"prompt"`
	Options           []botAnswerOption `json:"options"`
}

func botQuestion(a store.BotApproval) botQuestionCard {
	var q botQuestionCard
	if a.Interaction != nil {
		_ = json.Unmarshal(a.Interaction.Payload, &q)
	}
	if q.Kind == botQuestionYesNo {
		q.Options = []botAnswerOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}
	}
	return q
}
func (m model) handleBotAnswerKey(key string) (tea.Model, tea.Cmd) {
	a := m.bots.decisions[m.bots.cursor]
	q := botQuestion(a)
	if a.Interaction == nil || a.Interaction.Validate() != nil || (len(q.Options) == 0 && q.Kind != question.Text) {
		m.bots.err = "Invalid question; reload."
		return m, nil
	}
	editing := q.Kind == question.Text || (q.AllowCustomAnswer && m.bots.answerCursor == len(q.Options))
	if editing && key != "enter" && key != "up" && key != "down" {
		m.bots.response = nil
		if key == "backspace" {
			r := []rune(m.bots.answerText)
			if len(r) > 0 {
				m.bots.answerText = string(r[:len(r)-1])
			}
		} else if key == "space" {
			m.bots.answerText += " "
		} else if len([]rune(key)) == 1 && len([]rune(m.bots.answerText)) < question.MaxAnswerLength {
			m.bots.answerText += key
		}
		return m, nil
	}
	count := len(q.Options)
	if q.AllowCustomAnswer {
		count++
	}
	switch key {
	case "up", "k":
		if m.bots.answerCursor > 0 {
			m.bots.answerCursor--
		}
		m.bots.response = nil
	case "down", "j":
		if m.bots.answerCursor+1 < count {
			m.bots.answerCursor++
		}
		m.bots.response = nil
	case "enter":
		if a.Status != store.ApprovalPending || !a.ExpiresAt.After(time.Now()) || m.bots.err != "" {
			m.bots.err = "Approval changed or expired; reload."
			return m, nil
		}
		if m.bots.response == nil {
			answer := m.bots.answerText
			if !editing {
				answer = q.Options[m.bots.answerCursor].ID
			}
			r := &store.BotInteractionResponse{QuestionID: a.Interaction.QuestionID, Answer: answer}
			if a.Interaction.ValidateResponse(r) != nil {
				return m, nil
			}
			m.bots.response = r
			return m, nil
		}
		r := *m.bots.response
		if a.Interaction.ValidateResponse(&r) != nil {
			m.bots.err = "Invalid answer; reload."
			return m, nil
		}
		m.bots.loading = true
		return m, func() tea.Msg { return m.approveBotContinuation(a, &r) }
	}
	return m, nil
}
func (m model) renderBotQuestion(s *strings.Builder, a store.BotApproval) {
	q := botQuestion(a)
	fmt.Fprintf(s, "\nQuestion: %s\n", q.Prompt)
	for i, o := range q.Options {
		mark := "  "
		if m.bots.answering && i == m.bots.answerCursor {
			mark = "> "
		}
		fmt.Fprintf(s, "%s%s (%s)\n", mark, o.Label, o.ID)
	}
	if q.Kind == question.Text || q.AllowCustomAnswer {
		mark := "  "
		if m.bots.answering && (q.Kind == question.Text || m.bots.answerCursor == len(q.Options)) {
			mark = "> "
		}
		fmt.Fprintf(s, "%sCustom answer: %s\n", mark, m.bots.answerText)
	}
	if m.bots.answering {
		if m.bots.response != nil {
			fmt.Fprintf(s, "Confirm answer %q: Enter submits durable decision; Esc cancels\n", m.bots.response.Answer)
		} else {
			s.WriteString("↑↓ choose answer · Enter reviews · Esc cancels\n")
		}
	}
}
