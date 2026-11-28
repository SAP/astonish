package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

const botAssessmentContextKey = "observed-current-controls"

// The callback is factory-owned; model output and candidate bodies never select
// the read scope. The body and its observation are included in ContentDigest.
func captureBotAssessmentContext(ctx context.Context, resources *BotChatResources, candidate *store.BotWorkCandidate) error {
	// Never retain producer-supplied or stale blockers, including when discovery
	// is unavailable. Only this invocation's trusted callback can establish one.
	candidate.RuntimeBlockers = nil
	if resources.AssessmentContext == nil {
		return nil
	} // historical/injected factories
	raw, observation, err := resources.AssessmentContext(ctx)
	if err != nil {
		return fmt.Errorf("scoped assessment state unavailable: %w", err)
	}
	if len(raw) == 0 || len(raw) > 64*1024 || !json.Valid(raw) || observation.Kind != store.BotEvidenceObservation || observation.Source == "" || observation.ActorID == [16]byte{} || observation.ObservedAt.IsZero() {
		return fmt.Errorf("scoped assessment state invalid: %w", store.ErrBotInvalid)
	}
	if candidate.Bodies == nil {
		candidate.Bodies = make(map[string]string)
	}
	candidate.Bodies[botAssessmentContextKey] = string(raw)
	candidate.Observations = append(candidate.Observations, observation)
	// Parse only bytes returned directly by the factory-owned callback. Never
	// inspect producer bodies/tool responses to establish runtime blockers.
	var facts struct {
		OwnerEmail struct {
			State store.BotOwnerEmailState `json:"state"`
		} `json:"owner_email_delivery"`
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		return store.ErrBotInvalid
	}
	if facts.OwnerEmail.State == store.BotOwnerEmailTransportUnavailable {
		candidate.RuntimeBlockers = []store.BotWorkBlocker{store.NewBotOwnerEmailTransportBlocker(fmt.Sprintf("observation:%d", len(candidate.Observations)-1))}
	}
	return nil
}
