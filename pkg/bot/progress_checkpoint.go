package bot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// ProgressCheckpointer commits private context before advancing the durable
// checkpoint. Callers supply a fresh lease/binding/usage at the settled boundary;
// this helper neither invents evidence nor declares dispatches settled.
type ProgressCheckpointer struct {
	Store    store.BotProgressStore
	Contexts store.BotProgressContextStore
}

func (p ProgressCheckpointer) Commit(ctx context.Context, request store.BotProgressCheckpointRequest, working []byte, observed ProgressObservation, decision ProgressDecision, at time.Time) (*store.BotProgressState, error) {
	if p.Store == nil || p.Contexts == nil {
		return nil, store.ErrBotUnsupported
	}
	if !observed.Settled || observed.Fingerprint == "" || len(working) == 0 || !decision.Decision.Valid() {
		return nil, ErrProgressBoundaryUnsafe
	}
	data, err := json.Marshal(progressWorkingContext{Working: working, Observation: observed, Decision: decision})
	if err != nil || len(data) > store.BotProgressMaxBytes {
		return nil, store.ErrBotInvalid
	}
	// Never modify caller state until both durable writes succeed.
	request.State = *request.State.Clone()
	cp := request.State.Checkpoint
	if cp == nil {
		return nil, store.ErrBotInvalid
	}
	ref, digest, err := p.Contexts.SaveProgressContext(ctx, request.Mutation, data)
	if err != nil {
		return nil, err
	}
	cp.ContextRef, cp.ContextDigest = ref, digest
	cp.LastEvaluation = &store.BotProgressEvaluation{At: at, Decision: decision.Decision, Reason: store.BotProgressReasonCadence, Summary: decision.Summary}
	return p.Store.CheckpointProgress(ctx, request)
}

type progressWorkingContext struct {
	Working     []byte              `json:"working"`
	Observation ProgressObservation `json:"observation"`
	Decision    ProgressDecision    `json:"decision"`
}

// Restore authenticates committed bytes under the freshly acquired lease. It
// restores controller memory only; it never appends an owner turn or executes work.
func (p ProgressCheckpointer) Restore(ctx context.Context, m store.BotProgressMutation, state *store.BotProgressState, c *ProgressController) ([]byte, error) {
	if p.Contexts == nil || c == nil || state == nil {
		return nil, store.ErrBotUnsupported
	}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	cp := state.Checkpoint
	if err := cp.ValidateBinding(m.Binding); err != nil {
		return nil, err
	}
	if m.ExpectedRevision != cp.Revision {
		return nil, store.ErrBotConflict
	}
	data, err := p.Contexts.LoadProgressContext(ctx, m, cp.ContextRef, cp.ContextDigest)
	if err != nil {
		return nil, err
	}
	var saved progressWorkingContext
	if store.BotProgressContextDigest(data) != cp.ContextDigest || json.Unmarshal(data, &saved) != nil || len(saved.Working) == 0 || saved.Observation.Fingerprint == "" || cp.LastEvaluation == nil || saved.Decision.Decision != cp.LastEvaluation.Decision || saved.Decision.Summary != cp.LastEvaluation.Summary {
		return nil, store.ErrBotInvalid
	}
	c.lastAt, c.fingerprint, c.last = cp.LastEvaluation.At, saved.Observation.Fingerprint, &saved.Decision
	return append([]byte(nil), saved.Working...), nil
}
