package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLinkedBotSessionReviewsBeyondFirstPage(t *testing.T) {
	repo, ss := presentationReviewFixture(t)
	projector := &SessionEventProjector{BotID: repo.b.ID, HomeSessionID: repo.b.HomeSessionID, SessionID: repo.b.HomeSessionID, Repository: repo}
	clean, display, err := projectLinkedBotSession(t.Context(), projector, linkedBotSessionEvents{ss.rows[0].Event})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(display)
	if len(clean) != 1 || !strings.Contains(string(raw), "Late saved question?") {
		t.Fatalf("lost review: %s", raw)
	}
}
