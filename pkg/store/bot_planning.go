package store

import "encoding/json"

// BotPlanExecutionConsent identifies the exact originating definition. It never
// contains executable plan content or grants permission to a tool leaf.
type BotPlanExecutionConsent struct {
	RunID     RunID  `json:"run_id"`
	SessionID string `json:"session_id"`
	PlanID    string `json:"plan_id"`
	Revision  int64  `json:"revision"`
	Sequence  int64  `json:"sequence"`
}

const BotInteractionPlanDefinition BotInteractionKind = "plan_definition"
const BotPlanDefinitionApprove = "approve"

func (c BotPlanExecutionConsent) Validate() error {
	if c.RunID == [16]byte{} || c.SessionID == "" || c.PlanID == "" || c.Revision < 1 || c.Sequence < 1 {
		return ErrBotInvalid
	}
	return nil
}
func (i *BotInteraction) PlanDefinition() (BotPlanExecutionConsent, error) {
	var c BotPlanExecutionConsent
	if i == nil || i.Kind != BotInteractionPlanDefinition || json.Unmarshal(i.Payload, &c) != nil || c.Validate() != nil || i.QuestionID != c.PlanID {
		return c, ErrBotInvalid
	}
	return c, nil
}
