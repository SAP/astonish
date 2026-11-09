package store

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/question"
)

// BotInteraction is a user decision, never an action execution grant. Payload
// is retained verbatim for reconnecting readers, including unknown metadata.
type BotInteractionKind string

const BotInteractionQuestion BotInteractionKind = "chat_question"

type BotInteraction struct {
	Kind       BotInteractionKind `json:"kind"`
	QuestionID string             `json:"question_id"`
	Payload    json.RawMessage    `json:"payload"`
}
type BotInteractionResponse struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

func (i *BotInteraction) Validate() error {
	if i == nil || i.Kind != BotInteractionQuestion || i.QuestionID == "" || len(i.Payload) > 1<<20 {
		return ErrBotInvalid
	}
	var q question.Question
	if json.Unmarshal(i.Payload, &q) != nil || q.QuestionID != i.QuestionID || q.Validate() != nil {
		return ErrBotInvalid
	}
	return nil
}
func (i *BotInteraction) ValidateResponse(r *BotInteractionResponse) error {
	if i.Validate() != nil || r == nil || r.QuestionID != i.QuestionID {
		return ErrBotInvalid
	}
	var q question.Question
	if json.Unmarshal(i.Payload, &q) != nil || q.ValidateAnswer(r.Answer) != nil {
		return ErrBotInvalid
	}
	return nil
}
