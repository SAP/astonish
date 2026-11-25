package api

import "google.golang.org/adk/session"

// A producer-local view, never a persisted-history edit. ADK submits the accepted
// answer as its input; the transactional row remains visible to every reader.
type botAnswerInputSession struct {
	session.Session
	answerID string
}

func (s botAnswerInputSession) Events() session.Events {
	var events []*session.Event
	for e := range s.Session.Events().All() {
		if e == nil || e.ID != s.answerID {
			events = append(events, e)
		}
	}
	return botConversationAppendEvents{appended: events}
}
