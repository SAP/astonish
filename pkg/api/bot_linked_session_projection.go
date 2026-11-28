package api

import (
	"context"
	"encoding/json"
	"iter"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

// linkedBotSessionEvents preserves the sanitized compatibility transcript while
// canonical display events retain their stable identities and ordered payloads.
type linkedBotSessionEvents []*session.Event

func (e linkedBotSessionEvents) Len() int                { return len(e) }
func (e linkedBotSessionEvents) At(i int) *session.Event { return e[i] }
func (e linkedBotSessionEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, row := range e {
			if !yield(row) {
				return
			}
		}
	}
}
func projectLinkedBotSession(ctx context.Context, p *SessionEventProjector, events session.Events) (linkedBotSessionEvents, []ChatEvent, error) {
	approvals, err := listBotPresentationApprovals(ctx, p.Repository, p.BotID)
	if err != nil {
		return nil, nil, err
	}
	clean := linkedBotSessionEvents{}
	display := []ChatEvent{}
	for e := range events.All() {
		projection, err := p.ProjectBot(ctx, store.BotTranscriptEvent{Event: e}, approvals)
		if err != nil {
			return nil, nil, err
		}
		display = append(display, projection.Events...)
		if len(projection.Transcript) > 0 {
			var row session.Event
			if err := json.Unmarshal(projection.Transcript, &row); err != nil {
				return nil, nil, err
			}
			clean = append(clean, &row)
		}
	}
	return clean, display, nil
}
