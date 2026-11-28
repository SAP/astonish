package api

import (
	"context"
	"sync"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// botProgressSessions observes the session already owned by this runner. It never
// replaces durable Get/Append semantics or caches permission decisions. Only
// successfully persisted appends become visible to progress.
type botProgressSessions struct {
	session.Service
	mu                sync.Mutex
	current           session.Session
	app, user, id     string
	cursor            int
	owners, responses []*session.Event
	privateDispatches map[string]*botPrivateEvidenceDispatch
}

func (s *botProgressSessions) valid(v session.Session) bool {
	return v != nil && v.ID() == s.id && v.UserID() == s.user && v.AppName() == s.app
}
func (s *botProgressSessions) Get(ctx context.Context, q *session.GetRequest) (*session.GetResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.Service.Get(ctx, q)
	if err == nil && r != nil && q != nil && q.SessionID == s.id && q.AppName == s.app && q.UserID == s.user && s.valid(r.Session) {
		s.current = r.Session
		s.cursor, s.owners, s.responses, s.privateDispatches = 0, nil, nil, nil
	}
	return r, err
}
func (s *botProgressSessions) AppendEvent(ctx context.Context, v session.Session, e *session.Event) error {
	// Serialize persistence with snapshots: several session implementations mutate
	// their in-memory event slice while AppendEvent is running.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Service.AppendEvent(ctx, v, e); err != nil {
		return err
	}
	if s.valid(v) {
		if s.current != v {
			s.cursor, s.owners, s.responses, s.privateDispatches = 0, nil, nil, nil
		}
		s.current = v
	}
	return nil
}

// snapshot returns the same four latest owner text parts and 64 valid response
// parts used by observeContext, plus the full (not excerpt) transcript high-water.
// New events are inspected once; old irrelevant bodies are never reopened.
func (s *botProgressSessions) snapshot(ctx context.Context) (session.Session, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		r, err := s.Service.Get(ctx, &session.GetRequest{AppName: s.app, UserID: s.user, SessionID: s.id})
		if err != nil {
			return nil, 0, err
		}
		if r != nil {
			s.current = r.Session
		}
	}
	if !s.valid(s.current) {
		return nil, 0, store.ErrBotForbidden
	}
	events := s.current.Events()
	n := events.Len()
	if n < s.cursor {
		s.cursor, s.owners, s.responses, s.privateDispatches = 0, nil, nil, nil
	}
	if s.privateDispatches == nil {
		s.privateDispatches = make(map[string]*botPrivateEvidenceDispatch)
	}
	for i := s.cursor; i < n; i++ {
		e := events.At(i)
		if e == nil || e.Partial {
			continue
		}
		if conversation, ok := s.current.(*botConversationSession); ok && i < len(conversation.prior) {
			if _, private := e.Actions.StateDelta[botEmployeeWorkerTraceKey]; private {
				// Prior Home worker dispatches cannot authorize this run's evidence.
				continue
			}
		}
		content := botProgressEvidenceContent(e, "", s.privateDispatches)
		if content == nil {
			continue
		}
		// Owner selection preserves part order within each event: observeContext
		// takes the first parts of the newest event, not its last parts.
		var ownerParts []*genai.Part
		for _, part := range content.Parts {
			if part == nil || part.Thought {
				continue
			}
			if e.Author == "user" && part.Text != "" && len(ownerParts) < 4 {
				ownerParts = append(ownerParts, &genai.Part{Text: part.Text})
			}
			r := part.FunctionResponse
			if r == nil || r.ID == "" || len(r.ID) > 512 || len(r.Name) > 256 {
				continue
			}
			copy := *e
			copy.Content = &genai.Content{Role: content.Role, Parts: []*genai.Part{{FunctionResponse: r}}}
			copy.Actions = session.EventActions{}
			s.responses = append(s.responses, &copy)
			if len(s.responses) > 64 {
				s.responses = s.responses[1:]
			}
		}
		if len(ownerParts) > 0 {
			copy := *e
			copy.Content = &genai.Content{Role: e.Content.Role, Parts: ownerParts}
			s.owners = append(s.owners, &copy)
			if len(s.owners) > 4 {
				s.owners = s.owners[1:]
			}
		}
	}
	s.cursor = n
	selected := append([]*session.Event(nil), s.responses...)
	// Response events cannot also count as owner text (parts were narrowed above).
	selected = append(selected, s.owners...)
	return botProgressSessionSnapshot{Session: s.current, events: botConversationEvents{prior: selected}}, n, nil
}

// currentResponse is internal bookkeeping, called only after the work service's
// fresh authorization and lease check. It does not replace public transcript reads.
func (s *botProgressSessions) currentResponse(ctx context.Context, callID string) (*store.BotTranscriptEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, nil
	}
	if !s.valid(s.current) {
		return nil, store.ErrBotForbidden
	}
	var events session.Events
	if conversation, ok := s.current.(*botConversationSession); ok {
		// Exclude prior Home events, even when they reuse a call ID.
		events = botConversationAppendEvents{current: conversation.Session.Events(), appended: conversation.appended}
	} else {
		events = s.current.Events()
	}
	dispatches := make(map[string]*botPrivateEvidenceDispatch)
	for e := range events.All() {
		content := botProgressEvidenceContent(e, callID, dispatches)
		if content == nil || content.Role == "model" {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && !part.Thought && part.FunctionResponse != nil && part.FunctionResponse.ID == callID {
				copyEvent := *e
				copyEvent.Content = &genai.Content{Role: content.Role, Parts: []*genai.Part{{FunctionResponse: part.FunctionResponse}}}
				copyEvent.Actions = session.EventActions{}
				return &store.BotTranscriptEvent{Event: &copyEvent}, nil
			}
		}
	}
	return nil, nil
}

// promptEvidence returns a bounded chronological tail of current-run responses.
// It never loads a session: unavailable snapshots use the caller's scoped reader.
// Prior Home history is excluded so it cannot become evidence for this run.
func (s *botProgressSessions) promptEvidence(ctx context.Context, limit int) ([]store.BotTranscriptEvent, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, false, nil
	}
	if !s.valid(s.current) {
		return nil, false, store.ErrBotForbidden
	}
	events := s.current.Events()
	if conversation, ok := s.current.(*botConversationSession); ok {
		events = botConversationAppendEvents{current: conversation.Session.Events(), appended: conversation.appended}
	}
	var rows []store.BotTranscriptEvent
	if limit <= 0 {
		return rows, true, nil
	}
	dispatches := make(map[string]*botPrivateEvidenceDispatch)
	// Binding requires chronological dispatch/call replay, even when only a tail
	// is returned. This read-local state never mutates the incremental snapshot.
	for e := range events.All() {
		content := botProgressEvidenceContent(e, "", dispatches)
		if content == nil || content.Role == "model" {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.Thought || part.FunctionResponse == nil {
				continue
			}
			copyEvent := *e
			copyEvent.Content = &genai.Content{Role: content.Role, Parts: []*genai.Part{{FunctionResponse: part.FunctionResponse}}}
			copyEvent.Actions = session.EventActions{}
			rows = append(rows, store.BotTranscriptEvent{Event: &copyEvent})
			if len(rows) > limit {
				rows = rows[1:]
			}
		}
	}
	return rows, true, nil
}

// botProgressEvidenceContent unwraps only validated private response parts. The
// caller keeps the outer event identity and strips StateDelta from projections,
// so neither worker speech nor private inner event IDs become public evidence.
func botProgressEvidenceContent(e *session.Event, callID string, dispatches map[string]*botPrivateEvidenceDispatch) *genai.Content {
	if e == nil || e.Partial {
		return nil
	}
	if _, private := e.Actions.StateDelta[botEmployeeWorkerTraceKey]; private {
		return botPrivateEvidenceContent(store.BotTranscriptEvent{Event: e}, callID, dispatches)
	}
	return e.Content
}

type botProgressSessionSnapshot struct {
	session.Session
	events session.Events
}

func (s botProgressSessionSnapshot) Events() session.Events { return s.events }
