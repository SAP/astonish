package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

// CaptureDefinition is called only after the shared engine persisted the
// announcement snapshot. A model argument or an editable mirror cannot mint it.
func (p *BotPreparedPlanSession) CaptureDefinition(ctx context.Context, s agent.PlanSnapshot) error {
	if !p.Request.PlanMode || !p.Request.Run.IsInteractiveChat() {
		return nil
	}
	if p.capture == nil || s.Document.Lifecycle != "" || s.Document.Results != "" {
		return store.ErrBotForbidden
	}
	c := store.BotPlanExecutionConsent{RunID: p.Request.Run.ID, SessionID: p.Request.Run.SessionID.String(), PlanID: s.PlanID, Revision: s.Revision, Sequence: s.Sequence}
	if c.Validate() != nil {
		return store.ErrBotInvalid
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: raw, ConstraintsVerified: true}
	digest, err := action.Digest()
	if err != nil {
		return err
	}
	capture := p.capture
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.approval != nil {
		return store.ErrBotConflict
	}
	lease := p.Request.Run.Lease
	if p.Request.CurrentLease != nil {
		lease = p.Request.CurrentLease()
	}
	saved, err := capture.store.RequestApproval(ctx, lease, store.BotApproval{BotID: p.Request.Bot.ID, RunID: c.RunID, Version: p.Request.Run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionPlanDefinition, QuestionID: c.PlanID, Payload: raw}, ExpiresAt: time.Now().Add(24 * time.Hour)})
	if err == nil {
		capture.approval = saved
	}
	return err
}
